package httpapi

import (
	"context"
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
	"aika/internal/config"
	"aika/internal/database"
	"aika/internal/domain"
	"aika/internal/profilephoto"
	"aika/internal/telegram"
	"aika/internal/users"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

type likeSender interface {
	SendLike(ctx context.Context, recipient, sender domain.User, message string) error
}

type Server struct {
	cfg      config.Config
	store    *database.Store
	users    *users.Service
	auth     *auth.Validator
	telegram likeSender
	limiter  *users.LikeLimiter
	photos   *profilephoto.Store
	logger   *zap.Logger
}

func NewServer(cfg config.Config, store *database.Store, userService *users.Service, validator *auth.Validator, telegramClient likeSender, photos *profilephoto.Store, logger *zap.Logger) *Server {
	return &Server{
		cfg: cfg, store: store, users: userService, auth: validator,
		telegram: telegramClient, limiter: users.NewLikeLimiter(cfg.LikeRatePerMinute), photos: photos, logger: logger,
	}
}

func (s *Server) Router() http.Handler {
	router := chi.NewRouter()
	router.Use(s.recoverer, s.requestLogger, s.securityHeaders, s.httpsOnly, s.cors)
	router.Get("/health", s.health)
	router.Get("/profile_photo/{filename}", s.serveProfilePhoto)
	router.Route("/api", func(api chi.Router) {
		api.Post("/auth/telegram", s.telegramAuth)
		api.Group(func(protected chi.Router) {
			protected.Use(s.requireUser)
			protected.Get("/me", s.getMe)
			protected.Patch("/me", s.patchMe)
			protected.Post("/me/location", s.updateLocation)
			protected.Post("/me/photo", s.uploadProfilePhoto)
			protected.Get("/users/nearby", s.nearbyUsers)
			protected.Get("/users/{id}", s.publicProfile)
			protected.Post("/users/{id}/like", s.likeUser)
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

func (s *Server) uploadProfilePhoto(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, profilephoto.MaxUploadBytes+(1<<20))
	reader, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_photo", localized(currentUser(r).AppLanguage, "invalid_photo"))
		return
	}
	var uploaded []byte
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_photo", localized(currentUser(r).AppLanguage, "invalid_photo"))
			return
		}
		if part.FormName() != "photo" || uploaded != nil {
			part.Close()
			writeError(w, http.StatusBadRequest, "invalid_photo", localized(currentUser(r).AppLanguage, "invalid_photo"))
			return
		}
		uploaded, err = io.ReadAll(io.LimitReader(part, profilephoto.MaxUploadBytes+1))
		part.Close()
		if err != nil {
			s.internalError(w, r, err)
			return
		}
	}
	if len(uploaded) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_photo", localized(currentUser(r).AppLanguage, "invalid_photo"))
		return
	}
	if len(uploaded) > profilephoto.MaxUploadBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "photo_too_large", localized(currentUser(r).AppLanguage, "photo_too_large"))
		return
	}
	user := currentUser(r)
	photoURL, err := s.photos.Save(user.ID, uploaded)
	if errors.Is(err, profilephoto.ErrInvalidImage) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_photo", localized(user.AppLanguage, "invalid_photo"))
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	updated, err := s.store.UpdateProfilePhoto(r.Context(), user.ID, photoURL)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.meDTO(updated))
}

func (s *Server) serveProfilePhoto(w http.ResponseWriter, r *http.Request) {
	path, valid := s.photos.FilePath(chi.URLParam(r, "filename"))
	if !valid {
		http.NotFound(w, r)
		return
	}
	if _, err := os.Stat(path); err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Disposition", "inline")
	http.ServeFile(w, r, path)
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
	writeJSON(w, http.StatusOK, s.meDTO(user))
}

func (s *Server) getMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.meDTO(currentUser(r)))
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
	writeJSON(w, http.StatusOK, s.meDTO(updated))
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
	writeJSON(w, http.StatusOK, result)
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
	writeJSON(w, http.StatusOK, users.ToPublicProfile(user, time.Now()))
}

type likeRequest struct {
	Message *string `json:"message"`
}

func (s *Server) likeUser(w http.ResponseWriter, r *http.Request) {
	id, valid := pathID(r)
	if !valid {
		writeError(w, http.StatusBadRequest, "invalid_user_id", localized(currentUser(r).AppLanguage, "invalid_request"))
		return
	}
	var request likeRequest
	if err := decodeJSON(w, r, &request, 2<<10); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", localized(currentUser(r).AppLanguage, "invalid_request"))
		return
	}
	sender := currentUser(r)
	message, err := users.ValidateLike(sender.ID, id, request.Message)
	if errors.Is(err, users.ErrSelfLike) {
		writeError(w, http.StatusConflict, "self_like", localized(sender.AppLanguage, "self_like"))
		return
	}
	if errors.Is(err, users.ErrMessageRequired) {
		writeError(w, http.StatusUnprocessableEntity, "message_required", localized(sender.AppLanguage, "message_required"))
		return
	}
	if errors.Is(err, users.ErrMessageTooLong) {
		writeError(w, http.StatusUnprocessableEntity, "message_too_long", localized(sender.AppLanguage, "message_too_long"))
		return
	}
	if !sender.IsActive || !sender.IsProfileCompleted {
		writeError(w, http.StatusConflict, "profile_required", localized(sender.AppLanguage, "profile_required"))
		return
	}
	if !s.limiter.Allow(sender.ID) {
		writeError(w, http.StatusTooManyRequests, "rate_limit_exceeded", localized(sender.AppLanguage, "rate_limit_exceeded"))
		return
	}
	recipient, err := s.store.GetUserByID(r.Context(), id)
	if errors.Is(err, database.ErrNotFound) || (err == nil && (recipient.IsBlocked || !recipient.IsActive || !recipient.IsProfileCompleted)) {
		writeError(w, http.StatusNotFound, "recipient_unavailable", localized(sender.AppLanguage, "recipient_unavailable"))
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if err := s.telegram.SendLike(r.Context(), recipient, sender, message); err != nil {
		if errors.Is(err, telegram.ErrRecipientUnavailable) {
			writeError(w, http.StatusConflict, "recipient_unavailable", localized(sender.AppLanguage, "recipient_unavailable"))
			return
		}
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": localized(sender.AppLanguage, "like_sent")})
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

type errorEnvelope struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	var response errorEnvelope
	response.Error.Code = code
	response.Error.Message = message
	writeJSON(w, status, response)
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
		w.Header().Set("Permissions-Policy", "camera=(self), microphone=(), geolocation=(self)")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' https://telegram.org; style-src 'self' 'unsafe-inline'; img-src 'self' https: data:; connect-src 'self'; frame-ancestors https://web.telegram.org https://*.telegram.org")
		next.ServeHTTP(w, r)
	})
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
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Set("Access-Control-Max-Age", "600")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
