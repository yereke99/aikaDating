package database

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"aika/internal/domain"
)

func TestUpsertTelegramUserDoesNotCreateDuplicates(t *testing.T) {
	store, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first, err := store.UpsertTelegramUser(context.Background(), domain.TelegramProfile{
		UserID: 42, FirstName: "First", LanguageCode: "en", PhotoURLKnown: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.UpsertTelegramUser(context.Background(), domain.TelegramProfile{
		UserID: 42, FirstName: "Updated", LanguageCode: "en", PhotoURLKnown: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("upsert changed internal ID: %s != %s", first.ID, second.ID)
	}
	if !second.FirstName.Valid || second.FirstName.String != "Updated" {
		t.Fatalf("mutable Telegram data was not updated: %+v", second.FirstName)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM users WHERE telegram_user_id = 42`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("user count = %d, want 1", count)
	}
}

func TestMigrationsAreIdempotentAndBackfillAvatarsOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aikabot.db")
	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.UpsertTelegramUser(context.Background(), domain.TelegramProfile{
		UserID: 7, FirstName: "Backfill", LanguageCode: "en", PhotoURLKnown: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE users SET custom_photo_url = ? WHERE id = ?`,
		"/profile_photo/"+user.ID+".jpg", user.ID); err != nil {
		t.Fatal(err)
	}
	store.Close()

	// A second start replays every migration file, the way a redeploy does.
	for range 2 {
		store, err = Open(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		store.Close()
	}
	store, err = Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	photos, err := store.ListPhotos(context.Background(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(photos) != 1 {
		t.Fatalf("backfilled photo count = %d, want 1", len(photos))
	}
	if photos[0].FilePath != user.ID+".jpg" || !photos[0].IsPrimary {
		t.Fatalf("backfilled photo = %+v", photos[0])
	}
}

func TestDownMigrationDropsOnlyTheNewTables(t *testing.T) {
	store, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	down, err := os.ReadFile(filepath.Join("..", "..", "migrations", "000002_photos_and_cooldowns.down.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(string(down)); err != nil {
		t.Fatalf("down migration failed: %v", err)
	}
	var users int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&users); err != nil {
		t.Fatalf("users table did not survive the rollback: %v", err)
	}
	if _, err := store.db.Exec(`SELECT 1 FROM user_photos`); err == nil {
		t.Fatal("user_photos still exists after the rollback")
	}
}
