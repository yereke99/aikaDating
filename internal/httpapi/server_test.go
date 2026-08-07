package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"aika/internal/auth"
	"aika/internal/calls"
	"aika/internal/config"
	"aika/internal/database"
	"aika/internal/domain"
	"aika/internal/profilephoto"
	"aika/internal/users"

	"go.uber.org/zap"
)

// fakeTelegram stands in for the bot client. It also implements the optional call-ringing
// interface, so the "notify a callee who is not in the app" path is exercised rather than skipped.
type fakeTelegram struct {
	mu    sync.Mutex
	rings []string
}

func (*fakeTelegram) SendLike(context.Context, domain.User, domain.User, string) error { return nil }

func (f *fakeTelegram) SendCallInvite(_ context.Context, recipient, _ domain.User, callID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rings = append(f.rings, recipient.ID+":"+callID)
	return nil
}

// ringsFor waits briefly for the background notification, which is sent off the request path.
func (f *fakeTelegram) ringsFor(userID string) []string {
	for attempt := 0; attempt < 50; attempt++ {
		f.mu.Lock()
		matched := make([]string, 0, len(f.rings))
		for _, ring := range f.rings {
			if strings.HasPrefix(ring, userID+":") {
				matched = append(matched, ring)
			}
		}
		f.mu.Unlock()
		if len(matched) > 0 {
			return matched
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil
}

// environment is one wired-up server plus the pieces a test needs to arrange state directly.
type environment struct {
	router   http.Handler
	store    *database.Store
	photos   *profilephoto.Store
	calls    *calls.Registry
	telegram *fakeTelegram
	cfg      config.Config
}

func testEnvironment(t *testing.T, devUserID, adminID int64, adjust ...func(*config.Config)) environment {
	t.Helper()
	store, err := database.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	cfg := config.Config{
		BotToken: "test", AuthMaxAge: time.Hour, LocalDev: true, DevUserID: devUserID,
		DevFirstName: "Test", DevLanguageCode: "en", MiniAppOrigin: "http://localhost:5173",
		AdminTelegramIDs: map[int64]struct{}{adminID: {}}, LikeRatePerMinute: 60,
		ActionCooldown: 30 * time.Minute, MaxProfilePhotos: 4,
		Calls: config.CallConfig{
			Enabled: true, InviteTimeout: 45 * time.Second, SetupTimeout: time.Minute,
			EventWait: 50 * time.Millisecond, PresenceTimeout: 45 * time.Second,
			STUNURLs: []string{"stun:stun.example.org:3478"}, TURNCredentialTTL: time.Hour,
		},
	}
	for _, apply := range adjust {
		apply(&cfg)
	}
	photos, err := profilephoto.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry := calls.NewRegistry(calls.Settings{
		InviteTimeout:   cfg.Calls.InviteTimeout,
		SetupTimeout:    cfg.Calls.SetupTimeout,
		PresenceTimeout: cfg.Calls.PresenceTimeout,
	})
	bot := &fakeTelegram{}
	router := NewServer(cfg, store, users.NewService(store), auth.NewValidator(cfg), bot, photos, registry, zap.NewNop()).Router()
	return environment{router: router, store: store, photos: photos, calls: registry, telegram: bot, cfg: cfg}
}

func testServer(t *testing.T, devUserID, adminID int64) http.Handler {
	t.Helper()
	return testEnvironment(t, devUserID, adminID).router
}

func authenticateDevUser(t *testing.T, router http.Handler) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/auth/telegram", nil)
	request.Header.Set("Authorization", "dev")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("authentication status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestAdminAuthorizationRejectsOrdinaryUser(t *testing.T) {
	router := testServer(t, 123, 999)
	authenticateDevUser(t, router)
	request := httptest.NewRequest(http.MethodGet, "/api/admin/stats", nil)
	request.Header.Set("Authorization", "dev")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("admin status = %d, want 403; body = %s", response.Code, response.Body.String())
	}
}

func TestAdminAuthorizationAllowsConfiguredUser(t *testing.T) {
	router := testServer(t, 999, 999)
	authenticateDevUser(t, router)
	request := httptest.NewRequest(http.MethodGet, "/api/admin/stats", nil)
	request.Header.Set("Authorization", "dev")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("admin status = %d, want 200; body = %s", response.Code, response.Body.String())
	}
}

func TestProfilePhotoUploadAndPublicDelivery(t *testing.T) {
	router := testServer(t, 123, 999)
	authenticateDevUser(t, router)

	photo := image.NewRGBA(image.Rect(0, 0, 100, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 100; x++ {
			photo.Set(x, y, color.RGBA{R: 190, G: 148, B: 64, A: 255})
		}
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("photo", "selfie.png")
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(part, photo); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/me/photo", &body)
	request.Header.Set("Authorization", "dev")
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("upload status = %d, body = %s", response.Code, response.Body.String())
	}
	var me meResponse
	if err := json.NewDecoder(response.Body).Decode(&me); err != nil {
		t.Fatal(err)
	}
	if me.CustomPhotoURL == "" {
		t.Fatal("upload response has no custom photo URL")
	}

	photoRequest := httptest.NewRequest(http.MethodGet, me.CustomPhotoURL, nil)
	photoResponse := httptest.NewRecorder()
	router.ServeHTTP(photoResponse, photoRequest)
	if photoResponse.Code != http.StatusOK || photoResponse.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("photo response status = %d, content type = %q", photoResponse.Code, photoResponse.Header().Get("Content-Type"))
	}
}

func TestNearbyRespectsRadiusAndRevalidatesWithAnEntityTag(t *testing.T) {
	environment := testEnvironment(t, 123, 999)
	authenticateDevUser(t, environment.router)
	ctx := context.Background()

	viewer, err := environment.store.GetUserByTelegramID(ctx, 123)
	if err != nil {
		t.Fatal(err)
	}
	completeProfile(t, environment.store, viewer)
	if err := environment.store.UpdateLocation(ctx, viewer.ID, 43.238949, 76.889709); err != nil {
		t.Fatal(err)
	}
	// One neighbour a few kilometres away and one in another city.
	for index, coordinates := range [][2]float64{{43.24, 76.89}, {51.1694, 71.4491}} {
		neighbour, err := environment.store.UpsertTelegramUser(ctx, strangerProfile(int64(500+index)))
		if err != nil {
			t.Fatal(err)
		}
		completeProfile(t, environment.store, neighbour)
		if err := environment.store.UpdateLocation(ctx, neighbour.ID, coordinates[0], coordinates[1]); err != nil {
			t.Fatal(err)
		}
	}

	near := authorized(t, environment.router, http.MethodGet, "/api/users/nearby?radius_km=5&page=1&limit=20", "")
	if near.Code != http.StatusOK {
		t.Fatalf("nearby status = %d, body = %s", near.Code, near.Body.String())
	}
	var page users.NearbyPage
	if err := json.NewDecoder(near.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	if len(page.Users) != 1 {
		t.Fatalf("users within 5 km = %d, want 1", len(page.Users))
	}
	if page.ServerTime == "" {
		t.Fatal("nearby response carries no server time for countdown synchronisation")
	}

	wide := authorized(t, environment.router, http.MethodGet, "/api/users/nearby?radius_km=500&page=1&limit=20", "")
	var widePage users.NearbyPage
	if err := json.NewDecoder(wide.Body).Decode(&widePage); err != nil {
		t.Fatal(err)
	}
	if len(widePage.Users) != 1 {
		t.Fatalf("users within 500 km = %d, want 1 (the other city is farther)", len(widePage.Users))
	}

	tag := near.Header().Get("ETag")
	if tag == "" {
		t.Fatal("nearby response has no ETag")
	}
	request := httptest.NewRequest(http.MethodGet, "/api/users/nearby?radius_km=5&page=1&limit=20", nil)
	request.Header.Set("Authorization", "dev")
	request.Header.Set("If-None-Match", tag)
	revalidated := httptest.NewRecorder()
	environment.router.ServeHTTP(revalidated, request)
	if revalidated.Code != http.StatusNotModified {
		t.Fatalf("revalidation status = %d, want 304", revalidated.Code)
	}
	if revalidated.Body.Len() != 0 {
		t.Fatalf("304 response carried a body of %d bytes", revalidated.Body.Len())
	}
}

func TestNearbyRejectsAnUnsupportedRadius(t *testing.T) {
	router := testServer(t, 123, 999)
	authenticateDevUser(t, router)
	if response := authorized(t, router, http.MethodGet, "/api/users/nearby?radius_km=7", ""); response.Code != http.StatusBadRequest {
		t.Fatalf("unsupported radius status = %d, want 400", response.Code)
	}
}
