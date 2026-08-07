package httpapi

import (
	"errors"
	"net/http"
	"time"

	"aika/internal/database"
	"aika/internal/domain"
)

// blockLimit bounds the block list one response may carry. It is generous for a personal list and
// keeps the endpoint from ever returning an unbounded page.
const blockLimit = 200

type blockedUserResponse struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Username    string `json:"username,omitempty"`
	PhotoURL    string `json:"photo_url,omitempty"`
	BlockedAt   string `json:"blocked_at"`
}

// blocked reports whether two people are hidden from each other. Every interaction between two
// users passes through it, so a block cannot be bypassed by calling an endpoint directly.
func (s *Server) blocked(r *http.Request, viewerID, otherID string) (bool, error) {
	return s.store.IsBlockedPair(r.Context(), viewerID, otherID)
}

func (s *Server) blockUser(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	id, valid := pathID(r)
	if !valid {
		writeError(w, http.StatusBadRequest, "invalid_user_id", localized(user.AppLanguage, "invalid_request"))
		return
	}
	// The target must exist, but a blocked or hidden profile is still blockable: someone may want
	// to block a person they can no longer see.
	if _, err := s.store.GetUserByID(r.Context(), id); errors.Is(err, database.ErrNotFound) {
		writeError(w, http.StatusNotFound, "user_not_found", localized(user.AppLanguage, "recipient_unavailable"))
		return
	} else if err != nil {
		s.internalError(w, r, err)
		return
	}
	if err := s.store.BlockUser(r.Context(), user.ID, id); errors.Is(err, database.ErrSelfBlock) {
		writeError(w, http.StatusConflict, "self_block", localized(user.AppLanguage, "self_block"))
		return
	} else if err != nil {
		s.internalError(w, r, err)
		return
	}
	// A block ends whatever is happening right now, not only what happens next: a call already in
	// progress between the two is hung up immediately.
	s.endCallBetween(user.ID, id)
	s.writeBlocks(w, r, user)
}

func (s *Server) unblockUser(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	id, valid := pathID(r)
	if !valid {
		writeError(w, http.StatusBadRequest, "invalid_user_id", localized(user.AppLanguage, "invalid_request"))
		return
	}
	if err := s.store.UnblockUser(r.Context(), user.ID, id); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.writeBlocks(w, r, user)
}

func (s *Server) listBlockedUsers(w http.ResponseWriter, r *http.Request) {
	s.writeBlocks(w, r, currentUser(r))
}

// writeBlocks answers every block mutation with the resulting list, so the client never has to
// issue a second request to redraw settings.
func (s *Server) writeBlocks(w http.ResponseWriter, r *http.Request, user domain.User) {
	blocked, err := s.store.ListBlockedUsers(r.Context(), user.ID, blockLimit)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	items := make([]blockedUserResponse, 0, len(blocked))
	for _, item := range blocked {
		items = append(items, blockedUserResponse{
			ID: item.ID, DisplayName: item.DisplayName, Username: item.Username,
			PhotoURL: item.PhotoURL, BlockedAt: item.BlockedAt.UTC().Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"blocked": items})
}
