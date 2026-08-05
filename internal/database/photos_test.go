package database

import (
	"context"
	"errors"
	"testing"
)

func newPhoto(name string) Photo {
	return Photo{
		FilePath:  "users/x/photos/" + name + ".jpg",
		PublicURL: "/profile_photo/users/x/photos/" + name + ".jpg",
		Width:     800, Height: 1000, MIMEType: "image/jpeg",
	}
}

func addPhotos(t *testing.T, store *Store, userID string, names ...string) []Photo {
	t.Helper()
	var photos []Photo
	for _, name := range names {
		var err error
		photos, err = store.AddPhoto(context.Background(), userID, newPhoto(name), 3)
		if err != nil {
			t.Fatalf("add %s: %v", name, err)
		}
	}
	return photos
}

func avatarOf(t *testing.T, store *Store, userID string) string {
	t.Helper()
	user, err := store.GetUserByID(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	return user.CustomPhotoURL.String
}

func TestAddPhotoOrdersAndMirrorsTheAvatar(t *testing.T) {
	store := testStore(t)
	user := testUser(t, store, 1)
	photos := addPhotos(t, store, user.ID, "one", "two")

	if len(photos) != 2 {
		t.Fatalf("photo count = %d, want 2", len(photos))
	}
	if !photos[0].IsPrimary || photos[1].IsPrimary {
		t.Fatalf("primary flags = %v/%v, want the first photo only", photos[0].IsPrimary, photos[1].IsPrimary)
	}
	if photos[0].SortOrder != 0 || photos[1].SortOrder != 1 {
		t.Fatalf("sort orders = %d/%d", photos[0].SortOrder, photos[1].SortOrder)
	}
	if avatar := avatarOf(t, store, user.ID); avatar != photos[0].PublicURL {
		t.Fatalf("avatar = %q, want the primary photo %q", avatar, photos[0].PublicURL)
	}
}

func TestAddPhotoEnforcesTheLimit(t *testing.T) {
	store := testStore(t)
	user := testUser(t, store, 1)
	addPhotos(t, store, user.ID, "one", "two", "three")

	if _, err := store.AddPhoto(context.Background(), user.ID, newPhoto("four"), 3); !errors.Is(err, ErrPhotoLimit) {
		t.Fatalf("fourth photo error = %v, want %v", err, ErrPhotoLimit)
	}
}

func TestSetPrimaryPhotoMovesItToTheFront(t *testing.T) {
	store := testStore(t)
	user := testUser(t, store, 1)
	photos := addPhotos(t, store, user.ID, "one", "two", "three")

	updated, err := store.SetPrimaryPhoto(context.Background(), user.ID, photos[2].ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated[0].ID != photos[2].ID || !updated[0].IsPrimary {
		t.Fatalf("primary photo = %+v, want %s", updated[0], photos[2].ID)
	}
	if updated[1].ID != photos[0].ID || updated[2].ID != photos[1].ID {
		t.Fatal("promoting a photo did not preserve the relative order of the others")
	}
	if avatar := avatarOf(t, store, user.ID); avatar != photos[2].PublicURL {
		t.Fatalf("avatar = %q, want %q", avatar, photos[2].PublicURL)
	}
}

func TestReorderPhotosRejectsForeignAndPartialLists(t *testing.T) {
	store := testStore(t)
	user := testUser(t, store, 1)
	other := testUser(t, store, 2)
	photos := addPhotos(t, store, user.ID, "one", "two")
	foreign := addPhotos(t, store, other.ID, "other")

	ctx := context.Background()
	if _, err := store.ReorderPhotos(ctx, user.ID, []string{photos[0].ID}); !errors.Is(err, ErrPhotoOrderMismatch) {
		t.Fatalf("partial order error = %v, want %v", err, ErrPhotoOrderMismatch)
	}
	if _, err := store.ReorderPhotos(ctx, user.ID, []string{photos[0].ID, foreign[0].ID}); !errors.Is(err, ErrPhotoOrderMismatch) {
		t.Fatalf("foreign photo error = %v, want %v", err, ErrPhotoOrderMismatch)
	}
	if _, err := store.ReorderPhotos(ctx, user.ID, []string{photos[0].ID, photos[0].ID}); !errors.Is(err, ErrPhotoOrderMismatch) {
		t.Fatalf("duplicate photo error = %v, want %v", err, ErrPhotoOrderMismatch)
	}

	reordered, err := store.ReorderPhotos(ctx, user.ID, []string{photos[1].ID, photos[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	if reordered[0].ID != photos[1].ID || !reordered[0].IsPrimary {
		t.Fatal("reordering did not promote the new first photo")
	}
	if avatar := avatarOf(t, store, user.ID); avatar != photos[1].PublicURL {
		t.Fatalf("avatar = %q, want %q", avatar, photos[1].PublicURL)
	}
}

func TestDeletePhotoAuthorizesAndKeepsAnAvatar(t *testing.T) {
	store := testStore(t)
	user := testUser(t, store, 1)
	other := testUser(t, store, 2)
	photos := addPhotos(t, store, user.ID, "one", "two")
	ctx := context.Background()

	if _, _, err := store.DeletePhoto(ctx, other.ID, photos[0].ID, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-account delete error = %v, want %v", err, ErrNotFound)
	}
	removed, remaining, err := store.DeletePhoto(ctx, user.ID, photos[0].ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if removed != photos[0].FilePath {
		t.Fatalf("removed path = %q, want %q", removed, photos[0].FilePath)
	}
	if len(remaining) != 1 || remaining[0].ID != photos[1].ID || !remaining[0].IsPrimary {
		t.Fatalf("remaining gallery = %+v", remaining)
	}
	if _, _, err := store.DeletePhoto(ctx, user.ID, photos[1].ID, false); !errors.Is(err, ErrPhotoRequired) {
		t.Fatalf("last photo delete error = %v, want %v", err, ErrPhotoRequired)
	}
	if _, _, err := store.DeletePhoto(ctx, user.ID, photos[1].ID, true); err != nil {
		t.Fatalf("last photo delete with a Telegram avatar failed: %v", err)
	}
	if avatar := avatarOf(t, store, user.ID); avatar != "" {
		t.Fatalf("avatar = %q, want it cleared once the gallery is empty", avatar)
	}
}

func TestReplacePrimaryPhotoSwapsInOneStep(t *testing.T) {
	store := testStore(t)
	user := testUser(t, store, 1)
	photos := addPhotos(t, store, user.ID, "one", "two")

	removed, updated, err := store.ReplacePrimaryPhoto(context.Background(), user.ID, newPhoto("fresh"))
	if err != nil {
		t.Fatal(err)
	}
	if removed != photos[0].FilePath {
		t.Fatalf("removed path = %q, want the previous primary %q", removed, photos[0].FilePath)
	}
	if len(updated) != 2 || updated[0].PublicURL != newPhoto("fresh").PublicURL || !updated[0].IsPrimary {
		t.Fatalf("gallery after replace = %+v", updated)
	}
	if avatar := avatarOf(t, store, user.ID); avatar != updated[0].PublicURL {
		t.Fatalf("avatar = %q, want %q", avatar, updated[0].PublicURL)
	}
}

func TestListPhotosForGroupsByUser(t *testing.T) {
	store := testStore(t)
	first := testUser(t, store, 1)
	second := testUser(t, store, 2)
	addPhotos(t, store, first.ID, "one", "two")
	addPhotos(t, store, second.ID, "other")

	galleries, err := store.ListPhotosFor(context.Background(), []string{first.ID, second.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(galleries[first.ID]) != 2 || len(galleries[second.ID]) != 1 {
		t.Fatalf("galleries = %+v", galleries)
	}
}
