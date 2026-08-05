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
	LocalDev         bool
	DevUserID        int64
	DevFirstName     string
	DevLastName      string
	DevUsername      string
	DevLanguageCode  string
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
		MaxProfilePhotos:  intEnv("MAX_PROFILE_PHOTOS", 3),
		LocalDev:          boolEnv("LOCAL_DEV", false),
		DevUserID:         int64Env("DEV_TELEGRAM_USER_ID", 0),
		DevFirstName:      env("DEV_TELEGRAM_FIRST_NAME", "Local"),
		DevLastName:       env("DEV_TELEGRAM_LAST_NAME", "Developer"),
		DevUsername:       strings.TrimPrefix(strings.TrimSpace(os.Getenv("DEV_TELEGRAM_USERNAME")), "@"),
		DevLanguageCode:   env("DEV_TELEGRAM_LANGUAGE_CODE", "en"),
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
	return cfg, nil
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
