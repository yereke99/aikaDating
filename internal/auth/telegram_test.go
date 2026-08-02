package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"aika/internal/config"
)

func signedInitData(token string, values url.Values) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+values.Get(key))
	}
	secret := hmac.New(sha256.New, []byte("WebAppData"))
	_, _ = secret.Write([]byte(token))
	signature := hmac.New(sha256.New, secret.Sum(nil))
	_, _ = signature.Write([]byte(strings.Join(parts, "\n")))
	values.Set("hash", hex.EncodeToString(signature.Sum(nil)))
	return values.Encode()
}

func TestValidateTelegramInitData(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	validator := NewValidator(config.Config{BotToken: "test-token", AuthMaxAge: 24 * time.Hour})
	validator.SetNowForTest(func() time.Time { return now })
	raw := signedInitData("test-token", url.Values{
		"auth_date": {"1785671940"},
		"query_id":  {"query"},
		"user":      {`{"id":123456789,"first_name":"Aika","username":"aika_user","language_code":"kk","photo_url":"https://example.com/a.jpg"}`},
	})

	profile, err := validator.Validate(raw)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if profile.UserID != 123456789 || profile.Username != "aika_user" || profile.LanguageCode != "kk" {
		t.Fatalf("unexpected profile: %+v", profile)
	}
}

func TestValidateRejectsExpiredInitData(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	validator := NewValidator(config.Config{BotToken: "test-token", AuthMaxAge: time.Hour})
	validator.SetNowForTest(func() time.Time { return now })
	raw := signedInitData("test-token", url.Values{
		"auth_date": {"1785664800"},
		"user":      {`{"id":123456789,"first_name":"Aika"}`},
	})

	if _, err := validator.Validate(raw); err != ErrExpired {
		t.Fatalf("Validate() error = %v, want %v", err, ErrExpired)
	}
}

func TestValidateRejectsTampering(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	validator := NewValidator(config.Config{BotToken: "test-token", AuthMaxAge: time.Hour})
	validator.SetNowForTest(func() time.Time { return now })
	raw := signedInitData("test-token", url.Values{
		"auth_date": {"1785671940"},
		"user":      {`{"id":123,"first_name":"Aika"}`},
	})
	raw = strings.Replace(raw, "Aika", "Mallory", 1)

	if _, err := validator.Validate(raw); err != ErrInvalid {
		t.Fatalf("Validate() error = %v, want %v", err, ErrInvalid)
	}
}
