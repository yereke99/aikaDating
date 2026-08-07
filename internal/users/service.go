package users

import (
	"context"
	"errors"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"aika/internal/database"
	"aika/internal/domain"
)

var (
	ErrLocationRequired = errors.New("location is required")
	ErrInvalidProfile   = errors.New("invalid profile")
	ErrSelfLike         = errors.New("cannot like your own profile")
	ErrMessageRequired  = errors.New("message cannot be empty")
	ErrMessageTooLong   = errors.New("message exceeds 300 characters")
)

// Photo is one image of a public gallery. The thumbnail is what list cards load; the full URL is
// reserved for the photo viewer.
type Photo struct {
	ID       string `json:"id"`
	URL      string `json:"url"`
	ThumbURL string `json:"thumb_url,omitempty"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
}

type PublicProfile struct {
	ID          string  `json:"id"`
	DisplayName string  `json:"display_name"`
	Username    string  `json:"username,omitempty"`
	PhotoURL    string  `json:"photo_url,omitempty"`
	Age         int     `json:"age,omitempty"`
	Gender      string  `json:"gender,omitempty"`
	Purpose     string  `json:"purpose,omitempty"`
	Bio         string  `json:"bio,omitempty"`
	DistanceKM  float64 `json:"distance_km,omitempty"`
	Photos      []Photo `json:"photos,omitempty"`
	// Deadlines of the viewer's own timers towards this profile, so a list render already knows
	// which actions are blocked without a request per card.
	LikeNextAllowedAt    string `json:"like_next_allowed_at,omitempty"`
	MessageNextAllowedAt string `json:"message_next_allowed_at,omitempty"`
}

type NearbyPage struct {
	Users   []PublicProfile `json:"users"`
	Page    int             `json:"page"`
	HasMore bool            `json:"has_more"`
	// ServerTime anchors every countdown to the server's clock instead of the device's.
	ServerTime string `json:"server_time"`
}

func toPhotos(photos []database.Photo) []Photo {
	if len(photos) == 0 {
		return nil
	}
	result := make([]Photo, 0, len(photos))
	for _, photo := range photos {
		result = append(result, Photo{
			ID: photo.ID, URL: photo.PublicURL, ThumbURL: photo.Thumbnail(),
			Width: photo.Width, Height: photo.Height,
		})
	}
	return result
}

type ProfileInput struct {
	DisplayName    string
	Gender         string
	BirthDate      string
	Purpose        string
	Bio            string
	CustomPhotoURL string
	AppLanguage    string
	IsActive       *bool
}

type ValidationError struct {
	Field string
	Code  string
}

func (e ValidationError) Error() string { return e.Code }

type Service struct {
	store *database.Store
	now   func() time.Time
}

func NewService(store *database.Store) *Service {
	return &Service{store: store, now: time.Now}
}

func (s *Service) UpdateProfile(ctx context.Context, current domain.User, input ProfileInput) (domain.User, error) {
	displayName := strings.TrimSpace(input.DisplayName)
	if utf8.RuneCountInString(displayName) < 2 || utf8.RuneCountInString(displayName) > 80 {
		return domain.User{}, ValidationError{Field: "display_name", Code: "invalid_display_name"}
	}
	gender := strings.TrimSpace(input.Gender)
	if gender != "male" && gender != "female" && gender != "other" {
		return domain.User{}, ValidationError{Field: "gender", Code: "invalid_gender"}
	}
	// Parsed as a calendar date in UTC and stored as one. No local-time conversion happens on
	// either side, so a birthday can never shift by a day between the form and the database.
	birthDate, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(input.BirthDate), time.UTC)
	if err != nil {
		return domain.User{}, ValidationError{Field: "birth_date", Code: "invalid_birth_date"}
	}
	age := ageAt(birthDate, s.now())
	if age < 18 || age > 100 {
		return domain.User{}, ValidationError{Field: "birth_date", Code: "invalid_age"}
	}
	purpose := strings.TrimSpace(input.Purpose)
	if utf8.RuneCountInString(purpose) < 2 || utf8.RuneCountInString(purpose) > 120 {
		return domain.User{}, ValidationError{Field: "purpose", Code: "invalid_purpose"}
	}
	bio := strings.TrimSpace(input.Bio)
	if utf8.RuneCountInString(bio) > 500 {
		return domain.User{}, ValidationError{Field: "bio", Code: "bio_too_long"}
	}
	language := domain.NormalizeLanguage(input.AppLanguage)
	if language != strings.ToLower(strings.TrimSpace(input.AppLanguage)) {
		return domain.User{}, ValidationError{Field: "app_language", Code: "invalid_language"}
	}
	// The gallery owns the avatar once it has a photo: the primary photo is mirrored into
	// custom_photo_url on every gallery change, so a profile save must not push a stale URL back
	// over it. With an empty gallery the original rules still apply, which keeps pre-gallery
	// accounts and any client that sets an external photo URL working unchanged.
	photos, err := s.store.ListPhotos(ctx, current.ID)
	if err != nil {
		return domain.User{}, err
	}
	photoURL := strings.TrimSpace(input.CustomPhotoURL)
	if len(photos) > 0 {
		photoURL = photos[0].PublicURL
	} else {
		if photoURL != "" && !validHTTPSURL(photoURL) && !isManagedPhotoURL(photoURL, current.ID) {
			return domain.User{}, ValidationError{Field: "custom_photo_url", Code: "invalid_photo_url"}
		}
		if len(photoURL) > 2048 {
			return domain.User{}, ValidationError{Field: "custom_photo_url", Code: "invalid_photo_url"}
		}
	}
	completed := photoURL != "" || (current.TelegramPhotoURL.Valid && current.TelegramPhotoURL.String != "")
	if !completed {
		return domain.User{}, ValidationError{Field: "custom_photo_url", Code: "photo_required"}
	}
	isActive := current.IsActive
	if input.IsActive != nil {
		isActive = *input.IsActive
	}
	return s.store.UpdateProfile(ctx, current.ID, domain.ProfileUpdate{
		DisplayName:    displayName,
		Gender:         gender,
		BirthDate:      birthDate,
		Purpose:        purpose,
		Bio:            bio,
		CustomPhotoURL: photoURL,
		AppLanguage:    language,
		IsActive:       isActive,
		Completed:      completed,
	})
}

func validHTTPSURL(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil
}

// The two shapes a locally stored photo URL can take: the original flat avatar named after the
// user, and a photo inside that user's own gallery directory.
var (
	managedPhotoPattern = regexp.MustCompile(`^/profile_photo/([0-9a-f-]{36})\.jpg$`)
	galleryPhotoPattern = regexp.MustCompile(`^/profile_photo/users/([0-9a-f-]{36})/photos/[0-9a-f-]{36}(_t)?\.jpg$`)
)

func isManagedPhotoURL(raw, userID string) bool {
	if match := managedPhotoPattern.FindStringSubmatch(raw); len(match) == 2 {
		return match[1] == userID
	}
	match := galleryPhotoPattern.FindStringSubmatch(raw)
	return len(match) == 3 && match[1] == userID
}

func ageAt(birthDate, now time.Time) int {
	age := now.Year() - birthDate.Year()
	if now.Month() < birthDate.Month() || (now.Month() == birthDate.Month() && now.Day() < birthDate.Day()) {
		age--
	}
	return age
}

type locatedProfile struct {
	profile  PublicProfile
	distance float64
}

func FilterNearby(current domain.User, candidates []domain.User, radiusKM float64, gender string, page, limit int, now time.Time) NearbyPage {
	matched := make([]locatedProfile, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.ID == current.ID || candidate.IsBlocked || !candidate.IsActive || !candidate.IsProfileCompleted {
			continue
		}
		if !candidate.Latitude.Valid || !candidate.Longitude.Valid {
			continue
		}
		if gender != "" && (!candidate.Gender.Valid || candidate.Gender.String != gender) {
			continue
		}
		distance := HaversineKM(current.Latitude.Float64, current.Longitude.Float64, candidate.Latitude.Float64, candidate.Longitude.Float64)
		if distance > radiusKM {
			continue
		}
		matched = append(matched, locatedProfile{
			profile:  toPublicProfile(candidate, now),
			distance: distance,
		})
	}
	sort.SliceStable(matched, func(i, j int) bool { return matched[i].distance < matched[j].distance })

	start := (page - 1) * limit
	if start >= len(matched) {
		return NearbyPage{Users: []PublicProfile{}, Page: page}
	}
	end := min(start+limit, len(matched))
	result := make([]PublicProfile, 0, end-start)
	for _, match := range matched[start:end] {
		profile := match.profile
		profile.DistanceKM = math.Round(match.distance*10) / 10
		result = append(result, profile)
	}
	return NearbyPage{Users: result, Page: page, HasMore: end < len(matched)}
}

func (s *Service) Nearby(ctx context.Context, current domain.User, radiusKM float64, gender string, page, limit int) (NearbyPage, error) {
	if !current.Latitude.Valid || !current.Longitude.Valid {
		return NearbyPage{}, ErrLocationRequired
	}
	candidates, err := s.store.ListLocatedUsers(ctx)
	if err != nil {
		return NearbyPage{}, err
	}
	// Blocks are applied before ranking, so a hidden person never occupies a slot on a page and
	// the result count stays honest. The lookup runs once per request, not once per candidate.
	hidden, err := s.store.BlockedPairIDs(ctx, current.ID)
	if err != nil {
		return NearbyPage{}, err
	}
	if len(hidden) > 0 {
		visible := candidates[:0]
		for _, candidate := range candidates {
			if _, blocked := hidden[candidate.ID]; !blocked {
				visible = append(visible, candidate)
			}
		}
		candidates = visible
	}
	now := s.now().UTC()
	result := FilterNearby(current, candidates, radiusKM, gender, page, limit, now)
	result.ServerTime = now.Format(time.RFC3339)
	if err := s.decorate(ctx, current.ID, result.Users, now); err != nil {
		return NearbyPage{}, err
	}
	return result, nil
}

// decorate attaches the galleries and the viewer's cooldown deadlines to a page of profiles. Both
// are looked up in one query each, so a 2-second refresh costs two statements regardless of page
// size.
func (s *Service) decorate(ctx context.Context, viewerID string, profiles []PublicProfile, now time.Time) error {
	if len(profiles) == 0 {
		return nil
	}
	ids := make([]string, 0, len(profiles))
	for _, profile := range profiles {
		ids = append(ids, profile.ID)
	}
	galleries, err := s.store.ListPhotosFor(ctx, ids)
	if err != nil {
		return err
	}
	cooldowns, err := s.store.ActiveCooldowns(ctx, viewerID, now)
	if err != nil {
		return err
	}
	for index := range profiles {
		profiles[index].Photos = toPhotos(galleries[profiles[index].ID])
		active := cooldowns[profiles[index].ID]
		if next, ok := active[database.ActionLike]; ok {
			profiles[index].LikeNextAllowedAt = next.Format(time.RFC3339)
		}
		if next, ok := active[database.ActionMessage]; ok {
			profiles[index].MessageNextAllowedAt = next.Format(time.RFC3339)
		}
	}
	return nil
}

// PublicProfileWithPhotos loads one profile the way the nearby list would present it.
func (s *Service) PublicProfileWithPhotos(ctx context.Context, viewerID string, user domain.User) (PublicProfile, error) {
	now := s.now().UTC()
	profile := toPublicProfile(user, now)
	page := []PublicProfile{profile}
	if err := s.decorate(ctx, viewerID, page, now); err != nil {
		return PublicProfile{}, err
	}
	return page[0], nil
}

func ToPublicProfile(user domain.User, now time.Time) PublicProfile {
	return toPublicProfile(user, now)
}

func toPublicProfile(user domain.User, now time.Time) PublicProfile {
	profile := PublicProfile{
		ID:          user.ID,
		DisplayName: user.Name(),
		PhotoURL:    user.EffectivePhotoURL(),
		Age:         user.Age(now),
	}
	if user.Username.Valid {
		profile.Username = user.Username.String
	}
	if user.Gender.Valid {
		profile.Gender = user.Gender.String
	}
	if user.Purpose.Valid {
		profile.Purpose = user.Purpose.String
	}
	if user.Bio.Valid {
		profile.Bio = user.Bio.String
	}
	return profile
}

func HaversineKM(lat1, lon1, lat2, lon2 float64) float64 {
	const earthRadiusKM = 6371.0088
	toRadians := func(value float64) float64 { return value * math.Pi / 180 }
	dLat := toRadians(lat2 - lat1)
	dLon := toRadians(lon2 - lon1)
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(toRadians(lat1))*math.Cos(toRadians(lat2))*math.Sin(dLon/2)*math.Sin(dLon/2)
	return earthRadiusKM * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

func ValidateLike(senderID, recipientID string, message *string) (string, error) {
	if senderID == recipientID {
		return "", ErrSelfLike
	}
	if message == nil {
		return "", nil
	}
	trimmed := strings.TrimSpace(*message)
	if trimmed == "" {
		return "", ErrMessageRequired
	}
	if !utf8.ValidString(trimmed) || utf8.RuneCountInString(trimmed) > 300 {
		return "", ErrMessageTooLong
	}
	return trimmed, nil
}

type rateEntry struct {
	window time.Time
	count  int
}

type LikeLimiter struct {
	mu      sync.Mutex
	limit   int
	entries map[string]rateEntry
	now     func() time.Time
}

func NewLikeLimiter(limit int) *LikeLimiter {
	return &LikeLimiter{limit: limit, entries: make(map[string]rateEntry), now: time.Now}
}

func (l *LikeLimiter) Allow(userID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	entry := l.entries[userID]
	if entry.window.IsZero() || now.Sub(entry.window) >= time.Minute {
		l.entries[userID] = rateEntry{window: now, count: 1}
		return true
	}
	if entry.count >= l.limit {
		return false
	}
	entry.count++
	l.entries[userID] = entry
	return true
}
