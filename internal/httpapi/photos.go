package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"

	"aika/internal/database"
	"aika/internal/domain"
	"aika/internal/profilephoto"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

type photoResponse struct {
	ID        string `json:"id"`
	URL       string `json:"url"`
	ThumbURL  string `json:"thumb_url,omitempty"`
	SortOrder int    `json:"sort_order"`
	IsPrimary bool   `json:"is_primary"`
	Width     int    `json:"width,omitempty"`
	Height    int    `json:"height,omitempty"`
}

type galleryResponse struct {
	Photos    []photoResponse `json:"photos"`
	MaxPhotos int             `json:"max_photos"`
	Me        *meResponse     `json:"me,omitempty"`
}

func photoDTOs(photos []database.Photo) []photoResponse {
	result := make([]photoResponse, 0, len(photos))
	for _, photo := range photos {
		result = append(result, photoResponse{
			ID: photo.ID, URL: photo.PublicURL, ThumbURL: photo.Thumbnail(),
			SortOrder: photo.SortOrder, IsPrimary: photo.IsPrimary,
			Width: photo.Width, Height: photo.Height,
		})
	}
	return result
}

// photosOf never fails a request: a gallery that cannot be read degrades to the single avatar the
// rest of the response already carries.
func (s *Server) photosOf(ctx context.Context, userID string) []database.Photo {
	photos, err := s.store.ListPhotos(ctx, userID)
	if err != nil {
		s.logger.Warn("could not load photos", zap.String("user_id", userID), zap.Error(err))
		return nil
	}
	return photos
}

func (s *Server) listMyPhotos(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	photos, err := s.store.ListPhotos(r.Context(), user.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, galleryResponse{Photos: photoDTOs(photos), MaxPhotos: s.cfg.MaxProfilePhotos})
}

// readUploadedPhoto streams the single `photo` part of a multipart body. It is shared by the legacy
// avatar endpoint and the gallery endpoint so both enforce the same size limit and part shape.
func (s *Server) readUploadedPhoto(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	language := currentUser(r).AppLanguage
	r.Body = http.MaxBytesReader(w, r.Body, profilephoto.MaxUploadBytes+(1<<20))
	reader, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_photo", localized(language, "invalid_photo"))
		return nil, false
	}
	var uploaded []byte
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_photo", localized(language, "invalid_photo"))
			return nil, false
		}
		if part.FormName() != "photo" || uploaded != nil {
			part.Close()
			writeError(w, http.StatusBadRequest, "invalid_photo", localized(language, "invalid_photo"))
			return nil, false
		}
		uploaded, err = io.ReadAll(io.LimitReader(part, profilephoto.MaxUploadBytes+1))
		part.Close()
		if err != nil {
			s.internalError(w, r, err)
			return nil, false
		}
	}
	if len(uploaded) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_photo", localized(language, "invalid_photo"))
		return nil, false
	}
	if len(uploaded) > profilephoto.MaxUploadBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "photo_too_large", localized(language, "photo_too_large"))
		return nil, false
	}
	return uploaded, true
}

// storeUpload validates and writes the bytes. The user ID always comes from the authenticated
// context, so no request value ever reaches the filesystem path.
func (s *Server) storeUpload(w http.ResponseWriter, r *http.Request, user domain.User, uploaded []byte) (database.Photo, bool) {
	stored, err := s.photos.SaveUserPhoto(user.ID, uploaded)
	if errors.Is(err, profilephoto.ErrInvalidImage) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_photo", localized(user.AppLanguage, "invalid_photo"))
		return database.Photo{}, false
	}
	if err != nil {
		s.internalError(w, r, err)
		return database.Photo{}, false
	}
	return database.Photo{
		FilePath: stored.RelativePath, PublicURL: stored.PublicURL, ThumbnailURL: stored.ThumbnailURL,
		Width: stored.Width, Height: stored.Height, MIMEType: stored.MIMEType,
	}, true
}

// discard removes a file that was written but never referenced by a committed row, so a failed
// insert cannot leak an orphan.
func (s *Server) discard(relativePath string) {
	if relativePath == "" {
		return
	}
	if err := s.photos.Remove(relativePath); err != nil {
		s.logger.Warn("could not remove photo file", zap.Error(err))
	}
}

func (s *Server) addPhoto(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	uploaded, ok := s.readUploadedPhoto(w, r)
	if !ok {
		return
	}
	photo, ok := s.storeUpload(w, r, user, uploaded)
	if !ok {
		return
	}
	photos, err := s.store.AddPhoto(r.Context(), user.ID, photo, s.cfg.MaxProfilePhotos)
	if errors.Is(err, database.ErrPhotoLimit) {
		s.discard(photo.FilePath)
		writeError(w, http.StatusConflict, "photo_limit_reached", localized(user.AppLanguage, "photo_limit_reached"))
		return
	}
	if err != nil {
		s.discard(photo.FilePath)
		s.internalError(w, r, err)
		return
	}
	s.writeGallery(w, r, user.ID, photos)
}

// uploadProfilePhoto is the original single-avatar endpoint. It now replaces the primary gallery
// photo, so old clients keep their exact behaviour while the gallery stays the source of truth.
func (s *Server) uploadProfilePhoto(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	uploaded, ok := s.readUploadedPhoto(w, r)
	if !ok {
		return
	}
	photo, ok := s.storeUpload(w, r, user, uploaded)
	if !ok {
		return
	}
	removed, photos, err := s.store.ReplacePrimaryPhoto(r.Context(), user.ID, photo)
	if err != nil {
		s.discard(photo.FilePath)
		s.internalError(w, r, err)
		return
	}
	s.discard(removed)
	updated, err := s.store.GetUserByID(r.Context(), user.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	response := s.meDTO(updated)
	response.Photos = photoDTOs(photos)
	response.MaxPhotos = s.cfg.MaxProfilePhotos
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) deletePhoto(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	photoID, valid := uuidParam(r, "photoID")
	if !valid {
		writeError(w, http.StatusBadRequest, "invalid_request", localized(user.AppLanguage, "invalid_request"))
		return
	}
	// A Telegram avatar keeps the profile valid, so the last gallery photo may only be removed when
	// one exists. Otherwise the profile would fail its own completeness rule on the next save.
	avatarOptional := strings.TrimSpace(user.TelegramPhotoURL.String) != ""
	removed, photos, err := s.store.DeletePhoto(r.Context(), user.ID, photoID, avatarOptional)
	if errors.Is(err, database.ErrNotFound) {
		writeError(w, http.StatusNotFound, "photo_not_found", localized(user.AppLanguage, "photo_not_found"))
		return
	}
	if errors.Is(err, database.ErrPhotoRequired) {
		writeError(w, http.StatusConflict, "photo_required", localized(user.AppLanguage, "photo_required"))
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.discard(removed)
	s.writeGallery(w, r, user.ID, photos)
}

type photoOrderRequest struct {
	PhotoIDs []string `json:"photo_ids"`
}

func (s *Server) reorderPhotos(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	var request photoOrderRequest
	if err := decodeJSON(w, r, &request, 8<<10); err != nil || len(request.PhotoIDs) == 0 || len(request.PhotoIDs) > s.cfg.MaxProfilePhotos {
		writeError(w, http.StatusBadRequest, "invalid_request", localized(user.AppLanguage, "invalid_request"))
		return
	}
	for _, id := range request.PhotoIDs {
		if !uuidPattern.MatchString(strings.ToLower(id)) {
			writeError(w, http.StatusBadRequest, "invalid_request", localized(user.AppLanguage, "invalid_request"))
			return
		}
	}
	photos, err := s.store.ReorderPhotos(r.Context(), user.ID, request.PhotoIDs)
	if errors.Is(err, database.ErrPhotoOrderMismatch) {
		writeError(w, http.StatusConflict, "photo_order_mismatch", localized(user.AppLanguage, "photo_order_mismatch"))
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.writeGallery(w, r, user.ID, photos)
}

func (s *Server) setPrimaryPhoto(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	photoID, valid := uuidParam(r, "photoID")
	if !valid {
		writeError(w, http.StatusBadRequest, "invalid_request", localized(user.AppLanguage, "invalid_request"))
		return
	}
	photos, err := s.store.SetPrimaryPhoto(r.Context(), user.ID, photoID)
	if errors.Is(err, database.ErrNotFound) {
		writeError(w, http.StatusNotFound, "photo_not_found", localized(user.AppLanguage, "photo_not_found"))
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.writeGallery(w, r, user.ID, photos)
}

// writeGallery returns the new gallery together with the refreshed profile, because promoting or
// deleting a photo also changes the avatar the rest of the app renders.
func (s *Server) writeGallery(w http.ResponseWriter, r *http.Request, userID string, photos []database.Photo) {
	updated, err := s.store.GetUserByID(r.Context(), userID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	me := s.meDTO(updated)
	me.Photos = photoDTOs(photos)
	me.MaxPhotos = s.cfg.MaxProfilePhotos
	writeJSON(w, http.StatusOK, galleryResponse{Photos: me.Photos, MaxPhotos: s.cfg.MaxProfilePhotos, Me: &me})
}

// userPhotos exposes another user's gallery, subject to the same visibility rules as their profile.
func (s *Server) userPhotos(w http.ResponseWriter, r *http.Request) {
	viewer := currentUser(r)
	id, valid := pathID(r)
	if !valid {
		writeError(w, http.StatusBadRequest, "invalid_user_id", localized(viewer.AppLanguage, "invalid_request"))
		return
	}
	if _, err := s.store.GetPublicUserByID(r.Context(), id); errors.Is(err, database.ErrNotFound) {
		writeError(w, http.StatusNotFound, "user_not_found", localized(viewer.AppLanguage, "recipient_unavailable"))
		return
	} else if err != nil {
		s.internalError(w, r, err)
		return
	}
	hidden, err := s.blocked(r, viewer.ID, id)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if hidden {
		writeError(w, http.StatusNotFound, "user_not_found", localized(viewer.AppLanguage, "recipient_unavailable"))
		return
	}
	photos, err := s.store.ListPhotos(r.Context(), id)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, galleryResponse{Photos: photoDTOs(photos), MaxPhotos: s.cfg.MaxProfilePhotos})
}

func (s *Server) serveProfilePhoto(w http.ResponseWriter, r *http.Request) {
	requested := chi.URLParam(r, "*")
	path, valid := s.photos.FilePath(requested)
	if !valid {
		http.NotFound(w, r)
		return
	}
	if _, err := os.Stat(path); err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	// Gallery files carry a generated name and are never rewritten, so they can be cached hard.
	// The legacy flat avatar keeps one URL per user and is replaced in place, so it must not be.
	if strings.HasPrefix(requested, "users/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	w.Header().Set("Content-Disposition", "inline")
	http.ServeFile(w, r, path)
}

func uuidParam(r *http.Request, name string) (string, bool) {
	value := strings.ToLower(chi.URLParam(r, name))
	return value, uuidPattern.MatchString(value)
}
