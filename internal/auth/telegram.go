package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"aika/internal/config"
	"aika/internal/domain"
)

var (
	ErrUnavailable = errors.New("Telegram initialization data is unavailable")
	ErrInvalid     = errors.New("invalid Telegram initialization data")
	ErrExpired     = errors.New("Telegram initialization data has expired")
)

type telegramUser struct {
	ID           int64  `json:"id"`
	FirstName    string `json:"first_name"`
	LastName     string `json:"last_name"`
	Username     string `json:"username"`
	LanguageCode string `json:"language_code"`
	PhotoURL     string `json:"photo_url"`
}

type Validator struct {
	botToken string
	maxAge   time.Duration
	now      func() time.Time
	localDev bool
	devUser  domain.TelegramProfile
}

func NewValidator(cfg config.Config) *Validator {
	return &Validator{
		botToken: cfg.BotToken,
		maxAge:   cfg.AuthMaxAge,
		now:      time.Now,
		localDev: cfg.LocalDev,
		devUser: domain.TelegramProfile{
			UserID:        cfg.DevUserID,
			FirstName:     cfg.DevFirstName,
			LastName:      cfg.DevLastName,
			Username:      cfg.DevUsername,
			LanguageCode:  cfg.DevLanguageCode,
			PhotoURLKnown: true,
		},
	}
}

func (v *Validator) SetNowForTest(now func() time.Time) {
	v.now = now
}

func (v *Validator) FromRequest(r *http.Request) (domain.TelegramProfile, error) {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if header == "" {
		return domain.TelegramProfile{}, ErrUnavailable
	}
	if v.localDev && strings.EqualFold(header, "dev") {
		return v.devUser, nil
	}
	const prefix = "tma "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return domain.TelegramProfile{}, ErrInvalid
	}
	return v.Validate(strings.TrimSpace(header[len(prefix):]))
}

func (v *Validator) Validate(raw string) (domain.TelegramProfile, error) {
	if raw == "" {
		return domain.TelegramProfile{}, ErrUnavailable
	}
	if len(raw) > 16*1024 {
		return domain.TelegramProfile{}, ErrInvalid
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return domain.TelegramProfile{}, ErrInvalid
	}
	for _, entries := range values {
		if len(entries) != 1 {
			return domain.TelegramProfile{}, ErrInvalid
		}
	}

	receivedHash := values.Get("hash")
	if len(receivedHash) != sha256.Size*2 {
		return domain.TelegramProfile{}, ErrInvalid
	}
	decodedHash, err := hex.DecodeString(receivedHash)
	if err != nil {
		return domain.TelegramProfile{}, ErrInvalid
	}

	keys := make([]string, 0, len(values)-1)
	for key := range values {
		if key != "hash" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+values.Get(key))
	}
	dataCheckString := strings.Join(parts, "\n")

	secretMAC := hmac.New(sha256.New, []byte("WebAppData"))
	_, _ = secretMAC.Write([]byte(v.botToken))
	checkMAC := hmac.New(sha256.New, secretMAC.Sum(nil))
	_, _ = checkMAC.Write([]byte(dataCheckString))
	if !hmac.Equal(decodedHash, checkMAC.Sum(nil)) {
		return domain.TelegramProfile{}, ErrInvalid
	}

	authUnix, err := strconv.ParseInt(values.Get("auth_date"), 10, 64)
	if err != nil || authUnix <= 0 {
		return domain.TelegramProfile{}, ErrInvalid
	}
	authTime := time.Unix(authUnix, 0)
	now := v.now()
	if authTime.After(now.Add(time.Minute)) {
		return domain.TelegramProfile{}, ErrInvalid
	}
	if now.Sub(authTime) > v.maxAge {
		return domain.TelegramProfile{}, ErrExpired
	}

	var user telegramUser
	if err := json.Unmarshal([]byte(values.Get("user")), &user); err != nil || user.ID <= 0 {
		return domain.TelegramProfile{}, fmt.Errorf("%w: user", ErrInvalid)
	}
	return domain.TelegramProfile{
		UserID:        user.ID,
		Username:      strings.TrimSpace(user.Username),
		FirstName:     strings.TrimSpace(user.FirstName),
		LastName:      strings.TrimSpace(user.LastName),
		PhotoURL:      strings.TrimSpace(user.PhotoURL),
		PhotoURLKnown: true,
		LanguageCode:  strings.TrimSpace(user.LanguageCode),
	}, nil
}
