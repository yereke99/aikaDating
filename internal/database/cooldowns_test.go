package database

import (
	"context"
	"sync"
	"testing"
	"time"

	"aika/internal/domain"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	return store
}

func testUser(t *testing.T, store *Store, telegramID int64) domain.User {
	t.Helper()
	user, err := store.UpsertTelegramUser(context.Background(), domain.TelegramProfile{
		UserID: telegramID, FirstName: "User", LanguageCode: "en", PhotoURLKnown: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return user
}

func TestClaimActionEnforcesWindow(t *testing.T) {
	store := testStore(t)
	actor := testUser(t, store, 1)
	target := testUser(t, store, 2)
	ctx := context.Background()
	start := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	window := 30 * time.Minute

	first, granted, err := store.ClaimAction(ctx, actor.ID, target.ID, ActionLike, start, window)
	if err != nil || !granted {
		t.Fatalf("first claim granted = %v, err = %v", granted, err)
	}
	if !first.NextAllowedAt.Equal(start.Add(window)) {
		t.Fatalf("next allowed at = %v, want %v", first.NextAllowedAt, start.Add(window))
	}

	second, granted, err := store.ClaimAction(ctx, actor.ID, target.ID, ActionLike, start.Add(29*time.Minute), window)
	if err != nil {
		t.Fatal(err)
	}
	if granted {
		t.Fatal("a second like inside the window was granted")
	}
	if !second.NextAllowedAt.Equal(first.NextAllowedAt) {
		t.Fatalf("refused claim reported %v, want the original deadline %v", second.NextAllowedAt, first.NextAllowedAt)
	}

	after := start.Add(window).Add(time.Second)
	third, granted, err := store.ClaimAction(ctx, actor.ID, target.ID, ActionLike, after, window)
	if err != nil || !granted {
		t.Fatalf("claim after the window granted = %v, err = %v", granted, err)
	}
	if !third.NextAllowedAt.Equal(after.Add(window)) {
		t.Fatalf("renewed deadline = %v, want %v", third.NextAllowedAt, after.Add(window))
	}
}

func TestClaimActionKeepsLikeAndMessageIndependent(t *testing.T) {
	store := testStore(t)
	actor := testUser(t, store, 1)
	target := testUser(t, store, 2)
	ctx := context.Background()
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

	if _, granted, err := store.ClaimAction(ctx, actor.ID, target.ID, ActionLike, now, time.Hour); err != nil || !granted {
		t.Fatalf("like claim granted = %v, err = %v", granted, err)
	}
	if _, granted, err := store.ClaimAction(ctx, actor.ID, target.ID, ActionMessage, now, time.Hour); err != nil || !granted {
		t.Fatal("an active like blocked the message action")
	}
	if _, granted, err := store.ClaimAction(ctx, actor.ID, target.ID, ActionLike, now, time.Hour); err != nil || granted {
		t.Fatal("the like window was reset by the message action")
	}
}

func TestClaimActionIsAtomicUnderConcurrency(t *testing.T) {
	store := testStore(t)
	actor := testUser(t, store, 1)
	target := testUser(t, store, 2)
	ctx := context.Background()
	now := time.Now().UTC()

	var wait sync.WaitGroup
	results := make(chan bool, 16)
	for range 16 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, granted, err := store.ClaimAction(ctx, actor.ID, target.ID, ActionLike, now, 30*time.Minute)
			if err != nil {
				t.Error(err)
				return
			}
			results <- granted
		}()
	}
	wait.Wait()
	close(results)
	granted := 0
	for result := range results {
		if result {
			granted++
		}
	}
	if granted != 1 {
		t.Fatalf("%d of 16 concurrent claims were granted, want exactly 1", granted)
	}
}

func TestReleaseActionOnlyRemovesTheClaimItOwns(t *testing.T) {
	store := testStore(t)
	actor := testUser(t, store, 1)
	target := testUser(t, store, 2)
	ctx := context.Background()
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

	claim, _, err := store.ClaimAction(ctx, actor.ID, target.ID, ActionMessage, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReleaseAction(ctx, actor.ID, target.ID, ActionMessage, now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, granted, _ := store.ClaimAction(ctx, actor.ID, target.ID, ActionMessage, now, time.Hour); granted {
		t.Fatal("a release with the wrong deadline removed an active claim")
	}
	if err := store.ReleaseAction(ctx, actor.ID, target.ID, ActionMessage, claim.NextAllowedAt); err != nil {
		t.Fatal(err)
	}
	if _, granted, _ := store.ClaimAction(ctx, actor.ID, target.ID, ActionMessage, now, time.Hour); !granted {
		t.Fatal("the action stayed blocked after its own claim was released")
	}
}

func TestActiveCooldownsSkipsExpiredRows(t *testing.T) {
	store := testStore(t)
	actor := testUser(t, store, 1)
	target := testUser(t, store, 2)
	ctx := context.Background()
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

	if _, _, err := store.ClaimAction(ctx, actor.ID, target.ID, ActionLike, now, 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	active, err := store.ActiveCooldowns(ctx, actor.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := active[target.ID][ActionLike]; !ok {
		t.Fatal("an active cooldown was not reported")
	}
	expired, err := store.ActiveCooldowns(ctx, actor.ID, now.Add(11*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 0 {
		t.Fatalf("expired cooldowns were reported: %+v", expired)
	}
}
