package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"aika/internal/config"
	"aika/internal/database"
	"aika/internal/domain"
)

// completeProfile fills in the fields the like and message rules require, without going through the
// profile endpoint, so an action test is not also a profile-validation test.
func completeProfile(t *testing.T, store *database.Store, user domain.User) domain.User {
	t.Helper()
	updated, err := store.UpdateProfile(context.Background(), user.ID, domain.ProfileUpdate{
		DisplayName: "Test user", Gender: "other",
		BirthDate: time.Date(1996, 2, 3, 0, 0, 0, 0, time.UTC),
		Purpose:   "chat", CustomPhotoURL: "https://example.com/photo.jpg",
		AppLanguage: "en", IsActive: true, Completed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return updated
}

// actors prepares the authenticated dev user and one target they may act on.
func actors(t *testing.T, environment environment, devUserID int64) domain.User {
	t.Helper()
	authenticateDevUser(t, environment.router)
	sender, err := environment.store.GetUserByTelegramID(context.Background(), devUserID)
	if err != nil {
		t.Fatal(err)
	}
	completeProfile(t, environment.store, sender)
	target, err := environment.store.UpsertTelegramUser(context.Background(), domain.TelegramProfile{
		UserID: devUserID + 1, FirstName: "Target", LanguageCode: "en", PhotoURLKnown: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return completeProfile(t, environment.store, target)
}

func post(t *testing.T, router http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	request.Header.Set("Authorization", "dev")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestLikeIsRefusedUntilTheCooldownExpires(t *testing.T) {
	environment := testEnvironment(t, 123, 999)
	target := actors(t, environment, 123)

	first := post(t, environment.router, "/api/users/"+target.ID+"/like", "{}")
	if first.Code != http.StatusOK {
		t.Fatalf("first like status = %d, body = %s", first.Code, first.Body.String())
	}
	var success actionResponse
	if err := json.NewDecoder(first.Body).Decode(&success); err != nil {
		t.Fatal(err)
	}
	if success.Action != database.ActionLike || success.NextAllowedAt == "" || success.RetryAfterSeconds <= 0 {
		t.Fatalf("first like response = %+v", success)
	}

	second := post(t, environment.router, "/api/users/"+target.ID+"/like", "{}")
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second like status = %d, want 429; body = %s", second.Code, second.Body.String())
	}
	var refusal struct {
		Error             errorDetail `json:"error"`
		Code              string      `json:"code"`
		NextAllowedAt     string      `json:"next_allowed_at"`
		RetryAfterSeconds int64       `json:"retry_after_seconds"`
	}
	if err := json.NewDecoder(second.Body).Decode(&refusal); err != nil {
		t.Fatal(err)
	}
	if refusal.Code != "like_cooldown_active" || refusal.Error.Code != "like_cooldown_active" {
		t.Fatalf("refusal codes = %q / %q", refusal.Code, refusal.Error.Code)
	}
	if refusal.NextAllowedAt != success.NextAllowedAt {
		t.Fatalf("refusal deadline = %q, want the first one %q", refusal.NextAllowedAt, success.NextAllowedAt)
	}
	if refusal.RetryAfterSeconds <= 0 || second.Header().Get("Retry-After") == "" {
		t.Fatalf("retry hint = %d, header = %q", refusal.RetryAfterSeconds, second.Header().Get("Retry-After"))
	}
}

func TestLikeSucceedsAgainAfterTheCooldown(t *testing.T) {
	environment := testEnvironment(t, 123, 999, func(cfg *config.Config) { cfg.ActionCooldown = 150 * time.Millisecond })
	target := actors(t, environment, 123)

	if response := post(t, environment.router, "/api/users/"+target.ID+"/like", "{}"); response.Code != http.StatusOK {
		t.Fatalf("first like status = %d", response.Code)
	}
	if response := post(t, environment.router, "/api/users/"+target.ID+"/like", "{}"); response.Code != http.StatusTooManyRequests {
		t.Fatalf("second like status = %d, want 429", response.Code)
	}
	time.Sleep(250 * time.Millisecond)
	if response := post(t, environment.router, "/api/users/"+target.ID+"/like", "{}"); response.Code != http.StatusOK {
		t.Fatalf("like after the cooldown status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestLikeAndMessageCooldownsAreIndependent(t *testing.T) {
	environment := testEnvironment(t, 123, 999)
	target := actors(t, environment, 123)

	if response := post(t, environment.router, "/api/users/"+target.ID+"/like", "{}"); response.Code != http.StatusOK {
		t.Fatalf("like status = %d", response.Code)
	}
	message := post(t, environment.router, "/api/users/"+target.ID+"/message", `{"message":"hello"}`)
	if message.Code != http.StatusOK {
		t.Fatalf("message status = %d, want 200 while a like is active; body = %s", message.Code, message.Body.String())
	}
	if again := post(t, environment.router, "/api/users/"+target.ID+"/message", `{"message":"hello again"}`); again.Code != http.StatusTooManyRequests {
		t.Fatalf("second message status = %d, want 429", again.Code)
	}
	if like := post(t, environment.router, "/api/users/"+target.ID+"/like", "{}"); like.Code != http.StatusTooManyRequests {
		t.Fatalf("like status after messaging = %d, want the like timer still active", like.Code)
	}

	cooldowns := httptest.NewRequest(http.MethodGet, "/api/users/"+target.ID+"/cooldowns", nil)
	cooldowns.Header.Set("Authorization", "dev")
	recorder := httptest.NewRecorder()
	environment.router.ServeHTTP(recorder, cooldowns)
	var state cooldownsResponse
	if err := json.NewDecoder(recorder.Body).Decode(&state); err != nil {
		t.Fatal(err)
	}
	if len(state.Cooldowns) != 2 || state.Cooldowns[database.ActionLike].RetryAfterSeconds <= 0 || state.Cooldowns[database.ActionMessage].RetryAfterSeconds <= 0 {
		t.Fatalf("cooldown state = %+v", state)
	}
}

func TestConcurrentLikesProduceOneAction(t *testing.T) {
	environment := testEnvironment(t, 123, 999)
	target := actors(t, environment, 123)

	var wait sync.WaitGroup
	codes := make(chan int, 8)
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			codes <- post(t, environment.router, "/api/users/"+target.ID+"/like", "{}").Code
		}()
	}
	wait.Wait()
	close(codes)
	accepted := 0
	for code := range codes {
		switch code {
		case http.StatusOK:
			accepted++
		case http.StatusTooManyRequests:
		default:
			t.Fatalf("unexpected status %d", code)
		}
	}
	if accepted != 1 {
		t.Fatalf("%d of 8 simultaneous likes were accepted, want exactly 1", accepted)
	}
}

func TestMessageEndpointRequiresText(t *testing.T) {
	environment := testEnvironment(t, 123, 999)
	target := actors(t, environment, 123)

	if response := post(t, environment.router, "/api/users/"+target.ID+"/message", "{}"); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("empty message status = %d, want 422", response.Code)
	}
	if response := post(t, environment.router, "/api/users/"+target.ID+"/message", `{"message":"   "}`); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("blank message status = %d, want 422", response.Code)
	}
	// A refused message must not consume the timer.
	if response := post(t, environment.router, "/api/users/"+target.ID+"/message", `{"message":"hi"}`); response.Code != http.StatusOK {
		t.Fatalf("valid message status = %d, body = %s", response.Code, response.Body.String())
	}
}
