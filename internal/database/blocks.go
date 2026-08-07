package database

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"aika/internal/domain"
)

// ErrSelfBlock is returned when a user tries to block themselves.
var ErrSelfBlock = errors.New("cannot block yourself")

// BlockedUser is one entry of the caller's own block list, carrying just enough to recognise the
// person and undo it.
type BlockedUser struct {
	ID          string
	DisplayName string
	Username    string
	PhotoURL    string
	BlockedAt   time.Time
}

// BlockUser hides two people from each other. Blocking someone who is already blocked succeeds
// without changing anything, so a retried request is harmless.
func (s *Store) BlockUser(ctx context.Context, blockerID, blockedID string) error {
	if blockerID == blockedID {
		return ErrSelfBlock
	}
	id, err := newUUID()
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
        INSERT INTO user_blocks (id, blocker_user_id, blocked_user_id)
        VALUES (?, ?, ?)
        ON CONFLICT (blocker_user_id, blocked_user_id) DO NOTHING`, id, blockerID, blockedID)
	return err
}

// UnblockUser removes a block the caller made. Removing one that does not exist is not an error,
// for the same reason.
func (s *Store) UnblockUser(ctx context.Context, blockerID, blockedID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM user_blocks WHERE blocker_user_id = ? AND blocked_user_id = ?`, blockerID, blockedID)
	return err
}

// IsBlockedPair reports whether either user has blocked the other. Every interaction rule uses
// this rather than the one-directional row, so a block also stops the blocked person from reaching
// back.
func (s *Store) IsBlockedPair(ctx context.Context, first, second string) (bool, error) {
	var exists int
	err := s.db.QueryRowContext(ctx, `
        SELECT 1 FROM user_blocks
        WHERE (blocker_user_id = ? AND blocked_user_id = ?)
           OR (blocker_user_id = ? AND blocked_user_id = ?)
        LIMIT 1`, first, second, second, first).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// BlockedPairIDs returns every user hidden from this one, in either direction. The nearby list
// loads it once per request instead of asking per candidate.
func (s *Store) BlockedPairIDs(ctx context.Context, userID string) (map[string]struct{}, error) {
	rows, err := s.db.QueryContext(ctx, `
        SELECT blocked_user_id FROM user_blocks WHERE blocker_user_id = ?
        UNION
        SELECT blocker_user_id FROM user_blocks WHERE blocked_user_id = ?`, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	hidden := make(map[string]struct{})
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		hidden[id] = struct{}{}
	}
	return hidden, rows.Err()
}

// ListBlockedUsers returns the people the caller blocked, newest first, so they can be unblocked
// again from settings. It deliberately does not include people who blocked the caller: that would
// tell them something they were not meant to know.
func (s *Store) ListBlockedUsers(ctx context.Context, blockerID string, limit int) ([]BlockedUser, error) {
	rows, err := s.db.QueryContext(ctx, `
        SELECT users.id, users.display_name, users.first_name, users.last_name, users.username,
               users.custom_photo_url, users.telegram_photo_url, user_blocks.created_at
        FROM user_blocks
        JOIN users ON users.id = user_blocks.blocked_user_id
        WHERE user_blocks.blocker_user_id = ?
        ORDER BY user_blocks.created_at DESC
        LIMIT ?`, blockerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	blocked := make([]BlockedUser, 0, 8)
	for rows.Next() {
		var item BlockedUser
		var displayName, firstName, lastName, username, customPhoto, telegramPhoto sql.NullString
		if err := rows.Scan(&item.ID, &displayName, &firstName, &lastName, &username,
			&customPhoto, &telegramPhoto, &item.BlockedAt); err != nil {
			return nil, err
		}
		// Reusing the domain type keeps the display name and avatar rules identical to every other
		// place a person is rendered.
		user := domain.User{
			DisplayName: displayName, FirstName: firstName, LastName: lastName,
			Username: username, CustomPhotoURL: customPhoto, TelegramPhotoURL: telegramPhoto,
		}
		item.DisplayName = user.Name()
		item.Username = username.String
		item.PhotoURL = user.EffectivePhotoURL()
		blocked = append(blocked, item)
	}
	return blocked, rows.Err()
}
