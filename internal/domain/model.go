package domain

import (
	"database/sql"
	"strings"
	"time"
)

const (
	LanguageRussian = "ru"
	LanguageKazakh  = "kk"
	LanguageEnglish = "en"
)

type TelegramProfile struct {
	UserID        int64
	ChatID        sql.NullInt64
	Username      string
	FirstName     string
	LastName      string
	PhotoURL      string
	PhotoURLKnown bool
	LanguageCode  string
}

type ProfileUpdate struct {
	DisplayName    string
	Gender         string
	BirthDate      time.Time
	Purpose        string
	Bio            string
	CustomPhotoURL string
	AppLanguage    string
	IsActive       bool
	Completed      bool
}

type User struct {
	ID                   string
	TelegramUserID       int64
	TelegramChatID       sql.NullInt64
	Username             sql.NullString
	FirstName            sql.NullString
	LastName             sql.NullString
	TelegramPhotoURL     sql.NullString
	TelegramLanguageCode sql.NullString
	AppLanguage          string
	DisplayName          sql.NullString
	Gender               sql.NullString
	BirthDate            sql.NullTime
	Purpose              sql.NullString
	Bio                  sql.NullString
	CustomPhotoURL       sql.NullString
	Latitude             sql.NullFloat64
	Longitude            sql.NullFloat64
	LocationUpdatedAt    sql.NullTime
	IsProfileCompleted   bool
	IsActive             bool
	IsBlocked            bool
	CreatedAt            time.Time
	UpdatedAt            time.Time
	LastSeenAt           time.Time
}

func (u User) EffectivePhotoURL() string {
	if u.CustomPhotoURL.Valid && strings.TrimSpace(u.CustomPhotoURL.String) != "" {
		return u.CustomPhotoURL.String
	}
	if u.TelegramPhotoURL.Valid {
		return u.TelegramPhotoURL.String
	}
	return ""
}

func (u User) Name() string {
	if u.DisplayName.Valid && strings.TrimSpace(u.DisplayName.String) != "" {
		return strings.TrimSpace(u.DisplayName.String)
	}
	name := strings.TrimSpace(strings.TrimSpace(u.FirstName.String) + " " + strings.TrimSpace(u.LastName.String))
	if name != "" {
		return name
	}
	if u.Username.Valid && u.Username.String != "" {
		return u.Username.String
	}
	return "Telegram user"
}

func (u User) Age(now time.Time) int {
	if !u.BirthDate.Valid {
		return 0
	}
	birth := u.BirthDate.Time
	age := now.Year() - birth.Year()
	anniversary := time.Date(now.Year(), birth.Month(), birth.Day(), 0, 0, 0, 0, now.Location())
	if now.Before(anniversary) {
		age--
	}
	return age
}

func NormalizeLanguage(languageCode string) string {
	base := strings.ToLower(strings.Split(strings.TrimSpace(languageCode), "-")[0])
	switch base {
	case LanguageKazakh, LanguageEnglish, LanguageRussian:
		return base
	default:
		return LanguageRussian
	}
}

func NullString(value string) sql.NullString {
	value = strings.TrimSpace(value)
	return sql.NullString{String: value, Valid: value != ""}
}
