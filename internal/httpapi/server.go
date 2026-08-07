package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"aika/internal/auth"
	"aika/internal/calls"
	"aika/internal/config"
	"aika/internal/database"
	"aika/internal/domain"
	"aika/internal/profilephoto"
	"aika/internal/users"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

type likeSender interface {
	SendLike(ctx context.Context, recipient, sender domain.User, message string) error
}

// callRinger delivers an invitation through the bot to someone who does not have the Mini App
// open. It is a separate interface because it is optional: a deployment without it still works,
// it simply cannot reach an absent callee.
type callRinger interface {
	SendCallInvite(ctx context.Context, recipient, caller domain.User, callID string) (int64, error)
	DeleteMessage(ctx context.Context, chatID, messageID int64) error
}

type Server struct {
	cfg      config.Config
	store    *database.Store
	users    *users.Service
	auth     *auth.Validator
	telegram likeSender
	limiter  *users.LikeLimiter
	photos   *profilephoto.Store
	// calls holds the in-memory signalling state for one-to-one video calls. Call media is
	// peer-to-peer and never reaches this process.
	calls       *calls.Registry
	callLimiter *users.LikeLimiter
	// telegramCalls is nil when the bot client cannot ring an absent callee.
	telegramCalls callRinger
	// contentSecurityPolicy is assembled once, because connect-src has to name the configured ICE
	// servers and those are only known after the configuration is loaded.
	contentSecurityPolicy string
	logger                *zap.Logger
}

func NewServer(cfg config.Config, store *database.Store, userService *users.Service, validator *auth.Validator, telegramClient likeSender, photos *profilephoto.Store, callRegistry *calls.Registry, logger *zap.Logger) *Server {
	server := &Server{
		cfg: cfg, store: store, users: userService, auth: validator,
		telegram: telegramClient, limiter: users.NewLikeLimiter(cfg.LikeRatePerMinute), photos: photos,
		calls: callRegistry, callLimiter: users.NewLikeLimiter(callInvitesPerMinute),
		contentSecurityPolicy: contentSecurityPolicy(cfg), logger: logger,
	}
	// The real bot client can also ring an absent callee; a test double that only sends likes
	// leaves that capability off rather than requiring every caller to supply one.
	if ringer, ok := telegramClient.(callRinger); ok {
		server.telegramCalls = ringer
	}
	return server
}

// endCallBetween hangs up a live call between two people, used when one of them blocks the other.
func (s *Server) endCallBetween(first, second string) {
	if s.calls != nil {
		s.calls.EndBetween(first, second)
	}
}

func (s *Server) Router() http.Handler {
	router := chi.NewRouter()
	router.Use(s.recoverer, s.requestLogger, s.securityHeaders, s.httpsOnly, s.cors)
	router.Get("/health", s.health)
	router.Get("/profile_photo/*", s.serveProfilePhoto)
	router.Route("/api", func(api chi.Router) {
		api.Post("/auth/telegram", s.telegramAuth)
		api.Group(func(protected chi.Router) {
			protected.Use(s.requireUser)
			protected.Get("/me", s.getMe)
			protected.Patch("/me", s.patchMe)
			protected.Post("/me/location", s.updateLocation)
			protected.Post("/me/photo", s.uploadProfilePhoto)
			protected.Get("/me/blocks", s.listBlockedUsers)
			protected.Get("/me/photos", s.listMyPhotos)
			protected.Post("/me/photos", s.addPhoto)
			protected.Patch("/me/photos/order", s.reorderPhotos)
			protected.Patch("/me/photos/{photoID}/primary", s.setPrimaryPhoto)
			protected.Delete("/me/photos/{photoID}", s.deletePhoto)
			protected.Get("/users/nearby", s.nearbyUsers)
			protected.Get("/users/{id}", s.publicProfile)
			protected.Get("/users/{id}/photos", s.userPhotos)
			protected.Get("/users/{id}/cooldowns", s.userCooldowns)
			protected.Post("/users/{id}/like", s.likeUser)
			protected.Post("/users/{id}/message", s.messageUser)
			protected.Post("/users/{id}/block", s.blockUser)
			protected.Delete("/users/{id}/block", s.unblockUser)
			// One-to-one video calls. These routes carry signalling only; the audio and video
			// travel directly between the two browsers.
			protected.Route("/calls", func(call chi.Router) {
				call.Get("/config", s.callConfig)
				call.Get("/events", s.callEvents)
				call.Post("/", s.createCall)
				call.Post("/{callID}/open", s.openCall)
				call.Post("/{callID}/accept", s.acceptCall)
				call.Post("/{callID}/reject", s.rejectCall)
				call.Post("/{callID}/end", s.endCall)
				call.Post("/{callID}/state", s.updateCallState)
				call.Post("/{callID}/signal", s.signalCall)
			})
			protected.Route("/admin", func(admin chi.Router) {
				admin.Use(s.requireAdmin)
				admin.Get("/stats", s.adminStats)
				admin.Get("/users", s.adminUsers)
			})
		})
	})
	router.NotFound(s.serveFrontend)
	return router
}

type userContextKey struct{}

func currentUser(r *http.Request) domain.User {
	return r.Context().Value(userContextKey{}).(domain.User)
}

func (s *Server) requireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		profile, err := s.auth.FromRequest(r)
		if err != nil {
			s.writeAuthError(w, err)
			return
		}
		user, err := s.store.GetUserByTelegramID(r.Context(), profile.UserID)
		if errors.Is(err, database.ErrNotFound) {
			writeError(w, http.StatusUnauthorized, "authentication_required", "Authenticate with /api/auth/telegram first")
			return
		}
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		if user.IsBlocked {
			writeError(w, http.StatusForbidden, "account_blocked", localized(user.AppLanguage, "account_blocked"))
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userContextKey{}, user)))
	})
}

func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.cfg.IsAdmin(currentUser(r).TelegramUserID) {
			writeError(w, http.StatusForbidden, "forbidden", localized(currentUser(r).AppLanguage, "forbidden"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) telegramAuth(w http.ResponseWriter, r *http.Request) {
	profile, err := s.auth.FromRequest(r)
	if err != nil {
		s.writeAuthError(w, err)
		return
	}
	user, err := s.store.UpsertTelegramUser(r.Context(), profile)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.meWithPhotos(r, user))
}

func (s *Server) getMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.meWithPhotos(r, currentUser(r)))
}

type meResponse struct {
	ID                 string  `json:"id"`
	TelegramUserID     int64   `json:"telegram_user_id"`
	Username           string  `json:"username,omitempty"`
	FirstName          string  `json:"first_name,omitempty"`
	LastName           string  `json:"last_name,omitempty"`
	DisplayName        string  `json:"display_name,omitempty"`
	PhotoURL           string  `json:"photo_url,omitempty"`
	TelegramPhotoURL   string  `json:"telegram_photo_url,omitempty"`
	CustomPhotoURL     string  `json:"custom_photo_url,omitempty"`
	AppLanguage        string  `json:"app_language"`
	Gender             string  `json:"gender,omitempty"`
	BirthDate          string  `json:"birth_date,omitempty"`
	Purpose            string  `json:"purpose,omitempty"`
	Bio                string  `json:"bio,omitempty"`
	IsProfileCompleted bool    `json:"is_profile_completed"`
	IsActive           bool    `json:"is_active"`
	IsAdmin            bool    `json:"is_admin"`
	LocationAvailable  bool    `json:"location_available"`
	LocationUpdatedAt  *string `json:"location_updated_at,omitempty"`
	// Gallery. photo_url stays the single avatar every older client reads, so both work together.
	Photos    []photoResponse `json:"photos"`
	MaxPhotos int             `json:"max_photos"`
}

// meWithPhotos is the profile response used by every endpoint that returns the current user, so a
// client always receives the gallery alongside the profile it just read or changed.
func (s *Server) meWithPhotos(r *http.Request, user domain.User) meResponse {
	response := s.meDTO(user)
	response.Photos = photoDTOs(s.photosOf(r.Context(), user.ID))
	response.MaxPhotos = s.cfg.MaxProfilePhotos
	return response
}

func (s *Server) meDTO(user domain.User) meResponse {
	response := meResponse{
		ID: user.ID, TelegramUserID: user.TelegramUserID, Username: user.Username.String,
		FirstName: user.FirstName.String, LastName: user.LastName.String, DisplayName: user.DisplayName.String,
		PhotoURL: user.EffectivePhotoURL(), TelegramPhotoURL: user.TelegramPhotoURL.String,
		CustomPhotoURL: user.CustomPhotoURL.String, AppLanguage: user.AppLanguage, Gender: user.Gender.String,
		Purpose: user.Purpose.String, Bio: user.Bio.String, IsProfileCompleted: user.IsProfileCompleted,
		IsActive: user.IsActive, IsAdmin: s.cfg.IsAdmin(user.TelegramUserID),
		LocationAvailable: user.Latitude.Valid && user.Longitude.Valid,
	}
	if user.BirthDate.Valid {
		response.BirthDate = user.BirthDate.Time.Format("2006-01-02")
	}
	if user.LocationUpdatedAt.Valid {
		formatted := user.LocationUpdatedAt.Time.UTC().Format(time.RFC3339)
		response.LocationUpdatedAt = &formatted
	}
	return response
}

type profileRequest struct {
	DisplayName    string `json:"display_name"`
	Gender         string `json:"gender"`
	BirthDate      string `json:"birth_date"`
	Purpose        string `json:"purpose"`
	Bio            string `json:"bio"`
	CustomPhotoURL string `json:"custom_photo_url"`
	AppLanguage    string `json:"app_language"`
	IsActive       *bool  `json:"is_active"`
}

func (s *Server) patchMe(w http.ResponseWriter, r *http.Request) {
	var request profileRequest
	if err := decodeJSON(w, r, &request, 16<<10); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", localized(currentUser(r).AppLanguage, "invalid_request"))
		return
	}
	updated, err := s.users.UpdateProfile(r.Context(), currentUser(r), users.ProfileInput{
		DisplayName: request.DisplayName, Gender: request.Gender, BirthDate: request.BirthDate,
		Purpose: request.Purpose, Bio: request.Bio, CustomPhotoURL: request.CustomPhotoURL,
		AppLanguage: request.AppLanguage, IsActive: request.IsActive,
	})
	var validation users.ValidationError
	if errors.As(err, &validation) {
		writeError(w, http.StatusUnprocessableEntity, validation.Code, localized(currentUser(r).AppLanguage, validation.Code))
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.meWithPhotos(r, updated))
}

type locationRequest struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

func (s *Server) updateLocation(w http.ResponseWriter, r *http.Request) {
	var request locationRequest
	if err := decodeJSON(w, r, &request, 2<<10); err != nil ||
		math.IsNaN(request.Latitude) || math.IsInf(request.Latitude, 0) || request.Latitude < -90 || request.Latitude > 90 ||
		math.IsNaN(request.Longitude) || math.IsInf(request.Longitude, 0) || request.Longitude < -180 || request.Longitude > 180 {
		writeError(w, http.StatusBadRequest, "invalid_location", localized(currentUser(r).AppLanguage, "invalid_location"))
		return
	}
	if err := s.store.UpdateLocation(r.Context(), currentUser(r).ID, request.Latitude, request.Longitude); err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (s *Server) nearbyUsers(w http.ResponseWriter, r *http.Request) {
	radius, err := strconv.ParseFloat(queryDefault(r, "radius_km", "20"), 64)
	if err != nil || (radius != 5 && radius != 10 && radius != 20 && radius != 500) {
		writeError(w, http.StatusBadRequest, "invalid_radius", localized(currentUser(r).AppLanguage, "invalid_radius"))
		return
	}
	page, err := positiveInt(queryDefault(r, "page", "1"), 1, 100000)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_page", localized(currentUser(r).AppLanguage, "invalid_request"))
		return
	}
	limit, err := positiveInt(queryDefault(r, "limit", "20"), 1, 50)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_limit", localized(currentUser(r).AppLanguage, "invalid_request"))
		return
	}
	gender := strings.TrimSpace(r.URL.Query().Get("gender"))
	if gender != "" && gender != "male" && gender != "female" && gender != "other" {
		writeError(w, http.StatusBadRequest, "invalid_gender", localized(currentUser(r).AppLanguage, "invalid_gender"))
		return
	}
	result, err := s.users.Nearby(r.Context(), currentUser(r), radius, gender, page, limit)
	if errors.Is(err, users.ErrLocationRequired) {
		writeError(w, http.StatusConflict, "location_required", localized(currentUser(r).AppLanguage, "location_required"))
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	// The list is polled every couple of seconds. Its entity tag covers the profiles, their photos
	// and the caller's cooldown deadlines — everything except the server clock — so an unchanged
	// neighbourhood answers with an empty 304 instead of the full payload and its image URLs.
	tag, err := entityTag(result.Users, result.Page, result.HasMore)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	w.Header().Set("ETag", tag)
	w.Header().Set("Cache-Control", "private, no-cache")
	if matchesEntityTag(r.Header.Get("If-None-Match"), tag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func entityTag(values ...any) (string, error) {
	encoded, err := json.Marshal(values)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return `"` + hex.EncodeToString(digest[:16]) + `"`, nil
}

func matchesEntityTag(header, tag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		if strings.TrimSpace(candidate) == tag {
			return true
		}
	}
	return false
}

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func pathID(r *http.Request) (string, bool) {
	id := strings.ToLower(chi.URLParam(r, "id"))
	return id, uuidPattern.MatchString(id)
}

func (s *Server) publicProfile(w http.ResponseWriter, r *http.Request) {
	id, valid := pathID(r)
	if !valid {
		writeError(w, http.StatusBadRequest, "invalid_user_id", localized(currentUser(r).AppLanguage, "invalid_request"))
		return
	}
	user, err := s.store.GetPublicUserByID(r.Context(), id)
	if errors.Is(err, database.ErrNotFound) {
		writeError(w, http.StatusNotFound, "user_not_found", localized(currentUser(r).AppLanguage, "recipient_unavailable"))
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	// A blocked profile is reported exactly like a missing one, in both directions, so neither
	// person can tell a block apart from a deleted account.
	hidden, err := s.blocked(r, currentUser(r).ID, id)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if hidden {
		writeError(w, http.StatusNotFound, "user_not_found", localized(currentUser(r).AppLanguage, "recipient_unavailable"))
		return
	}
	profile, err := s.users.PublicProfileWithPhotos(r.Context(), currentUser(r).ID, user)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, profile)
}

func (s *Server) adminStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.AdminStats(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

type adminUserResponse struct {
	TelegramUserID    int64  `json:"telegram_user_id"`
	Username          string `json:"username,omitempty"`
	DisplayName       string `json:"display_name"`
	Gender            string `json:"gender,omitempty"`
	Age               int    `json:"age,omitempty"`
	Purpose           string `json:"purpose,omitempty"`
	RegisteredAt      string `json:"registered_at"`
	LastSeenAt        string `json:"last_seen_at"`
	LocationAvailable bool   `json:"location_available"`
	ProfileCompleted  bool   `json:"profile_completed"`
	IsActive          bool   `json:"is_active"`
	IsBlocked         bool   `json:"is_blocked"`
}

func (s *Server) adminUsers(w http.ResponseWriter, r *http.Request) {
	page, err := positiveInt(queryDefault(r, "page", "1"), 1, 100000)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_page", "Invalid page")
		return
	}
	limit, err := positiveInt(queryDefault(r, "limit", "30"), 1, 100)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_limit", "Invalid limit")
		return
	}
	search := strings.TrimSpace(r.URL.Query().Get("search"))
	if len(search) > 100 {
		writeError(w, http.StatusBadRequest, "invalid_search", "Invalid search")
		return
	}
	items, err := s.store.AdminUsers(r.Context(), search, limit+1, (page-1)*limit)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	response := make([]adminUserResponse, 0, len(items))
	for _, item := range items {
		response = append(response, adminUserResponse{
			TelegramUserID: item.TelegramUserID, Username: item.Username.String, DisplayName: item.Name(),
			Gender: item.Gender.String, Age: item.Age(time.Now()), Purpose: item.Purpose.String,
			RegisteredAt: item.CreatedAt.UTC().Format(time.RFC3339), LastSeenAt: item.LastSeenAt.UTC().Format(time.RFC3339),
			LocationAvailable: item.Latitude.Valid && item.Longitude.Valid, ProfileCompleted: item.IsProfileCompleted,
			IsActive: item.IsActive, IsBlocked: item.IsBlocked,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": response, "page": page, "has_more": hasMore})
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.store.Ping(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unhealthy"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) writeAuthError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrUnavailable):
		writeError(w, http.StatusUnauthorized, "telegram_unavailable", "Telegram initialization unavailable")
	case errors.Is(err, auth.ErrExpired):
		writeError(w, http.StatusUnauthorized, "telegram_auth_expired", "Telegram authorization expired")
	default:
		writeError(w, http.StatusUnauthorized, "telegram_auth_invalid", "Invalid Telegram authorization")
	}
}

func (s *Server) internalError(w http.ResponseWriter, r *http.Request, err error) {
	s.logger.Error("request failed", zap.String("method", r.Method), zap.String("path", r.URL.Path), zap.Error(err))
	writeError(w, http.StatusInternalServerError, "server_error", "Server error")
}

func decodeJSON(w http.ResponseWriter, r *http.Request, destination any, maxBytes int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON value")
	}
	return nil
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	// Present only on a cooldown refusal, where the client needs the deadline to run a countdown.
	NextAllowedAt     string `json:"next_allowed_at,omitempty"`
	RetryAfterSeconds int64  `json:"retry_after_seconds,omitempty"`
}

type errorEnvelope struct {
	Error errorDetail `json:"error"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorEnvelope{Error: errorDetail{Code: code, Message: message}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func positiveInt(raw string, minValue, maxValue int) (int, error) {
	value, err := strconv.Atoi(raw)
	if err != nil || value < minValue || value > maxValue {
		return 0, errors.New("invalid integer")
	}
	return value, nil
}

func queryDefault(r *http.Request, name, fallback string) string {
	if value := r.URL.Query().Get(name); value != "" {
		return value
	}
	return fallback
}

func (s *Server) serveFrontend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.NotFound(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeError(w, http.StatusNotFound, "not_found", "Not found")
		return
	}
	cleanPath := filepath.Clean(strings.TrimPrefix(r.URL.Path, "/"))
	if cleanPath == ".." || strings.HasPrefix(cleanPath, ".."+string(filepath.Separator)) {
		http.NotFound(w, r)
		return
	}
	if cleanPath == "." {
		cleanPath = "index.html"
	}
	requested := filepath.Join(s.cfg.WebDir, cleanPath)
	if info, err := os.Stat(requested); err == nil && !info.IsDir() {
		http.ServeFile(w, r, requested)
		return
	}
	index := filepath.Join(s.cfg.WebDir, "index.html")
	if _, err := os.Stat(index); err != nil {
		http.Error(w, "AikaBot frontend is not built", http.StatusServiceUnavailable)
		return
	}
	http.ServeFile(w, r, index)
}

func localized(language, code string) string {
	translations := map[string]map[string]string{
		"ru": {
			"invalid_request": "Проверьте введённые данные.", "invalid_location": "Некорректная геолокация.",
			"invalid_radius": "Выберите доступный радиус.", "invalid_gender": "Некорректное значение пола.",
			"location_required": "Сначала разрешите доступ к геолокации.", "self_like": "Нельзя лайкнуть свою анкету.",
			"message_required": "Введите сообщение.", "message_too_long": "Сообщение не должно превышать 300 символов.",
			"rate_limit_exceeded": "Слишком много лайков. Попробуйте через минуту.", "recipient_unavailable": "Пользователь сейчас недоступен.",
			"profile_required": "Сначала заполните и включите анкету.", "like_sent": "Лайк отправлен.",
			"forbidden": "Доступ запрещён.", "account_blocked": "Аккаунт заблокирован.",
			"invalid_display_name": "Введите имя от 2 до 80 символов.", "invalid_birth_date": "Укажите корректную дату рождения.",
			"invalid_age": "AikaBot доступен пользователям от 18 до 100 лет.", "invalid_purpose": "Укажите цель знакомства.",
			"bio_too_long": "Описание не должно превышать 500 символов.", "invalid_language": "Выберите доступный язык.",
			"invalid_photo_url": "Укажите корректную HTTPS-ссылку на фото.", "photo_required": "Добавьте фото профиля.",
			"invalid_photo": "Выберите корректное фото JPEG или PNG.", "photo_too_large": "Фото не должно превышать 8 МБ.",
			"like_cooldown_active":    "Вы уже лайкнули этого пользователя. Повторный лайк будет доступен позже.",
			"message_cooldown_active": "Вы уже отправили сообщение этому пользователю. Следующее можно отправить позже.",
			"message_sent":            "Сообщение отправлено.",
			"photo_limit_reached":     "Достигнут максимум фотографий.",
			"photo_not_found":         "Фото не найдено.",
			"photo_order_mismatch":    "Список фотографий устарел. Обновите страницу.",
			"calls_disabled":          "Видеозвонки сейчас недоступны.",
			"call_not_found":          "Звонок не найден или уже завершён.",
			"call_busy":               "Вы уже участвуете в звонке.",
			"peer_busy":               "Пользователь сейчас разговаривает.",
			"incoming_call_pending":   "Этот пользователь уже звонит вам.",
			"call_invalid_state":      "Действие недоступно для этого звонка.",
			"self_call":               "Нельзя позвонить самому себе.",
			"self_block":              "Нельзя заблокировать самого себя.",
			"user_blocked":            "Пользователь заблокирован.",
			"user_unblocked":          "Пользователь разблокирован.",
		},
		"kk": {
			"invalid_request": "Енгізілген деректерді тексеріңіз.", "invalid_location": "Геолокация дұрыс емес.",
			"invalid_radius": "Қолжетімді радиусты таңдаңыз.", "invalid_gender": "Жыныс мәні дұрыс емес.",
			"location_required": "Алдымен геолокацияға рұқсат беріңіз.", "self_like": "Өз профиліңізге лайк қоя алмайсыз.",
			"message_required": "Хабарлама енгізіңіз.", "message_too_long": "Хабарлама 300 таңбадан аспауы керек.",
			"rate_limit_exceeded": "Лайк тым көп. Бір минуттан кейін қайталаңыз.", "recipient_unavailable": "Қолданушы қазір қолжетімсіз.",
			"profile_required": "Алдымен профильді толтырып, қосыңыз.", "like_sent": "Лайк жіберілді.",
			"forbidden": "Қолжетімділік жоқ.", "account_blocked": "Аккаунт бұғатталған.",
			"invalid_display_name": "2–80 таңбадан тұратын ат енгізіңіз.", "invalid_birth_date": "Туған күнді дұрыс енгізіңіз.",
			"invalid_age": "AikaBot 18–100 жастағы қолданушыларға арналған.", "invalid_purpose": "Танысу мақсатын көрсетіңіз.",
			"bio_too_long": "Сипаттама 500 таңбадан аспауы керек.", "invalid_language": "Қолжетімді тілді таңдаңыз.",
			"invalid_photo_url": "Фотоға дұрыс HTTPS сілтемесін енгізіңіз.", "photo_required": "Профиль фотосын қосыңыз.",
			"invalid_photo": "Дұрыс JPEG немесе PNG фотосын таңдаңыз.", "photo_too_large": "Фото 8 МБ-тан аспауы керек.",
			"like_cooldown_active":    "Сіз бұл қолданушыға лайк қойып қойдыңыз. Келесі лайк кейінірек қолжетімді.",
			"message_cooldown_active": "Сіз бұл қолданушыға хабарлама жібердіңіз. Келесісін кейінірек жіберуге болады.",
			"message_sent":            "Хабарлама жіберілді.",
			"photo_limit_reached":     "Фото саны шегіне жетті.",
			"photo_not_found":         "Фото табылмады.",
			"photo_order_mismatch":    "Фото тізімі ескірді. Бетті жаңартыңыз.",
			"calls_disabled":          "Бейнеқоңыраулар қазір қолжетімсіз.",
			"call_not_found":          "Қоңырау табылмады немесе аяқталған.",
			"call_busy":               "Сіз қазір қоңырауда отырсыз.",
			"peer_busy":               "Қолданушы қазір сөйлесіп жатыр.",
			"incoming_call_pending":   "Бұл қолданушы сізге қоңырау шалып жатыр.",
			"call_invalid_state":      "Бұл қоңырау үшін әрекет қолжетімсіз.",
			"self_call":               "Өзіңізге қоңырау шала алмайсыз.",
			"self_block":              "Өзіңізді бұғаттай алмайсыз.",
			"user_blocked":            "Қолданушы бұғатталды.",
			"user_unblocked":          "Қолданушы бұғаттан шығарылды.",
		},
		"en": {
			"invalid_request": "Check the submitted data.", "invalid_location": "Invalid location.",
			"invalid_radius": "Choose an available radius.", "invalid_gender": "Invalid gender.",
			"location_required": "Allow location access first.", "self_like": "You cannot like your own profile.",
			"message_required": "Enter a message.", "message_too_long": "The message must not exceed 300 characters.",
			"rate_limit_exceeded": "Too many likes. Try again in a minute.", "recipient_unavailable": "This user is currently unavailable.",
			"profile_required": "Complete and enable your profile first.", "like_sent": "Like sent.",
			"forbidden": "Access denied.", "account_blocked": "Account blocked.",
			"invalid_display_name": "Enter a name between 2 and 80 characters.", "invalid_birth_date": "Enter a valid birth date.",
			"invalid_age": "AikaBot is for users aged 18 to 100.", "invalid_purpose": "Enter your purpose.",
			"bio_too_long": "The bio must not exceed 500 characters.", "invalid_language": "Choose a supported language.",
			"invalid_photo_url": "Enter a valid HTTPS photo URL.", "photo_required": "Add a profile photo.",
			"invalid_photo": "Choose a valid JPEG or PNG photo.", "photo_too_large": "The photo must not exceed 8 MB.",
			"like_cooldown_active":    "You already liked this user. You can like again later.",
			"message_cooldown_active": "You already messaged this user. You can send another one later.",
			"message_sent":            "Message sent.",
			"photo_limit_reached":     "Photo limit reached.",
			"photo_not_found":         "Photo not found.",
			"photo_order_mismatch":    "The photo list is out of date. Reload and try again.",
			"calls_disabled":          "Video calls are unavailable right now.",
			"call_not_found":          "That call no longer exists.",
			"call_busy":               "You are already in a call.",
			"peer_busy":               "This user is already in a call.",
			"incoming_call_pending":   "This user is already calling you.",
			"call_invalid_state":      "That action does not apply to this call.",
			"self_call":               "You cannot call yourself.",
			"self_block":              "You cannot block yourself.",
			"user_blocked":            "User blocked.",
			"user_unblocked":          "User unblocked.",
		},
	}
	language = domain.NormalizeLanguage(language)
	if message := translations[language][code]; message != "" {
		return message
	}
	return translations["ru"][code]
}

type responseRecorder struct {
	http.ResponseWriter
	status int
}

func (w *responseRecorder) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

// Unwrap lets http.ResponseController reach the real writer through this wrapper. The signalling
// poll needs it to extend its write deadline past the server's default WriteTimeout.
func (w *responseRecorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (s *Server) requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &responseRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		s.logger.Info("http request", zap.String("method", r.Method), zap.String("path", r.URL.Path),
			zap.Int("status", recorder.status), zap.Duration("duration", time.Since(start)))
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				s.logger.Error("panic recovered", zap.Any("panic", recovered), zap.String("path", r.URL.Path))
				writeError(w, http.StatusInternalServerError, "server_error", "Server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		// The microphone was previously denied outright. A video call needs both capture devices,
		// and `self` still keeps them to this origin and behind the platform's own permission
		// prompt — nothing here grants access, it only stops the policy from refusing before the
		// user is ever asked.
		w.Header().Set("Permissions-Policy", "camera=(self), microphone=(self), geolocation=(self)")
		w.Header().Set("Content-Security-Policy", s.contentSecurityPolicy)
		next.ServeHTTP(w, r)
	})
}

// contentSecurityPolicy builds the page policy once at startup.
//
// blob: is required by the profile photo picker: the Mini App decodes the chosen file and
// re-encodes it to JPEG in a canvas before upload, and that decode step loads a blob: URL. Without
// it the browser blocks the image load and the upload never starts.
//
// connect-src additionally allows the ICE schemes in use. Engines disagree about whether the
// directive governs an RTCPeerConnection's ICE servers, and a policy that silently blocked the
// relay would look exactly like an ordinary NAT failure, so the schemes are permitted rather than
// left to chance.
//
// Only the scheme is listed, never the full server URL: a CSP source is a scheme or a host, and
// `stun:host:port` is neither — browsers discard such an entry as invalid, which would leave the
// protection this line exists for silently absent.
func contentSecurityPolicy(cfg config.Config) string {
	connect := []string{"'self'"}
	if cfg.Calls.Enabled {
		for _, scheme := range iceSchemes(cfg.Calls.ICEHosts()) {
			connect = append(connect, scheme)
		}
	}
	return "default-src 'self'; script-src 'self' https://telegram.org; style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' https: data: blob:; media-src 'self' blob: mediastream:; " +
		"connect-src " + strings.Join(connect, " ") + "; " +
		"frame-ancestors https://web.telegram.org https://*.telegram.org"
}

// iceSchemes reduces ICE server URLs to their distinct `scheme:` prefixes, in a stable order.
func iceSchemes(urls []string) []string {
	seen := make(map[string]struct{}, 4)
	schemes := make([]string, 0, 4)
	for _, url := range urls {
		scheme, _, found := strings.Cut(url, ":")
		if !found || scheme == "" {
			continue
		}
		if _, duplicate := seen[scheme]; duplicate {
			continue
		}
		seen[scheme] = struct{}{}
		schemes = append(schemes, scheme+":")
	}
	return schemes
}

func (s *Server) httpsOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.Environment == "production" && r.URL.Path != "/health" && r.TLS == nil && r.Header.Get("X-Forwarded-Proto") != "https" {
			writeError(w, http.StatusUpgradeRequired, "https_required", "HTTPS is required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		allowed := origin == "" || origin == s.cfg.MiniAppOrigin ||
			(s.cfg.LocalDev && (origin == "http://localhost:5173" || origin == "http://127.0.0.1:5173"))
		if !allowed {
			writeError(w, http.StatusForbidden, "origin_forbidden", "Origin is not allowed")
			return
		}
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, If-None-Match")
			w.Header().Set("Access-Control-Max-Age", "600")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
