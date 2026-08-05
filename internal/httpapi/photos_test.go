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
	"testing"

	"aika/internal/database"
	"aika/internal/domain"
)

func pngBytes(t *testing.T, size int) []byte {
	t.Helper()
	source := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := range size {
		for x := range size {
			source.Set(x, y, color.RGBA{R: uint8(x % 255), G: 148, B: 64, A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, source); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func uploadPhoto(t *testing.T, router http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("photo", "selfie.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(pngBytes(t, 120)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, path, &body)
	request.Header.Set("Authorization", "dev")
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func authorized(t *testing.T, router http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "dev")
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestGalleryUploadStopsAtTheConfiguredLimit(t *testing.T) {
	environment := testEnvironment(t, 123, 999)
	authenticateDevUser(t, environment.router)

	var gallery galleryResponse
	for index := range environment.cfg.MaxProfilePhotos {
		response := uploadPhoto(t, environment.router, "/api/me/photos")
		if response.Code != http.StatusOK {
			t.Fatalf("upload %d status = %d, body = %s", index+1, response.Code, response.Body.String())
		}
		gallery = galleryResponse{}
		if err := json.NewDecoder(response.Body).Decode(&gallery); err != nil {
			t.Fatal(err)
		}
		if len(gallery.Photos) != index+1 {
			t.Fatalf("gallery size after upload %d = %d", index+1, len(gallery.Photos))
		}
	}
	if !gallery.Photos[0].IsPrimary || gallery.Photos[0].ThumbURL == "" {
		t.Fatalf("first photo = %+v, want a primary photo with a thumbnail", gallery.Photos[0])
	}
	if !strings.HasPrefix(gallery.Photos[0].URL, "/profile_photo/users/") {
		t.Fatalf("photo URL = %q, want it under the owner's directory", gallery.Photos[0].URL)
	}

	overflow := uploadPhoto(t, environment.router, "/api/me/photos")
	if overflow.Code != http.StatusConflict {
		t.Fatalf("upload past the limit status = %d, want 409; body = %s", overflow.Code, overflow.Body.String())
	}
}

func TestGalleryStoresPhotosUnderTheOwnerDirectory(t *testing.T) {
	environment := testEnvironment(t, 123, 999)
	authenticateDevUser(t, environment.router)
	response := uploadPhoto(t, environment.router, "/api/me/photos")
	if response.Code != http.StatusOK {
		t.Fatalf("upload status = %d", response.Code)
	}
	var gallery galleryResponse
	if err := json.NewDecoder(response.Body).Decode(&gallery); err != nil {
		t.Fatal(err)
	}
	user, err := environment.store.GetUserByTelegramID(context.Background(), 123)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gallery.Photos[0].URL, "/users/"+user.ID+"/photos/") {
		t.Fatalf("photo URL = %q, want it inside the authenticated user's directory", gallery.Photos[0].URL)
	}
	served := authorized(t, environment.router, http.MethodGet, gallery.Photos[0].URL, "")
	if served.Code != http.StatusOK || served.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("serve status = %d, content type = %q", served.Code, served.Header().Get("Content-Type"))
	}
}

func TestPhotoOperationsRefuseAnotherUsersPhoto(t *testing.T) {
	environment := testEnvironment(t, 123, 999)
	authenticateDevUser(t, environment.router)
	if response := uploadPhoto(t, environment.router, "/api/me/photos"); response.Code != http.StatusOK {
		t.Fatalf("upload status = %d", response.Code)
	}

	stranger, err := environment.store.UpsertTelegramUser(context.Background(), strangerProfile(456))
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := environment.store.AddPhoto(context.Background(), stranger.ID, database.Photo{
		FilePath:  "users/" + stranger.ID + "/photos/a.jpg",
		PublicURL: "/profile_photo/users/" + stranger.ID + "/photos/a.jpg",
		MIMEType:  "image/jpeg",
	}, 3)
	if err != nil {
		t.Fatal(err)
	}

	if response := authorized(t, environment.router, http.MethodDelete, "/api/me/photos/"+foreign[0].ID, ""); response.Code != http.StatusNotFound {
		t.Fatalf("cross-account delete status = %d, want 404", response.Code)
	}
	if response := authorized(t, environment.router, http.MethodPatch, "/api/me/photos/"+foreign[0].ID+"/primary", ""); response.Code != http.StatusNotFound {
		t.Fatalf("cross-account promote status = %d, want 404", response.Code)
	}
	body := `{"photo_ids":["` + foreign[0].ID + `"]}`
	if response := authorized(t, environment.router, http.MethodPatch, "/api/me/photos/order", body); response.Code != http.StatusConflict {
		t.Fatalf("cross-account reorder status = %d, want 409", response.Code)
	}

	remaining, err := environment.store.ListPhotos(context.Background(), stranger.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 {
		t.Fatalf("the stranger's gallery changed: %+v", remaining)
	}
}

func TestPhotoPathsRejectTraversal(t *testing.T) {
	environment := testEnvironment(t, 123, 999)
	authenticateDevUser(t, environment.router)
	paths := []string{
		"/profile_photo/../../etc/passwd",
		"/profile_photo/users/../../secret.jpg",
		"/profile_photo/..%2f..%2fsecret.jpg",
		"/profile_photo/users/123/photos/../../../escape.jpg",
	}
	for _, path := range paths {
		response := authorized(t, environment.router, http.MethodGet, path, "")
		if response.Code == http.StatusOK {
			t.Fatalf("%s was served with status 200", path)
		}
	}
	if response := authorized(t, environment.router, http.MethodDelete, "/api/me/photos/..%2f..%2fetc", ""); response.Code != http.StatusBadRequest && response.Code != http.StatusNotFound {
		t.Fatalf("traversal photo id status = %d", response.Code)
	}
}

func TestReorderAndPrimaryChangeTheAvatar(t *testing.T) {
	environment := testEnvironment(t, 123, 999)
	authenticateDevUser(t, environment.router)
	for range 2 {
		if response := uploadPhoto(t, environment.router, "/api/me/photos"); response.Code != http.StatusOK {
			t.Fatalf("upload status = %d", response.Code)
		}
	}
	listed := authorized(t, environment.router, http.MethodGet, "/api/me/photos", "")
	var gallery galleryResponse
	if err := json.NewDecoder(listed.Body).Decode(&gallery); err != nil {
		t.Fatal(err)
	}
	if len(gallery.Photos) != 2 {
		t.Fatalf("gallery size = %d", len(gallery.Photos))
	}

	promoted := authorized(t, environment.router, http.MethodPatch, "/api/me/photos/"+gallery.Photos[1].ID+"/primary", "")
	if promoted.Code != http.StatusOK {
		t.Fatalf("promote status = %d, body = %s", promoted.Code, promoted.Body.String())
	}
	var afterPromote galleryResponse
	if err := json.NewDecoder(promoted.Body).Decode(&afterPromote); err != nil {
		t.Fatal(err)
	}
	if afterPromote.Photos[0].ID != gallery.Photos[1].ID || afterPromote.Me == nil {
		t.Fatalf("promote response = %+v", afterPromote)
	}
	if afterPromote.Me.PhotoURL != afterPromote.Photos[0].URL {
		t.Fatalf("avatar = %q, want the promoted photo %q", afterPromote.Me.PhotoURL, afterPromote.Photos[0].URL)
	}

	order := `{"photo_ids":["` + gallery.Photos[0].ID + `","` + gallery.Photos[1].ID + `"]}`
	reordered := authorized(t, environment.router, http.MethodPatch, "/api/me/photos/order", order)
	if reordered.Code != http.StatusOK {
		t.Fatalf("reorder status = %d, body = %s", reordered.Code, reordered.Body.String())
	}
	var afterReorder galleryResponse
	if err := json.NewDecoder(reordered.Body).Decode(&afterReorder); err != nil {
		t.Fatal(err)
	}
	if afterReorder.Photos[0].ID != gallery.Photos[0].ID || !afterReorder.Photos[0].IsPrimary {
		t.Fatalf("reorder response = %+v", afterReorder.Photos)
	}
	if response := authorized(t, environment.router, http.MethodPatch, "/api/me/photos/order", `{"photo_ids":[]}`); response.Code != http.StatusBadRequest {
		t.Fatalf("empty order status = %d, want 400", response.Code)
	}
}

func TestDeletePhotoRemovesTheStoredFile(t *testing.T) {
	environment := testEnvironment(t, 123, 999)
	authenticateDevUser(t, environment.router)
	for range 2 {
		if response := uploadPhoto(t, environment.router, "/api/me/photos"); response.Code != http.StatusOK {
			t.Fatalf("upload status = %d", response.Code)
		}
	}
	listed := authorized(t, environment.router, http.MethodGet, "/api/me/photos", "")
	var gallery galleryResponse
	if err := json.NewDecoder(listed.Body).Decode(&gallery); err != nil {
		t.Fatal(err)
	}
	removedURL := gallery.Photos[0].URL

	deleted := authorized(t, environment.router, http.MethodDelete, "/api/me/photos/"+gallery.Photos[0].ID, "")
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body = %s", deleted.Code, deleted.Body.String())
	}
	if response := authorized(t, environment.router, http.MethodGet, removedURL, ""); response.Code != http.StatusNotFound {
		t.Fatalf("deleted photo is still served with status %d", response.Code)
	}
	// The profile has no Telegram avatar here, so the final photo must not be removable.
	var remaining galleryResponse
	if err := json.NewDecoder(deleted.Body).Decode(&remaining); err != nil {
		t.Fatal(err)
	}
	last := authorized(t, environment.router, http.MethodDelete, "/api/me/photos/"+remaining.Photos[0].ID, "")
	if last.Code != http.StatusConflict {
		t.Fatalf("last photo delete status = %d, want 409", last.Code)
	}
}

func strangerProfile(telegramID int64) domain.TelegramProfile {
	return domain.TelegramProfile{UserID: telegramID, FirstName: "Stranger", LanguageCode: "en", PhotoURLKnown: true}
}
