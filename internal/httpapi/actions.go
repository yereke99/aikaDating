package httpapi

import (
	"errors"
	"math"
	"net/http"
	"strconv"
	"time"

	"aika/internal/database"
	"aika/internal/telegram"
	"aika/internal/users"
)

type likeRequest struct {
	Message *string `json:"message"`
}

type actionResponse struct {
	Success           bool   `json:"success"`
	Message           string `json:"message"`
	Action            string `json:"action"`
	NextAllowedAt     string `json:"next_allowed_at"`
	RetryAfterSeconds int64  `json:"retry_after_seconds"`
	ServerTime        string `json:"server_time"`
}

type cooldownState struct {
	NextAllowedAt     string `json:"next_allowed_at"`
	RetryAfterSeconds int64  `json:"retry_after_seconds"`
}

type cooldownsResponse struct {
	ServerTime string                   `json:"server_time"`
	Cooldowns  map[string]cooldownState `json:"cooldowns"`
	WindowSecs int64                    `json:"window_seconds"`
}

// retryAfter is the whole number of seconds a client must wait, never negative and rounded up so a
// countdown never shows zero while the server would still refuse.
func retryAfter(next, now time.Time) int64 {
	remaining := next.Sub(now)
	if remaining <= 0 {
		return 0
	}
	return int64(math.Ceil(remaining.Seconds()))
}

// likeUser handles POST /users/{id}/like. A body with a message performs the message action, which
// carries its own independent timer; without one it is a plain like.
func (s *Server) likeUser(w http.ResponseWriter, r *http.Request) {
	s.performAction(w, r, false)
}

// messageUser handles POST /users/{id}/message and always requires message text.
func (s *Server) messageUser(w http.ResponseWriter, r *http.Request) {
	s.performAction(w, r, true)
}

func (s *Server) performAction(w http.ResponseWriter, r *http.Request, requireMessage bool) {
	sender := currentUser(r)
	id, valid := pathID(r)
	if !valid {
		writeError(w, http.StatusBadRequest, "invalid_user_id", localized(sender.AppLanguage, "invalid_request"))
		return
	}
	var request likeRequest
	if err := decodeJSON(w, r, &request, 2<<10); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", localized(sender.AppLanguage, "invalid_request"))
		return
	}
	if requireMessage && request.Message == nil {
		writeError(w, http.StatusUnprocessableEntity, "message_required", localized(sender.AppLanguage, "message_required"))
		return
	}
	message, err := users.ValidateLike(sender.ID, id, request.Message)
	if errors.Is(err, users.ErrSelfLike) {
		writeError(w, http.StatusConflict, "self_like", localized(sender.AppLanguage, "self_like"))
		return
	}
	if errors.Is(err, users.ErrMessageRequired) {
		writeError(w, http.StatusUnprocessableEntity, "message_required", localized(sender.AppLanguage, "message_required"))
		return
	}
	if errors.Is(err, users.ErrMessageTooLong) {
		writeError(w, http.StatusUnprocessableEntity, "message_too_long", localized(sender.AppLanguage, "message_too_long"))
		return
	}
	if !sender.IsActive || !sender.IsProfileCompleted {
		writeError(w, http.StatusConflict, "profile_required", localized(sender.AppLanguage, "profile_required"))
		return
	}
	if !s.limiter.Allow(sender.ID) {
		writeError(w, http.StatusTooManyRequests, "rate_limit_exceeded", localized(sender.AppLanguage, "rate_limit_exceeded"))
		return
	}
	recipient, err := s.store.GetUserByID(r.Context(), id)
	if errors.Is(err, database.ErrNotFound) || (err == nil && (recipient.IsBlocked || !recipient.IsActive || !recipient.IsProfileCompleted)) {
		writeError(w, http.StatusNotFound, "recipient_unavailable", localized(sender.AppLanguage, "recipient_unavailable"))
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}

	action := database.ActionLike
	if request.Message != nil {
		action = database.ActionMessage
	}
	now := time.Now().UTC()
	// The claim is taken before the notification is sent, so a duplicate request that arrives while
	// the first one is still in flight is refused rather than delivered twice.
	cooldown, granted, err := s.store.ClaimAction(r.Context(), sender.ID, id, action, now, s.cfg.ActionCooldown)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !granted {
		writeCooldownError(w, action, cooldown.NextAllowedAt, now, localized(sender.AppLanguage, action+"_cooldown_active"))
		return
	}

	if err := s.telegram.SendLike(r.Context(), recipient, sender, message); err != nil {
		// Delivery failed, so the action never happened: hand the claim back rather than making the
		// sender wait out a full window for something the recipient never received.
		if releaseErr := s.store.ReleaseAction(r.Context(), sender.ID, id, action, cooldown.NextAllowedAt); releaseErr != nil {
			s.internalError(w, r, releaseErr)
			return
		}
		if errors.Is(err, telegram.ErrRecipientUnavailable) {
			writeError(w, http.StatusConflict, "recipient_unavailable", localized(sender.AppLanguage, "recipient_unavailable"))
			return
		}
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, actionResponse{
		Success: true, Action: action, Message: localized(sender.AppLanguage, action+"_sent"),
		NextAllowedAt: cooldown.NextAllowedAt.Format(time.RFC3339), RetryAfterSeconds: retryAfter(cooldown.NextAllowedAt, now),
		ServerTime: now.Format(time.RFC3339),
	})
}

// userCooldowns lets a client resynchronise its countdowns after a reload or an app resume without
// attempting the action first.
func (s *Server) userCooldowns(w http.ResponseWriter, r *http.Request) {
	viewer := currentUser(r)
	id, valid := pathID(r)
	if !valid {
		writeError(w, http.StatusBadRequest, "invalid_user_id", localized(viewer.AppLanguage, "invalid_request"))
		return
	}
	now := time.Now().UTC()
	active, err := s.store.CooldownsForTarget(r.Context(), viewer.ID, id, now)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, cooldownsResponse{
		ServerTime: now.Format(time.RFC3339),
		Cooldowns:  cooldownStates(active, now),
		WindowSecs: int64(s.cfg.ActionCooldown.Seconds()),
	})
}

func cooldownStates(active map[string]time.Time, now time.Time) map[string]cooldownState {
	states := make(map[string]cooldownState, len(active))
	for action, next := range active {
		states[action] = cooldownState{NextAllowedAt: next.Format(time.RFC3339), RetryAfterSeconds: retryAfter(next, now)}
	}
	return states
}

// writeCooldownError reports an active timer in both the project's error envelope and the flat
// cooldown shape, so a client can read either without a second request.
func writeCooldownError(w http.ResponseWriter, action string, next, now time.Time, message string) {
	code := action + "_cooldown_active"
	seconds := retryAfter(next, now)
	var body struct {
		Error             errorDetail `json:"error"`
		Success           bool        `json:"success"`
		Code              string      `json:"code"`
		Action            string      `json:"action"`
		NextAllowedAt     string      `json:"next_allowed_at"`
		RetryAfterSeconds int64       `json:"retry_after_seconds"`
		ServerTime        string      `json:"server_time"`
	}
	body.Error = errorDetail{Code: code, Message: message, NextAllowedAt: next.Format(time.RFC3339), RetryAfterSeconds: seconds}
	body.Code = code
	body.Action = action
	body.NextAllowedAt = next.Format(time.RFC3339)
	body.RetryAfterSeconds = seconds
	body.ServerTime = now.Format(time.RFC3339)
	w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
	writeJSON(w, http.StatusTooManyRequests, body)
}
