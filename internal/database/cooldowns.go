package database

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Action types tracked by the cooldown table. Each pair (actor, target, action) has its own timer,
// so a like never blocks a message and a message never blocks a like.
const (
	ActionLike    = "like"
	ActionMessage = "message"
)

// Cooldown is the server's view of one timer. Times are always server time; the client is expected
// to render a countdown against them rather than trusting its own clock. They are persisted as Unix
// milliseconds, which keeps every comparison an integer one and stays exact for short windows.
type Cooldown struct {
	ActionType    string
	LastActionAt  time.Time
	NextAllowedAt time.Time
}

// ClaimAction atomically reserves the right to perform one action. It returns granted=false with
// the active deadline when the previous action is still inside its window.
//
// The insert-or-conditional-update is a single statement: two concurrent requests cannot both see
// an expired row and both write a fresh one, because the second one's WHERE clause is evaluated
// against the first one's committed value.
func (s *Store) ClaimAction(ctx context.Context, actorID, targetID, actionType string, now time.Time, window time.Duration) (Cooldown, bool, error) {
	id, err := newUUID()
	if err != nil {
		return Cooldown{}, false, err
	}
	nowUnix := now.UTC().UnixMilli()
	nextUnix := now.UTC().Add(window).UnixMilli()

	var claimed Cooldown
	granted := false
	err = s.inTransaction(ctx, func(tx *sql.Tx) error {
		var storedLast, storedNext int64
		err := tx.QueryRowContext(ctx, `
            INSERT INTO user_action_cooldowns (id, actor_user_id, target_user_id, action_type, last_action_at, next_allowed_at)
            VALUES (?, ?, ?, ?, ?, ?)
            ON CONFLICT (actor_user_id, target_user_id, action_type) DO UPDATE SET
                last_action_at = excluded.last_action_at,
                next_allowed_at = excluded.next_allowed_at,
                updated_at = CURRENT_TIMESTAMP
            WHERE user_action_cooldowns.next_allowed_at <= ?
            RETURNING last_action_at, next_allowed_at`,
			id, actorID, targetID, actionType, nowUnix, nextUnix, nowUnix,
		).Scan(&storedLast, &storedNext)
		if errors.Is(err, sql.ErrNoRows) {
			// The conflicting row is still active: report its deadline instead of a generic failure.
			err = tx.QueryRowContext(ctx, `
                SELECT last_action_at, next_allowed_at FROM user_action_cooldowns
                WHERE actor_user_id = ? AND target_user_id = ? AND action_type = ?`,
				actorID, targetID, actionType).Scan(&storedLast, &storedNext)
		} else if err == nil {
			granted = true
		}
		if err != nil {
			return err
		}
		claimed.LastActionAt = time.UnixMilli(storedLast).UTC()
		claimed.NextAllowedAt = time.UnixMilli(storedNext).UTC()
		return nil
	})
	if err != nil {
		return Cooldown{}, false, err
	}
	claimed.ActionType = actionType
	return claimed, granted, nil
}

// ReleaseAction gives a claim back when the work it guarded could not be completed — for example a
// Telegram delivery that failed because the recipient blocked the bot. The deadline must match the
// one that was claimed, so a newer claim made in the meantime is never discarded.
func (s *Store) ReleaseAction(ctx context.Context, actorID, targetID, actionType string, claimed time.Time) error {
	_, err := s.db.ExecContext(ctx, `
        DELETE FROM user_action_cooldowns
        WHERE actor_user_id = ? AND target_user_id = ? AND action_type = ? AND next_allowed_at = ?`,
		actorID, targetID, actionType, claimed.UTC().UnixMilli())
	return err
}

// ActiveCooldowns returns every timer of one actor that has not yet expired, keyed by target user
// and action type, so a list response can carry its cooldown state without a query per row.
func (s *Store) ActiveCooldowns(ctx context.Context, actorID string, now time.Time) (map[string]map[string]time.Time, error) {
	rows, err := s.db.QueryContext(ctx, `
        SELECT target_user_id, action_type, next_allowed_at
        FROM user_action_cooldowns
        WHERE actor_user_id = ? AND next_allowed_at > ?`, actorID, now.UTC().UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]map[string]time.Time)
	for rows.Next() {
		var targetID, actionType string
		var nextUnix int64
		if err := rows.Scan(&targetID, &actionType, &nextUnix); err != nil {
			return nil, err
		}
		if result[targetID] == nil {
			result[targetID] = make(map[string]time.Time, 2)
		}
		result[targetID][actionType] = time.UnixMilli(nextUnix).UTC()
	}
	return result, rows.Err()
}

// CooldownsForTarget returns the active timers between one actor and one target.
func (s *Store) CooldownsForTarget(ctx context.Context, actorID, targetID string, now time.Time) (map[string]time.Time, error) {
	rows, err := s.db.QueryContext(ctx, `
        SELECT action_type, next_allowed_at
        FROM user_action_cooldowns
        WHERE actor_user_id = ? AND target_user_id = ? AND next_allowed_at > ?`,
		actorID, targetID, now.UTC().UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]time.Time, 2)
	for rows.Next() {
		var actionType string
		var nextUnix int64
		if err := rows.Scan(&actionType, &nextUnix); err != nil {
			return nil, err
		}
		result[actionType] = time.UnixMilli(nextUnix).UTC()
	}
	return result, rows.Err()
}
