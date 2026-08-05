package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	// ErrPhotoLimit is returned when a user already owns the maximum number of photos.
	ErrPhotoLimit = errors.New("photo limit reached")
	// ErrPhotoRequired is returned when removing a photo would leave a profile with no avatar at
	// all, which the profile rules do not allow.
	ErrPhotoRequired = errors.New("at least one photo is required")
	// ErrPhotoOrderMismatch is returned when a reorder request does not list exactly the photos the
	// user owns.
	ErrPhotoOrderMismatch = errors.New("photo order does not match the stored photos")
)

// Photo is one image in a user's gallery. Position 0 is always the primary photo: keeping the two
// in sync means a reorder that moves a photo to the front is also the gesture that promotes it,
// and every consumer can rely on photos[0] being the avatar.
type Photo struct {
	ID           string
	UserID       string
	FilePath     string
	PublicURL    string
	ThumbnailURL string
	SortOrder    int
	IsPrimary    bool
	Width        int
	Height       int
	MIMEType     string
	CreatedAt    time.Time
}

// Thumbnail is the small variant when one exists, and the full photo otherwise.
func (p Photo) Thumbnail() string {
	if p.ThumbnailURL != "" {
		return p.ThumbnailURL
	}
	return p.PublicURL
}

const photoColumns = `id, user_id, file_path, public_url, thumb_url, sort_order, is_primary, width, height, mime_type, created_at`

func scanPhotos(rows *sql.Rows) ([]Photo, error) {
	defer rows.Close()
	photos := make([]Photo, 0, 6)
	for rows.Next() {
		var photo Photo
		if err := rows.Scan(&photo.ID, &photo.UserID, &photo.FilePath, &photo.PublicURL, &photo.ThumbnailURL,
			&photo.SortOrder, &photo.IsPrimary, &photo.Width, &photo.Height, &photo.MIMEType, &photo.CreatedAt); err != nil {
			return nil, err
		}
		photos = append(photos, photo)
	}
	return photos, rows.Err()
}

// ListPhotos returns one user's gallery in display order.
func (s *Store) ListPhotos(ctx context.Context, userID string) ([]Photo, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+photoColumns+` FROM user_photos WHERE user_id = ? ORDER BY sort_order, created_at`, userID)
	if err != nil {
		return nil, err
	}
	return scanPhotos(rows)
}

// ListPhotosFor loads the galleries of several users in one query, for list responses that would
// otherwise issue a query per card.
func (s *Store) ListPhotosFor(ctx context.Context, userIDs []string) (map[string][]Photo, error) {
	result := make(map[string][]Photo, len(userIDs))
	if len(userIDs) == 0 {
		return result, nil
	}
	arguments := make([]any, len(userIDs))
	for index, id := range userIDs {
		arguments[index] = id
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(userIDs)), ",")
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+photoColumns+` FROM user_photos WHERE user_id IN (`+placeholders+`) ORDER BY user_id, sort_order, created_at`, arguments...)
	if err != nil {
		return nil, err
	}
	photos, err := scanPhotos(rows)
	if err != nil {
		return nil, err
	}
	for _, photo := range photos {
		result[photo.UserID] = append(result[photo.UserID], photo)
	}
	return result, nil
}

// AddPhoto inserts a photo the caller has already written to disk. The count check and the insert
// share one transaction, so two concurrent uploads cannot both pass a limit check.
func (s *Store) AddPhoto(ctx context.Context, userID string, photo Photo, maxPhotos int) ([]Photo, error) {
	id, err := newUUID()
	if err != nil {
		return nil, err
	}
	var stored []Photo
	err = s.inTransaction(ctx, func(tx *sql.Tx) error {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_photos WHERE user_id = ?`, userID).Scan(&count); err != nil {
			return err
		}
		if count >= maxPhotos {
			return ErrPhotoLimit
		}
		_, err := tx.ExecContext(ctx, `
            INSERT INTO user_photos (id, user_id, file_path, public_url, thumb_url, sort_order, is_primary, width, height, mime_type)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, userID, photo.FilePath, photo.PublicURL, photo.ThumbnailURL, count, count == 0, photo.Width, photo.Height, photo.MIMEType)
		if err != nil {
			return err
		}
		stored, err = normalizeGallery(ctx, tx, userID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return stored, nil
}

// ReplacePrimaryPhoto swaps the current primary photo for a new one in a single transaction. It
// backs the original single-avatar upload endpoint, which replaces rather than appends, and never
// leaves the profile without an avatar even if the caller is already at the photo limit.
func (s *Store) ReplacePrimaryPhoto(ctx context.Context, userID string, photo Photo) (string, []Photo, error) {
	id, err := newUUID()
	if err != nil {
		return "", nil, err
	}
	var removedPath string
	var stored []Photo
	err = s.inTransaction(ctx, func(tx *sql.Tx) error {
		var previousID, previousPath string
		err := tx.QueryRowContext(ctx,
			`SELECT id, file_path FROM user_photos WHERE user_id = ? ORDER BY sort_order, created_at LIMIT 1`,
			userID).Scan(&previousID, &previousPath)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO user_photos (id, user_id, file_path, public_url, thumb_url, sort_order, is_primary, width, height, mime_type)
            VALUES (?, ?, ?, ?, ?, -1, 0, ?, ?, ?)`,
			id, userID, photo.FilePath, photo.PublicURL, photo.ThumbnailURL, photo.Width, photo.Height, photo.MIMEType); err != nil {
			return err
		}
		if previousID != "" {
			if _, err := tx.ExecContext(ctx, `DELETE FROM user_photos WHERE id = ? AND user_id = ?`, previousID, userID); err != nil {
				return err
			}
			removedPath = previousPath
		}
		stored, err = normalizeGallery(ctx, tx, userID)
		return err
	})
	if err != nil {
		return "", nil, err
	}
	return removedPath, stored, nil
}

// DeletePhoto removes one photo the caller owns and returns the stored file path so the file can be
// deleted only after the transaction has committed. `avatarOptional` reflects whether the profile
// would still have an avatar (a Telegram photo) once the last gallery photo is gone.
func (s *Store) DeletePhoto(ctx context.Context, userID, photoID string, avatarOptional bool) (string, []Photo, error) {
	var removedPath string
	var stored []Photo
	err := s.inTransaction(ctx, func(tx *sql.Tx) error {
		var filePath string
		err := tx.QueryRowContext(ctx, `SELECT file_path FROM user_photos WHERE id = ? AND user_id = ?`, photoID, userID).Scan(&filePath)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_photos WHERE user_id = ?`, userID).Scan(&count); err != nil {
			return err
		}
		if count <= 1 && !avatarOptional {
			return ErrPhotoRequired
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM user_photos WHERE id = ? AND user_id = ?`, photoID, userID); err != nil {
			return err
		}
		removedPath = filePath
		stored, err = normalizeGallery(ctx, tx, userID)
		return err
	})
	if err != nil {
		return "", nil, err
	}
	return removedPath, stored, nil
}

// ReorderPhotos applies an explicit order. The request must name exactly the photos the user owns,
// which rejects both a stale client list and an attempt to slip in another account's photo ID.
func (s *Store) ReorderPhotos(ctx context.Context, userID string, photoIDs []string) ([]Photo, error) {
	var stored []Photo
	err := s.inTransaction(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id FROM user_photos WHERE user_id = ?`, userID)
		if err != nil {
			return err
		}
		owned := make(map[string]struct{})
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			owned[id] = struct{}{}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(owned) != len(photoIDs) {
			return ErrPhotoOrderMismatch
		}
		seen := make(map[string]struct{}, len(photoIDs))
		for _, id := range photoIDs {
			if _, ok := owned[id]; !ok {
				return ErrPhotoOrderMismatch
			}
			if _, duplicate := seen[id]; duplicate {
				return ErrPhotoOrderMismatch
			}
			seen[id] = struct{}{}
		}
		for position, id := range photoIDs {
			if _, err := tx.ExecContext(ctx,
				`UPDATE user_photos SET sort_order = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND user_id = ?`,
				position, id, userID); err != nil {
				return err
			}
		}
		stored, err = normalizeGallery(ctx, tx, userID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return stored, nil
}

// SetPrimaryPhoto promotes one photo to the front of the gallery, keeping the relative order of the
// rest.
func (s *Store) SetPrimaryPhoto(ctx context.Context, userID, photoID string) ([]Photo, error) {
	var stored []Photo
	err := s.inTransaction(ctx, func(tx *sql.Tx) error {
		var exists int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM user_photos WHERE id = ? AND user_id = ?`, photoID, userID).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		// Push everything back one slot, then pull the chosen photo to the front. Positions are
		// renumbered from zero by normalizeGallery, so the temporary gap is harmless.
		if _, err := tx.ExecContext(ctx,
			`UPDATE user_photos SET sort_order = sort_order + 1, updated_at = CURRENT_TIMESTAMP WHERE user_id = ?`, userID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE user_photos SET sort_order = -1, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND user_id = ?`, photoID, userID); err != nil {
			return err
		}
		stored, err = normalizeGallery(ctx, tx, userID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return stored, nil
}

// normalizeGallery renumbers a user's photos from zero, makes the first one primary, and mirrors it
// into users.custom_photo_url so every existing read path — nearby cards, public profiles, the
// Telegram notification photo — keeps working without knowing about the gallery.
func normalizeGallery(ctx context.Context, tx *sql.Tx, userID string) ([]Photo, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT `+photoColumns+` FROM user_photos WHERE user_id = ? ORDER BY sort_order, created_at`, userID)
	if err != nil {
		return nil, err
	}
	photos, err := scanPhotos(rows)
	if err != nil {
		return nil, err
	}
	// The unique index allows one primary per user, so every flag is cleared before the new one is
	// set rather than swapped in place.
	if _, err := tx.ExecContext(ctx, `UPDATE user_photos SET is_primary = 0 WHERE user_id = ?`, userID); err != nil {
		return nil, err
	}
	for position := range photos {
		photos[position].SortOrder = position
		photos[position].IsPrimary = position == 0
		if _, err := tx.ExecContext(ctx,
			`UPDATE user_photos SET sort_order = ?, is_primary = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
			position, position == 0, photos[position].ID); err != nil {
			return nil, err
		}
	}
	primary := ""
	if len(photos) > 0 {
		primary = photos[0].PublicURL
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE users SET custom_photo_url = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		nullableString(primary), userID); err != nil {
		return nil, err
	}
	return photos, nil
}

func (s *Store) inTransaction(ctx context.Context, action func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	if err := action(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
