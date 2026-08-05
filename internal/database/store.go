package database

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"aika/internal/domain"
	"aika/migrations"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")

type Store struct {
	db *sql.DB
}

func Open(ctx context.Context, databasePath string) (*Store, error) {
	if databasePath != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(databasePath), 0o750); err != nil {
			return nil, fmt.Errorf("create SQLite directory: %w", err)
		}
	}
	dsn := databasePath
	if databasePath != ":memory:" {
		dsn = "file:" + filepath.ToSlash(databasePath) + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open SQLite: %w", err)
	}
	// A single writer connection keeps SQLite behavior predictable for this small MVP.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect to SQLite: %w", err)
	}
	statements, err := migrations.Statements()
	if err != nil {
		db.Close()
		return nil, err
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			db.Close()
			return nil, fmt.Errorf("apply SQLite migration: %w", err)
		}
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() { _ = s.db.Close() }

func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

const userColumns = `
    id, telegram_user_id, telegram_chat_id, username, first_name, last_name,
    telegram_photo_url, telegram_language_code, app_language, display_name, gender,
    birth_date, purpose, bio, custom_photo_url, latitude, longitude,
    location_updated_at, is_profile_completed, is_active, is_blocked,
    created_at, updated_at, last_seen_at`

type scanner interface {
	Scan(dest ...any) error
}

func scanUser(row scanner) (domain.User, error) {
	var user domain.User
	err := row.Scan(
		&user.ID, &user.TelegramUserID, &user.TelegramChatID, &user.Username,
		&user.FirstName, &user.LastName, &user.TelegramPhotoURL,
		&user.TelegramLanguageCode, &user.AppLanguage, &user.DisplayName,
		&user.Gender, &user.BirthDate, &user.Purpose, &user.Bio,
		&user.CustomPhotoURL, &user.Latitude, &user.Longitude,
		&user.LocationUpdatedAt, &user.IsProfileCompleted, &user.IsActive,
		&user.IsBlocked, &user.CreatedAt, &user.UpdatedAt, &user.LastSeenAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.User{}, ErrNotFound
	}
	return user, err
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableInt64(value sql.NullInt64) any {
	if !value.Valid {
		return nil
	}
	return value.Int64
}

func newUUID() (string, error) {
	var value [16]byte
	if _, err := io.ReadFull(rand.Reader, value[:]); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}

func (s *Store) UpsertTelegramUser(ctx context.Context, profile domain.TelegramProfile) (domain.User, error) {
	id, err := newUUID()
	if err != nil {
		return domain.User{}, fmt.Errorf("generate user ID: %w", err)
	}
	query := `
        INSERT INTO users (
            id, telegram_user_id, telegram_chat_id, username, first_name, last_name,
            telegram_photo_url, telegram_language_code, app_language
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT (telegram_user_id) DO UPDATE SET
            telegram_chat_id = COALESCE(excluded.telegram_chat_id, users.telegram_chat_id),
            username = excluded.username,
            first_name = excluded.first_name,
            last_name = excluded.last_name,
            telegram_photo_url = CASE WHEN ? THEN excluded.telegram_photo_url ELSE users.telegram_photo_url END,
            telegram_language_code = excluded.telegram_language_code,
            updated_at = CURRENT_TIMESTAMP,
            last_seen_at = CURRENT_TIMESTAMP
        RETURNING ` + userColumns

	return scanUser(s.db.QueryRowContext(ctx, query,
		id,
		profile.UserID,
		nullableInt64(profile.ChatID),
		nullableString(profile.Username),
		nullableString(profile.FirstName),
		nullableString(profile.LastName),
		nullableString(profile.PhotoURL),
		nullableString(profile.LanguageCode),
		domain.NormalizeLanguage(profile.LanguageCode),
		profile.PhotoURLKnown,
	))
}

func (s *Store) GetUserByTelegramID(ctx context.Context, telegramUserID int64) (domain.User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE telegram_user_id = ?`, telegramUserID))
}

func (s *Store) GetUserByID(ctx context.Context, id string) (domain.User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, id))
}

func (s *Store) GetPublicUserByID(ctx context.Context, id string) (domain.User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `
        SELECT `+userColumns+`
        FROM users
        WHERE id = ?
          AND is_profile_completed = 1
          AND is_active = 1
          AND is_blocked = 0`, id))
}

func (s *Store) UpdateProfile(ctx context.Context, id string, update domain.ProfileUpdate) (domain.User, error) {
	query := `
        UPDATE users SET
            display_name = ?, gender = ?, birth_date = ?, purpose = ?, bio = ?,
            custom_photo_url = ?, app_language = ?, is_active = ?,
            is_profile_completed = ?, updated_at = CURRENT_TIMESTAMP
        WHERE id = ? AND is_blocked = 0
        RETURNING ` + userColumns
	return scanUser(s.db.QueryRowContext(ctx, query,
		nullableString(update.DisplayName), nullableString(update.Gender), update.BirthDate,
		nullableString(update.Purpose), nullableString(update.Bio), nullableString(update.CustomPhotoURL),
		update.AppLanguage, update.IsActive, update.Completed, id,
	))
}

func (s *Store) UpdateLocation(ctx context.Context, id string, latitude, longitude float64) error {
	result, err := s.db.ExecContext(ctx, `
        UPDATE users
        SET latitude = ?, longitude = ?, location_updated_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
        WHERE id = ? AND is_blocked = 0`, latitude, longitude, id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) UpdateProfilePhoto(ctx context.Context, id, photoURL string) (domain.User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `
        UPDATE users
        SET custom_photo_url = ?, updated_at = CURRENT_TIMESTAMP
        WHERE id = ? AND is_blocked = 0
        RETURNING `+userColumns, photoURL, id))
}

func (s *Store) ListLocatedUsers(ctx context.Context) ([]domain.User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userColumns+` FROM users WHERE latitude IS NOT NULL AND longitude IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := make([]domain.User, 0)
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	return users, rows.Err()
}

type Stats struct {
	Total      int64            `json:"total"`
	Completed  int64            `json:"completed"`
	Incomplete int64            `json:"incomplete"`
	Active     int64            `json:"active"`
	Blocked    int64            `json:"blocked"`
	ByGender   map[string]int64 `json:"by_gender"`
	ByLanguage map[string]int64 `json:"by_language"`
}

func (s *Store) AdminStats(ctx context.Context) (Stats, error) {
	stats := Stats{ByGender: make(map[string]int64), ByLanguage: make(map[string]int64)}
	err := s.db.QueryRowContext(ctx, `
        SELECT COUNT(*),
               COALESCE(SUM(CASE WHEN is_profile_completed = 1 THEN 1 ELSE 0 END), 0),
               COALESCE(SUM(CASE WHEN is_profile_completed = 0 THEN 1 ELSE 0 END), 0),
               COALESCE(SUM(CASE WHEN is_active = 1 THEN 1 ELSE 0 END), 0),
               COALESCE(SUM(CASE WHEN is_blocked = 1 THEN 1 ELSE 0 END), 0)
        FROM users`).Scan(&stats.Total, &stats.Completed, &stats.Incomplete, &stats.Active, &stats.Blocked)
	if err != nil {
		return Stats{}, err
	}
	genderRows, err := s.db.QueryContext(ctx, `SELECT COALESCE(gender, 'unspecified'), COUNT(*) FROM users GROUP BY gender`)
	if err != nil {
		return Stats{}, err
	}
	for genderRows.Next() {
		var key string
		var count int64
		if err := genderRows.Scan(&key, &count); err != nil {
			genderRows.Close()
			return Stats{}, err
		}
		stats.ByGender[key] = count
	}
	genderRows.Close()
	if err := genderRows.Err(); err != nil {
		return Stats{}, err
	}
	languageRows, err := s.db.QueryContext(ctx, `SELECT app_language, COUNT(*) FROM users GROUP BY app_language`)
	if err != nil {
		return Stats{}, err
	}
	defer languageRows.Close()
	for languageRows.Next() {
		var key string
		var count int64
		if err := languageRows.Scan(&key, &count); err != nil {
			return Stats{}, err
		}
		stats.ByLanguage[key] = count
	}
	return stats, languageRows.Err()
}

func (s *Store) AdminUsers(ctx context.Context, search string, limit, offset int) ([]domain.User, error) {
	rows, err := s.db.QueryContext(ctx, `
        SELECT `+userColumns+`
        FROM users
        WHERE ? = '' OR LOWER(
            CAST(telegram_user_id AS TEXT) || ' ' || COALESCE(username, '') || ' ' ||
            COALESCE(first_name, '') || ' ' || COALESCE(last_name, '') || ' ' || COALESCE(display_name, '')
        ) LIKE '%' || LOWER(?) || '%'
        ORDER BY created_at DESC
        LIMIT ? OFFSET ?`, search, search, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := make([]domain.User, 0, limit)
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	return users, rows.Err()
}
