package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aika/internal/calls"
	"aika/internal/config"
	"aika/internal/domain"
)

func getWithAuth(t *testing.T, router http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.Header.Set("Authorization", "dev")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func decodeInto[T any](t *testing.T, response *httptest.ResponseRecorder) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
		t.Fatalf("decode %s: %v", response.Body.String(), err)
	}
	return value
}

func TestCallInviteNamesOnlyTheCallee(t *testing.T) {
	environment := testEnvironment(t, 123, 999)
	target := actors(t, environment, 123)
	caller, err := environment.store.GetUserByTelegramID(context.Background(), 123)
	if err != nil {
		t.Fatal(err)
	}

	response := post(t, environment.router, "/api/calls", `{"user_id":"`+target.ID+`"}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("invite status = %d, body = %s", response.Code, response.Body.String())
	}
	body := decodeInto[callResponse](t, response)
	// The caller is taken from the authenticated session, never from the request.
	if body.Call.Caller.ID != caller.ID || body.Call.Callee.ID != target.ID {
		t.Fatalf("call = %+v", body.Call)
	}
	if body.Call.Status != calls.StatusRinging {
		t.Fatalf("status = %q", body.Call.Status)
	}
	if len(body.ICEServers) == 0 {
		t.Fatal("a live call must carry its ICE servers")
	}

	// The callee's mailbox holds the invitation; the caller's does not.
	events, _, _ := environment.calls.Poll(context.Background(), target.ID, 0, 0)
	if len(events) != 1 || events[0].Type != calls.EventIncoming {
		t.Fatalf("callee events = %+v", events)
	}
	if events, _, _ := environment.calls.Poll(context.Background(), caller.ID, 0, 0); len(events) != 0 {
		t.Fatalf("caller events = %+v", events)
	}
}

func TestCallInviteRejectsBadTargets(t *testing.T) {
	environment := testEnvironment(t, 123, 999)
	actors(t, environment, 123)
	caller, err := environment.store.GetUserByTelegramID(context.Background(), 123)
	if err != nil {
		t.Fatal(err)
	}

	cases := map[string]struct {
		body   string
		status int
		code   string
	}{
		"not a uuid":        {`{"user_id":"nobody"}`, http.StatusBadRequest, "invalid_request"},
		"unknown user":      {`{"user_id":"11111111-1111-4111-8111-111111111111"}`, http.StatusNotFound, "user_not_found"},
		"self":              {`{"user_id":"` + caller.ID + `"}`, http.StatusConflict, "self_call"},
		"unexpected fields": {`{"user_id":"11111111-1111-4111-8111-111111111111","caller_id":"x"}`, http.StatusBadRequest, "invalid_request"},
	}
	for name, expected := range cases {
		t.Run(name, func(t *testing.T) {
			response := post(t, environment.router, "/api/calls", expected.body)
			if response.Code != expected.status {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, expected.status, response.Body.String())
			}
			body := decodeInto[errorEnvelope](t, response)
			if body.Error.Code != expected.code {
				t.Fatalf("code = %q, want %q", body.Error.Code, expected.code)
			}
		})
	}
}

func TestCallActionsOfAnotherPairAreNotFound(t *testing.T) {
	environment := testEnvironment(t, 123, 999)
	actors(t, environment, 123)
	// A call between two people the authenticated user has nothing to do with.
	foreign, err := environment.calls.Invite(calls.Peer{ID: "someone", DisplayName: "Someone"}, calls.Peer{ID: "another", DisplayName: "Another"})
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/accept", "/reject", "/end"} {
		response := post(t, environment.router, "/api/calls/"+foreign.ID+path, "")
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404; body = %s", path, response.Code, response.Body.String())
		}
	}
	response := post(t, environment.router, "/api/calls/"+foreign.ID+"/signal", `{"type":"ice_candidate","candidate":{"candidate":"x"}}`)
	if response.Code != http.StatusNotFound {
		t.Fatalf("signal status = %d, want 404; body = %s", response.Code, response.Body.String())
	}
	// Nothing leaked into either stranger's mailbox.
	for _, userID := range []string{"someone", "another"} {
		events, _, _ := environment.calls.Poll(context.Background(), userID, 1, 0)
		if len(events) != 0 {
			t.Fatalf("%s received foreign signalling: %+v", userID, events)
		}
	}
}

func TestCallSignalValidatesItsPayload(t *testing.T) {
	environment := testEnvironment(t, 123, 999)
	target := actors(t, environment, 123)

	invite := post(t, environment.router, "/api/calls", `{"user_id":"`+target.ID+`"}`)
	call := decodeInto[callResponse](t, invite).Call
	if _, err := environment.calls.Accept(call.ID, target.ID); err != nil {
		t.Fatal(err)
	}

	for name, body := range map[string]string{
		"unknown type":      `{"type":"whatever","sdp":"v=0"}`,
		"offer without sdp": `{"type":"webrtc_offer","sdp":""}`,
		"candidate missing": `{"type":"ice_candidate"}`,
	} {
		t.Run(name, func(t *testing.T) {
			response := post(t, environment.router, "/api/calls/"+call.ID+"/signal", body)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body = %s", response.Code, response.Body.String())
			}
		})
	}

	response := post(t, environment.router, "/api/calls/"+call.ID+"/signal", `{"type":"webrtc_offer","sdp":"v=0 real offer"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("valid offer status = %d, body = %s", response.Code, response.Body.String())
	}
	events, _, _ := environment.calls.Poll(context.Background(), target.ID, 1, 0)
	if len(events) != 1 || events[0].Type != calls.EventOffer || events[0].SDP != "v=0 real offer" {
		t.Fatalf("callee events = %+v", events)
	}
}

func TestCallEventsPollAnswersWithACursor(t *testing.T) {
	environment := testEnvironment(t, 123, 999)
	target := actors(t, environment, 123)
	caller, err := environment.store.GetUserByTelegramID(context.Background(), 123)
	if err != nil {
		t.Fatal(err)
	}

	// An idle channel answers empty after its wait, which the test configuration keeps short.
	idle := decodeInto[callEventsResponse](t, getWithAuth(t, environment.router, "/api/calls/events?after=0"))
	if len(idle.Events) != 0 || idle.Cursor != 0 {
		t.Fatalf("idle poll = %+v", idle)
	}

	if _, err := environment.calls.Invite(calls.Peer{ID: target.ID}, calls.Peer{ID: caller.ID, DisplayName: caller.Name()}); err != nil {
		t.Fatal(err)
	}
	queued := decodeInto[callEventsResponse](t, getWithAuth(t, environment.router, "/api/calls/events?after=0"))
	if len(queued.Events) != 1 || queued.Events[0].Type != calls.EventIncoming || queued.Cursor != 1 {
		t.Fatalf("queued poll = %+v", queued)
	}
	// The same cursor must not replay the event.
	again := decodeInto[callEventsResponse](t, getWithAuth(t, environment.router, "/api/calls/events?after=1"))
	if len(again.Events) != 0 {
		t.Fatalf("event replayed: %+v", again)
	}
}

func TestCallConfigMintsEphemeralTURNCredentials(t *testing.T) {
	environment := testEnvironment(t, 123, 999, func(cfg *config.Config) {
		cfg.Calls.TURNURLs = []string{"turn:turn.example.org:3478"}
		cfg.Calls.TURNSecret = "shared-secret"
		cfg.Calls.TURNCredentialTTL = time.Hour
	})
	authenticateDevUser(t, environment.router)

	body := decodeInto[callConfigResponse](t, getWithAuth(t, environment.router, "/api/calls/config"))
	if !body.Enabled || len(body.ICEServers) != 2 {
		t.Fatalf("config = %+v", body)
	}
	turn := body.ICEServers[1]
	if turn.Credential == "" || turn.Credential == "shared-secret" {
		t.Fatalf("the shared secret must never be sent to a client: %+v", turn)
	}
	expiry, _, found := strings.Cut(turn.Username, ":")
	if !found || expiry == "" {
		t.Fatalf("username = %q, want an expiring <unix>:<id> pair", turn.Username)
	}
}

func TestCallsCanBeDisabled(t *testing.T) {
	environment := testEnvironment(t, 123, 999, func(cfg *config.Config) { cfg.Calls.Enabled = false })
	target := actors(t, environment, 123)

	body := decodeInto[callConfigResponse](t, getWithAuth(t, environment.router, "/api/calls/config"))
	if body.Enabled || len(body.ICEServers) != 0 {
		t.Fatalf("config = %+v", body)
	}
	response := post(t, environment.router, "/api/calls", `{"user_id":"`+target.ID+`"}`)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("invite status = %d, want 503; body = %s", response.Code, response.Body.String())
	}
}

func TestCallsRequireAnEnabledProfile(t *testing.T) {
	environment := testEnvironment(t, 123, 999)
	target := actors(t, environment, 123)
	caller, err := environment.store.GetUserByTelegramID(context.Background(), 123)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := environment.store.UpdateProfile(context.Background(), caller.ID, domain.ProfileUpdate{
		DisplayName: caller.Name(), Gender: "other", BirthDate: time.Date(1996, 2, 3, 0, 0, 0, 0, time.UTC),
		Purpose: "chat", AppLanguage: "en", IsActive: false, Completed: true,
	}); err != nil {
		t.Fatal(err)
	}
	response := post(t, environment.router, "/api/calls", `{"user_id":"`+target.ID+`"}`)
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body = %s", response.Code, response.Body.String())
	}
}

// The camera and microphone are both needed for a call, and the ICE servers have to be reachable.
// Both used to be blocked by the response headers, so they are asserted here.
func TestSecurityHeadersAllowCallMedia(t *testing.T) {
	environment := testEnvironment(t, 123, 999, func(cfg *config.Config) {
		cfg.Calls.TURNURLs = []string{"turn:turn.example.org:3478"}
		cfg.Calls.TURNSecret = "shared-secret"
	})
	response := getWithAuth(t, environment.router, "/health")

	permissions := response.Header().Get("Permissions-Policy")
	if !strings.Contains(permissions, "camera=(self)") || !strings.Contains(permissions, "microphone=(self)") {
		t.Fatalf("Permissions-Policy = %q", permissions)
	}
	policy := response.Header().Get("Content-Security-Policy")
	// Schemes, not full URLs: a browser discards `stun:host:port` as an invalid CSP source and the
	// directive silently loses its effect.
	for _, scheme := range []string{"connect-src 'self' stun: turn:"} {
		if !strings.Contains(policy, scheme) {
			t.Fatalf("Content-Security-Policy connect-src = %s, want %q", policy, scheme)
		}
	}
	if strings.Contains(policy, "turn.example.org") {
		t.Fatalf("a full ICE URL is not a valid CSP source: %s", policy)
	}
	if !strings.Contains(policy, "frame-ancestors https://web.telegram.org") {
		t.Fatalf("the Telegram frame policy regressed: %s", policy)
	}
}

// A Mini App cannot be woken up, so an invitation to someone who is not holding the signalling
// channel open has to arrive through the bot instead — otherwise it would just time out unanswered.
func TestAbsentCalleeIsRungThroughTelegram(t *testing.T) {
	environment := testEnvironment(t, 123, 999)
	target := actors(t, environment, 123)

	invite := post(t, environment.router, "/api/calls", `{"user_id":"`+target.ID+`"}`)
	if invite.Code != http.StatusCreated {
		t.Fatalf("invite status = %d, body = %s", invite.Code, invite.Body.String())
	}
	call := decodeInto[callResponse](t, invite).Call
	rings := environment.telegram.ringsFor(target.ID)
	if len(rings) != 1 || rings[0] != target.ID+":"+call.ID {
		t.Fatalf("rings = %+v, want one for call %s", rings, call.ID)
	}
}

func TestPresentCalleeIsNotMessaged(t *testing.T) {
	environment := testEnvironment(t, 123, 999)
	target := actors(t, environment, 123)
	// A client that has polled recently is holding the channel open and already receives the
	// invitation over it.
	environment.calls.Touch(target.ID)

	if invite := post(t, environment.router, "/api/calls", `{"user_id":"`+target.ID+`"}`); invite.Code != http.StatusCreated {
		t.Fatalf("invite status = %d, body = %s", invite.Code, invite.Body.String())
	}
	if rings := environment.telegram.ringsFor(target.ID); len(rings) != 0 {
		t.Fatalf("a listening callee was messaged as well: %+v", rings)
	}
}
