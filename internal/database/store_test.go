package database

import (
	"context"
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
