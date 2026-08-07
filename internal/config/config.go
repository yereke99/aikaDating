package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Environment       string
	Port              string
	DatabasePath      string
	BotToken          string
	BotUsername       string
	MiniAppURL        string
	MiniAppOrigin     string
	WebDir            string
	ProfilePhotoDir   string
	AdminTelegramIDs  map[int64]struct{}
	AuthMaxAge        time.Duration
	LikeRatePerMinute int
	// ActionCooldown is the single window every per-target action timer uses: after a like or a
	// message to one user, the same action towards that user is refused until it elapses.
	ActionCooldown time.Duration
	// MaxProfilePhotos caps a user's gallery.
	MaxProfilePhotos int
	// Calls holds the one-to-one video call settings. Media is peer-to-peer; the server only
	// relays signalling.
	Calls           CallConfig
	LocalDev        bool
	DevUserID       int64
	DevFirstName    string
	DevLastName     string
	DevUsername     string
	DevLanguageCode string
}

// CallConfig describes the one-to-one video call feature. Only the ICE servers ever reach a
// browser, and only over an authenticated request, so a static TURN password never has to be
// baked into the Mini App bundle.
type CallConfig struct {
	Enabled bool
	// InviteTimeout ends an unanswered invitation. The caller and the callee are both told.
	InviteTimeout time.Duration
	// SetupTimeout ends a call that was accepted but never reached the connected state, which is
	// what a failed ICE negotiation looks like from the server's side.
	SetupTimeout time.Duration
	// EventWait is how long a signalling poll is parked before it answers with nothing. The
	// request is flushed the instant an event arrives, so this is only the idle ceiling.
	EventWait time.Duration
	// PresenceTimeout ends an active call when a participant stops polling — a closed Mini App, a
	// dead network. It must stay comfortably above EventWait plus one round trip.
	PresenceTimeout time.Duration
	STUNURLs        []string
	TURNURLs        []string
	TURNUsername    string
	TURNPassword    string
	// TURNSecret enables coturn's `use-auth-secret` REST flow: the server mints a short-lived
	// username/credential pair per request instead of handing out a permanent password.
	TURNSecret        string
	TURNCredentialTTL time.Duration
}

// ICEHosts returns every host a browser may open an ICE connection to, for the
// Content-Security-Policy connect-src list.
func (c CallConfig) ICEHosts() []string {
	hosts := make([]string, 0, len(c.STUNURLs)+len(c.TURNURLs))
	hosts = append(hosts, c.STUNURLs...)
	hosts = append(hosts, c.TURNURLs...)
	return hosts
}

func Load() (Config, error) {
	cfg := Config{
		Environment:       env("APP_ENV", "development"),
		Port:              env("PORT", "8080"),
		DatabasePath:      env("DATABASE_PATH", "./data/aikabot.db"),
		BotToken:          strings.TrimSpace(os.Getenv("BOT_TOKEN")),
		BotUsername:       strings.TrimPrefix(strings.TrimSpace(os.Getenv("BOT_USERNAME")), "@"),
		MiniAppURL:        strings.TrimRight(strings.TrimSpace(os.Getenv("MINI_APP_URL")), "/"),
		MiniAppOrigin:     strings.TrimRight(strings.TrimSpace(os.Getenv("MINI_APP_ORIGIN")), "/"),
		WebDir:            env("WEB_DIR", "./web/dist"),
		ProfilePhotoDir:   env("PROFILE_PHOTO_DIR", "/profile_photo"),
		AuthMaxAge:        durationEnv("AUTH_MAX_AGE", 24*time.Hour),
		LikeRatePerMinute: intEnv("LIKE_RATE_PER_MINUTE", 5),
		ActionCooldown:    durationEnv("ACTION_COOLDOWN", 30*time.Minute),
		MaxProfilePhotos:  intEnv("MAX_PROFILE_PHOTOS", 4),
		Calls: CallConfig{
			Enabled:           boolEnv("CALLS_ENABLED", true),
			InviteTimeout:     durationEnv("CALL_INVITE_TIMEOUT", 45*time.Second),
			SetupTimeout:      durationEnv("CALL_SETUP_TIMEOUT", 60*time.Second),
			EventWait:         durationEnv("CALL_EVENT_WAIT", 20*time.Second),
			PresenceTimeout:   durationEnv("CALL_PRESENCE_TIMEOUT", 45*time.Second),
			STUNURLs:          listEnv("STUN_URLS", "stun:stun.l.google.com:19302,stun:stun1.l.google.com:19302"),
			TURNURLs:          listEnv("TURN_URLS", ""),
			TURNUsername:      strings.TrimSpace(os.Getenv("TURN_USERNAME")),
			TURNPassword:      strings.TrimSpace(os.Getenv("TURN_PASSWORD")),
			TURNSecret:        strings.TrimSpace(os.Getenv("TURN_STATIC_AUTH_SECRET")),
			TURNCredentialTTL: durationEnv("TURN_CREDENTIAL_TTL", time.Hour),
		},
		LocalDev:        boolEnv("LOCAL_DEV", false),
		DevUserID:       int64Env("DEV_TELEGRAM_USER_ID", 0),
		DevFirstName:    env("DEV_TELEGRAM_FIRST_NAME", "Local"),
		DevLastName:     env("DEV_TELEGRAM_LAST_NAME", "Developer"),
		DevUsername:     strings.TrimPrefix(strings.TrimSpace(os.Getenv("DEV_TELEGRAM_USERNAME")), "@"),
		DevLanguageCode: env("DEV_TELEGRAM_LANGUAGE_CODE", "en"),
	}

	admins, err := parseIDs(os.Getenv("ADMIN_TELEGRAM_IDS"))
	if err != nil {
		return Config{}, fmt.Errorf("ADMIN_TELEGRAM_IDS: %w", err)
	}
	cfg.AdminTelegramIDs = admins

	if cfg.DatabasePath == "" {
		return Config{}, errors.New("DATABASE_PATH is required")
	}
	if cfg.ProfilePhotoDir == "" {
		return Config{}, errors.New("PROFILE_PHOTO_DIR is required")
	}
	if cfg.BotToken == "" {
		return Config{}, errors.New("BOT_TOKEN is required")
	}
	if cfg.BotUsername == "" {
		return Config{}, errors.New("BOT_USERNAME is required")
	}
	if cfg.MiniAppURL == "" {
		return Config{}, errors.New("MINI_APP_URL is required")
	}
	parsedURL, err := url.Parse(cfg.MiniAppURL)
	if err != nil || parsedURL.Host == "" {
		return Config{}, errors.New("MINI_APP_URL must be an absolute URL")
	}
	if cfg.MiniAppOrigin == "" {
		cfg.MiniAppOrigin = parsedURL.Scheme + "://" + parsedURL.Host
	}
	if cfg.Environment == "production" && parsedURL.Scheme != "https" {
		return Config{}, errors.New("MINI_APP_URL must use HTTPS in production")
	}
	if cfg.LocalDev && cfg.Environment == "production" {
		return Config{}, errors.New("LOCAL_DEV cannot be enabled in production")
	}
	if cfg.LocalDev && cfg.DevUserID <= 0 {
		return Config{}, errors.New("DEV_TELEGRAM_USER_ID is required when LOCAL_DEV=true")
	}
	if cfg.AuthMaxAge <= 0 {
		return Config{}, errors.New("AUTH_MAX_AGE must be positive")
	}
	if cfg.LikeRatePerMinute < 1 || cfg.LikeRatePerMinute > 60 {
		return Config{}, errors.New("LIKE_RATE_PER_MINUTE must be between 1 and 60")
	}
	if cfg.ActionCooldown <= 0 || cfg.ActionCooldown > 24*time.Hour {
		return Config{}, errors.New("ACTION_COOLDOWN must be between 1s and 24h")
	}
	if cfg.MaxProfilePhotos < 1 || cfg.MaxProfilePhotos > 12 {
		return Config{}, errors.New("MAX_PROFILE_PHOTOS must be between 1 and 12")
	}
	if err := validateCalls(cfg.Calls); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func validateCalls(calls CallConfig) error {
	if !calls.Enabled {
		return nil
	}
	if calls.InviteTimeout < 10*time.Second || calls.InviteTimeout > 3*time.Minute {
		return errors.New("CALL_INVITE_TIMEOUT must be between 10s and 3m")
	}
	if calls.SetupTimeout < 10*time.Second || calls.SetupTimeout > 5*time.Minute {
		return errors.New("CALL_SETUP_TIMEOUT must be between 10s and 5m")
	}
	if calls.EventWait < time.Second || calls.EventWait > time.Minute {
		return errors.New("CALL_EVENT_WAIT must be between 1s and 1m")
	}
	// A participant is declared gone only after it has had a full idle poll plus a round trip to
	// come back, otherwise a healthy client would be dropped mid-call between two polls.
	if calls.PresenceTimeout <= calls.EventWait {
		return errors.New("CALL_PRESENCE_TIMEOUT must be greater than CALL_EVENT_WAIT")
	}
	for _, url := range calls.ICEHosts() {
		if !strings.HasPrefix(url, "stun:") && !strings.HasPrefix(url, "stuns:") &&
			!strings.HasPrefix(url, "turn:") && !strings.HasPrefix(url, "turns:") {
			return fmt.Errorf("invalid ICE server URL %q", url)
		}
	}
	if len(calls.TURNURLs) > 0 && calls.TURNSecret == "" && (calls.TURNUsername == "" || calls.TURNPassword == "") {
		return errors.New("TURN_URLS requires either TURN_STATIC_AUTH_SECRET or TURN_USERNAME and TURN_PASSWORD")
	}
	if calls.TURNCredentialTTL < time.Minute || calls.TURNCredentialTTL > 24*time.Hour {
		return errors.New("TURN_CREDENTIAL_TTL must be between 1m and 24h")
	}
	return nil
}

func (c Config) IsAdmin(telegramUserID int64) bool {
	_, ok := c.AdminTelegramIDs[telegramUserID]
	return ok
}

func parseIDs(raw string) (map[int64]struct{}, error) {
	result := make(map[int64]struct{})
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("invalid Telegram user ID %q", part)
		}
		result[id] = struct{}{}
	}
	return result, nil
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

// listEnv reads a comma-separated list, dropping empty entries so a trailing comma is harmless.
func listEnv(key, fallback string) []string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		raw = fallback
	}
	values := make([]string, 0, 4)
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			values = append(values, part)
		}
	}
	return values
}

func boolEnv(key string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func intEnv(key string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
	if err != nil {
		return fallback
	}
	return value
}

func int64Env(key string, fallback int64) int64 {
	value, err := strconv.ParseInt(strings.TrimSpace(os.Getenv(key)), 10, 64)
	if err != nil {
		return fallback
	}
	return value
}

func durationEnv(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}
