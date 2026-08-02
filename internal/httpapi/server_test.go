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
	"testing"
	"time"

	"aika/internal/auth"
	"aika/internal/config"
	"aika/internal/database"
	"aika/internal/domain"
	"aika/internal/profilephoto"
	"aika/internal/users"

	"go.uber.org/zap"
)

type fakeTelegram struct{}

func (fakeTelegram) SendLike(context.Context, domain.User, domain.User, string) error { return nil }

func testServer(t *testing.T, devUserID, adminID int64) http.Handler {
	t.Helper()
	store, err := database.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	cfg := config.Config{
		BotToken: "test", AuthMaxAge: time.Hour, LocalDev: true, DevUserID: devUserID,
		DevFirstName: "Test", DevLanguageCode: "en", MiniAppOrigin: "http://localhost:5173",
		AdminTelegramIDs: map[int64]struct{}{adminID: {}}, LikeRatePerMinute: 5,
	}
	photos, err := profilephoto.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return NewServer(cfg, store, users.NewService(store), auth.NewValidator(cfg), fakeTelegram{}, photos, zap.NewNop()).Router()
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
