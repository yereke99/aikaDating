package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"aika/internal/calls"
	"aika/internal/database"
	"aika/internal/domain"
)

// callInvitesPerMinute bounds how often one account may start a call. The "one active call per
// user" rule already prevents parallel calls; this stops a rapid invite/cancel loop from being
// used to ring someone repeatedly.
const callInvitesPerMinute = 6

type callConfigResponse struct {
	Enabled bool `json:"enabled"`
	// ICEServers is minted per request so a TURN credential is never part of the shipped bundle.
	ICEServers           []calls.ICEServer `json:"ice_servers"`
	InviteTimeoutSeconds int               `json:"invite_timeout_seconds"`
	EventWaitSeconds     int               `json:"event_wait_seconds"`
	Current              *calls.Call       `json:"current,omitempty"`
	ServerTime           string            `json:"server_time"`
}

type callResponse struct {
	Call       *calls.Call       `json:"call"`
	ICEServers []calls.ICEServer `json:"ice_servers,omitempty"`
	ServerTime string            `json:"server_time"`
}

type callEventsResponse struct {
	Events []calls.Event `json:"events"`
	Cursor int64         `json:"cursor"`
	// Reset tells a client its cursor is older than what the server still buffers, so it must
	// re-read the current call instead of assuming it saw every transition.
	Reset      bool   `json:"reset,omitempty"`
	ServerTime string `json:"server_time"`
}

func peerOf(user domain.User) calls.Peer {
	return calls.Peer{ID: user.ID, DisplayName: user.Name(), PhotoURL: user.EffectivePhotoURL()}
}

// callsAvailable refuses every call route with one consistent error when the feature is switched
// off, so a client can hide the button instead of failing at an arbitrary step.
func (s *Server) callsAvailable(w http.ResponseWriter, user domain.User) bool {
	if s.cfg.Calls.Enabled {
		return true
	}
	writeError(w, http.StatusServiceUnavailable, "calls_disabled", localized(user.AppLanguage, "calls_disabled"))
	return false
}

func (s *Server) callConfig(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	now := time.Now().UTC()
	response := callConfigResponse{
		Enabled:              s.cfg.Calls.Enabled,
		InviteTimeoutSeconds: int(s.cfg.Calls.InviteTimeout.Seconds()),
		EventWaitSeconds:     int(s.cfg.Calls.EventWait.Seconds()),
		ServerTime:           now.Format(time.RFC3339),
	}
	if s.cfg.Calls.Enabled {
		response.ICEServers = calls.ICEServers(s.cfg.Calls, user.ID, now)
		response.Current = s.calls.Current(user.ID)
	}
	writeJSON(w, http.StatusOK, response)
}

// callEvents is the signalling channel: one parked GET per online user.
//
// It is a long poll rather than a socket because the Mini App already authenticates every request
// with a Telegram header and this reuses that untouched. Latency is not the trade-off it sounds
// like: the request is flushed the moment an event is queued, so a signalling message costs one
// round trip, and the wait below is only how long an idle channel stays open before it renews.
func (s *Server) callEvents(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if !s.callsAvailable(w, user) {
		return
	}
	after, err := strconv.ParseInt(queryDefault(r, "after", "0"), 10, 64)
	if err != nil || after < 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", localized(user.AppLanguage, "invalid_request"))
		return
	}

	wait := s.cfg.Calls.EventWait
	// The server's WriteTimeout is shorter than a full wait, so the deadline is extended for this
	// one handler. If the platform cannot do that, the wait is clamped instead of being cut off
	// mid-response.
	if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(wait + 10*time.Second)); err != nil {
		if wait > 10*time.Second {
			wait = 10 * time.Second
		}
	}

	events, cursor, reset := s.calls.Poll(r.Context(), user.ID, after, wait)
	if r.Context().Err() != nil {
		return
	}
	if events == nil {
		events = []calls.Event{}
	}
	// A parked poll must never be cached or revalidated by an intermediary.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, callEventsResponse{
		Events: events, Cursor: cursor, Reset: reset, ServerTime: time.Now().UTC().Format(time.RFC3339),
	})
}

type callInviteRequest struct {
	UserID string `json:"user_id"`
}

// createCall starts an invitation. The caller is the authenticated user and is never read from the
// body; only the person being called is named by the request.
func (s *Server) createCall(w http.ResponseWriter, r *http.Request) {
	caller := currentUser(r)
	if !s.callsAvailable(w, caller) {
		return
	}
	var request callInviteRequest
	if err := decodeJSON(w, r, &request, 1<<10); err != nil || !uuidPattern.MatchString(request.UserID) {
		writeError(w, http.StatusBadRequest, "invalid_request", localized(caller.AppLanguage, "invalid_request"))
		return
	}
	if !caller.IsActive || !caller.IsProfileCompleted {
		writeError(w, http.StatusConflict, "profile_required", localized(caller.AppLanguage, "profile_required"))
		return
	}
	if !s.callLimiter.Allow(caller.ID) {
		writeError(w, http.StatusTooManyRequests, "rate_limit_exceeded", localized(caller.AppLanguage, "rate_limit_exceeded"))
		return
	}
	// The same visibility rule as every other interaction: a hidden, blocked or incomplete profile
	// cannot be called, and the lookup happens server-side so a stale client cannot bypass it.
	callee, err := s.store.GetPublicUserByID(r.Context(), request.UserID)
	if errors.Is(err, database.ErrNotFound) {
		writeError(w, http.StatusNotFound, "user_not_found", localized(caller.AppLanguage, "recipient_unavailable"))
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}

	s.calls.Touch(caller.ID)
	call, err := s.calls.Invite(peerOf(caller), peerOf(callee))
	if errors.Is(err, calls.ErrIncomingPending) {
		// Both dialled at once. The client is handed the invitation that already exists so it can
		// answer that one instead of opening a competing call.
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": errorDetail{Code: "incoming_call_pending", Message: localized(caller.AppLanguage, "incoming_call_pending")},
			"call":  call,
		})
		return
	}
	if err != nil {
		s.writeCallError(w, r, caller, err)
		return
	}
	s.writeCall(w, http.StatusCreated, caller.ID, call)
}

func (s *Server) acceptCall(w http.ResponseWriter, r *http.Request) {
	s.callTransition(w, r, s.calls.Accept)
}

func (s *Server) rejectCall(w http.ResponseWriter, r *http.Request) {
	s.callTransition(w, r, s.calls.Reject)
}

func (s *Server) endCall(w http.ResponseWriter, r *http.Request) {
	s.callTransition(w, r, s.calls.End)
}

// callTransition runs one state change on a call the authenticated user belongs to. Membership is
// resolved inside the registry from the call itself, so a request can only ever affect a call the
// user is actually part of.
func (s *Server) callTransition(w http.ResponseWriter, r *http.Request, action func(callID, userID string) (*calls.Call, error)) {
	user := currentUser(r)
	if !s.callsAvailable(w, user) {
		return
	}
	callID, valid := uuidParam(r, "callID")
	if !valid {
		writeError(w, http.StatusBadRequest, "invalid_request", localized(user.AppLanguage, "invalid_request"))
		return
	}
	s.calls.Touch(user.ID)
	call, err := action(callID, user.ID)
	if err != nil {
		s.writeCallError(w, r, user, err)
		return
	}
	s.writeCall(w, http.StatusOK, user.ID, call)
}

type callStateRequest struct {
	State string `json:"state"`
}

// updateCallState records what the browser's peer connection actually did. `connected` stops the
// setup timeout; `failed` ends the call at once so the other side is not left on a spinner until
// that timeout expires.
func (s *Server) updateCallState(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if !s.callsAvailable(w, user) {
		return
	}
	callID, valid := uuidParam(r, "callID")
	if !valid {
		writeError(w, http.StatusBadRequest, "invalid_request", localized(user.AppLanguage, "invalid_request"))
		return
	}
	var request callStateRequest
	if err := decodeJSON(w, r, &request, 1<<10); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", localized(user.AppLanguage, "invalid_request"))
		return
	}
	s.calls.Touch(user.ID)
	var call *calls.Call
	var err error
	switch request.State {
	case "connected":
		call, err = s.calls.MarkConnected(callID, user.ID)
	case "failed":
		call, err = s.calls.Fail(callID, user.ID)
	default:
		writeError(w, http.StatusBadRequest, "invalid_request", localized(user.AppLanguage, "invalid_request"))
		return
	}
	if err != nil {
		s.writeCallError(w, r, user, err)
		return
	}
	s.writeCall(w, http.StatusOK, user.ID, call)
}

type callSignalRequest struct {
	Type      string          `json:"type"`
	SDP       string          `json:"sdp"`
	Candidate json.RawMessage `json:"candidate"`
}

// maxSignalBytes bounds one signalling message. A session description with several codecs is a few
// kilobytes; an ICE candidate is a few hundred bytes.
const maxSignalBytes = 24 << 10

// signalCall relays one WebRTC message to the other participant. Nothing in the body names a
// recipient: the registry derives it from the call, so a client cannot address a stranger.
func (s *Server) signalCall(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if !s.callsAvailable(w, user) {
		return
	}
	callID, valid := uuidParam(r, "callID")
	if !valid {
		writeError(w, http.StatusBadRequest, "invalid_request", localized(user.AppLanguage, "invalid_request"))
		return
	}
	var request callSignalRequest
	if err := decodeJSON(w, r, &request, maxSignalBytes); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", localized(user.AppLanguage, "invalid_request"))
		return
	}
	event := calls.Event{Type: request.Type}
	switch request.Type {
	case calls.EventOffer, calls.EventAnswer:
		if request.SDP == "" || len(request.SDP) > maxSignalBytes {
			writeError(w, http.StatusBadRequest, "invalid_request", localized(user.AppLanguage, "invalid_request"))
			return
		}
		event.SDP = request.SDP
	case calls.EventCandidate:
		if len(request.Candidate) == 0 || len(request.Candidate) > 4<<10 || !json.Valid(request.Candidate) {
			writeError(w, http.StatusBadRequest, "invalid_request", localized(user.AppLanguage, "invalid_request"))
			return
		}
		event.Candidate = request.Candidate
	default:
		writeError(w, http.StatusBadRequest, "invalid_request", localized(user.AppLanguage, "invalid_request"))
		return
	}
	s.calls.Touch(user.ID)
	if err := s.calls.Signal(callID, user.ID, event); err != nil {
		s.writeCallError(w, r, user, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (s *Server) writeCall(w http.ResponseWriter, status int, userID string, call *calls.Call) {
	now := time.Now().UTC()
	response := callResponse{Call: call, ServerTime: now.Format(time.RFC3339)}
	// The ICE servers travel with any response that starts or continues a live call, so a client
	// never has to hold a credential longer than the call it is about to make.
	if call != nil && call.Status != calls.StatusEnded {
		response.ICEServers = calls.ICEServers(s.cfg.Calls, userID, now)
	}
	writeJSON(w, status, response)
}

func (s *Server) writeCallError(w http.ResponseWriter, r *http.Request, user domain.User, err error) {
	switch {
	case errors.Is(err, calls.ErrNotFound):
		writeError(w, http.StatusNotFound, "call_not_found", localized(user.AppLanguage, "call_not_found"))
	case errors.Is(err, calls.ErrSelfCall):
		writeError(w, http.StatusConflict, "self_call", localized(user.AppLanguage, "self_call"))
	case errors.Is(err, calls.ErrBusy):
		writeError(w, http.StatusConflict, "call_busy", localized(user.AppLanguage, "call_busy"))
	case errors.Is(err, calls.ErrPeerBusy):
		writeError(w, http.StatusConflict, "peer_busy", localized(user.AppLanguage, "peer_busy"))
	case errors.Is(err, calls.ErrInvalidState):
		writeError(w, http.StatusConflict, "call_invalid_state", localized(user.AppLanguage, "call_invalid_state"))
	default:
		s.internalError(w, r, err)
	}
}
