package calls

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func testSettings() Settings {
	return Settings{InviteTimeout: 45 * time.Second, SetupTimeout: 60 * time.Second, PresenceTimeout: 45 * time.Second}
}

// testRegistry returns a registry whose clock the test drives, so timeout behaviour is verified
// without any real waiting.
func testRegistry(t *testing.T) (*Registry, *time.Time) {
	t.Helper()
	clock := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	registry := NewRegistry(testSettings())
	registry.now = func() time.Time { return clock }
	return registry, &clock
}

var (
	alice = Peer{ID: "alice", DisplayName: "Alice"}
	bob   = Peer{ID: "bob", DisplayName: "Bob"}
	carol = Peer{ID: "carol", DisplayName: "Carol"}
)

// drain reads whatever is already queued for a user without blocking.
func drain(t *testing.T, registry *Registry, userID string, after int64) ([]Event, int64) {
	t.Helper()
	events, cursor, reset := registry.Poll(context.Background(), userID, after, 0)
	if reset {
		t.Fatalf("unexpected reset for %s", userID)
	}
	return events, cursor
}

func TestInviteRingsOnlyTheCallee(t *testing.T) {
	registry, _ := testRegistry(t)
	call, err := registry.Invite(alice, bob)
	if err != nil {
		t.Fatal(err)
	}
	if call.Status != StatusRinging || call.Caller.ID != "alice" || call.Callee.ID != "bob" {
		t.Fatalf("call = %+v", call)
	}

	events, _ := drain(t, registry, "bob", 0)
	if len(events) != 1 || events[0].Type != EventIncoming || events[0].CallID != call.ID {
		t.Fatalf("bob events = %+v", events)
	}
	if events[0].Peer == nil || events[0].Peer.ID != "alice" {
		t.Fatalf("invitation must name the caller, got %+v", events[0].Peer)
	}
	// The caller is not told about their own invitation; they already have it in the response.
	if events, _ := drain(t, registry, "alice", 0); len(events) != 0 {
		t.Fatalf("alice events = %+v", events)
	}
	// A stranger must not see anything at all.
	if events, _ := drain(t, registry, "carol", 0); len(events) != 0 {
		t.Fatalf("carol events = %+v", events)
	}
}

func TestRepeatedInviteReturnsTheSameCall(t *testing.T) {
	registry, _ := testRegistry(t)
	first, err := registry.Invite(alice, bob)
	if err != nil {
		t.Fatal(err)
	}
	second, err := registry.Invite(alice, bob)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("double tap created a second call: %s and %s", first.ID, second.ID)
	}
	if events, _ := drain(t, registry, "bob", 0); len(events) != 1 {
		t.Fatalf("bob was rung twice: %+v", events)
	}
}

func TestConcurrentCallsAreRefused(t *testing.T) {
	registry, _ := testRegistry(t)
	if _, err := registry.Invite(alice, bob); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Invite(carol, bob); !errors.Is(err, ErrPeerBusy) {
		t.Fatalf("calling a busy user = %v, want ErrPeerBusy", err)
	}
	if _, err := registry.Invite(alice, carol); !errors.Is(err, ErrBusy) {
		t.Fatalf("calling while busy = %v, want ErrBusy", err)
	}
	if _, err := registry.Invite(alice, alice); !errors.Is(err, ErrSelfCall) {
		t.Fatalf("self call = %v, want ErrSelfCall", err)
	}
}

func TestSimultaneousDialKeepsTheFirstInvitation(t *testing.T) {
	registry, _ := testRegistry(t)
	first, err := registry.Invite(alice, bob)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := registry.Invite(bob, alice)
	if !errors.Is(err, ErrIncomingPending) {
		t.Fatalf("simultaneous dial = %v, want ErrIncomingPending", err)
	}
	if pending == nil || pending.ID != first.ID {
		t.Fatalf("the caller must be handed the existing invitation, got %+v", pending)
	}
}

func TestOnlyTheCalleeMayAccept(t *testing.T) {
	registry, _ := testRegistry(t)
	call, err := registry.Invite(alice, bob)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Accept(call.ID, "alice"); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("caller accepting own call = %v, want ErrInvalidState", err)
	}
	if _, err := registry.Accept(call.ID, "carol"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stranger accepting = %v, want ErrNotFound", err)
	}
	accepted, err := registry.Accept(call.ID, "bob")
	if err != nil || accepted.Status != StatusAccepted || accepted.AcceptedAt == nil {
		t.Fatalf("accept = %+v, %v", accepted, err)
	}
	events, _ := drain(t, registry, "alice", 0)
	if len(events) != 1 || events[0].Type != EventAccepted {
		t.Fatalf("alice events = %+v", events)
	}
	// Accepting twice must not re-notify or reopen anything.
	if _, err := registry.Accept(call.ID, "bob"); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("second accept = %v, want ErrInvalidState", err)
	}
}

func TestRejectEndsTheCallAndTellsTheCaller(t *testing.T) {
	registry, _ := testRegistry(t)
	call, _ := registry.Invite(alice, bob)
	ended, err := registry.Reject(call.ID, "bob")
	if err != nil || ended.Status != StatusEnded || ended.Reason != ReasonRejected {
		t.Fatalf("reject = %+v, %v", ended, err)
	}
	events, _ := drain(t, registry, "alice", 0)
	if len(events) != 1 || events[0].Type != EventRejected || events[0].Reason != ReasonRejected {
		t.Fatalf("alice events = %+v", events)
	}
	// Both users are free again.
	if registry.Current("alice") != nil || registry.Current("bob") != nil {
		t.Fatal("a rejected call must release both participants")
	}
	if _, err := registry.Invite(alice, bob); err != nil {
		t.Fatalf("re-inviting after a rejection failed: %v", err)
	}
}

func TestEndReportsCancelledForTheCallerAndHangUpAfterAccept(t *testing.T) {
	registry, _ := testRegistry(t)
	call, _ := registry.Invite(alice, bob)
	ended, err := registry.End(call.ID, "alice")
	if err != nil || ended.Reason != ReasonCancelled {
		t.Fatalf("caller ending a ringing call = %+v, %v", ended, err)
	}

	second, _ := registry.Invite(alice, bob)
	if _, err := registry.Accept(second.ID, "bob"); err != nil {
		t.Fatal(err)
	}
	ended, err = registry.End(second.ID, "alice")
	if err != nil || ended.Reason != ReasonHangUp {
		t.Fatalf("ending an accepted call = %+v, %v", ended, err)
	}
	// Ending twice is a no-op rather than an error, because both a button and a cleanup path call it.
	if again, err := registry.End(second.ID, "bob"); err != nil || again.Reason != ReasonHangUp {
		t.Fatalf("second end = %+v, %v", again, err)
	}
}

func TestSignalingReachesOnlyTheOtherParticipant(t *testing.T) {
	registry, _ := testRegistry(t)
	call, _ := registry.Invite(alice, bob)

	// Nothing may be negotiated before the callee agreed.
	if err := registry.Signal(call.ID, "alice", Event{Type: EventOffer, SDP: "v=0"}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("offer before accept = %v, want ErrInvalidState", err)
	}
	if _, err := registry.Accept(call.ID, "bob"); err != nil {
		t.Fatal(err)
	}
	_, bobCursor := drain(t, registry, "bob", 0)
	_, aliceCursor := drain(t, registry, "alice", 0)

	if err := registry.Signal(call.ID, "alice", Event{Type: EventOffer, SDP: "v=0 offer"}); err != nil {
		t.Fatal(err)
	}
	events, bobCursor := drain(t, registry, "bob", bobCursor)
	if len(events) != 1 || events[0].Type != EventOffer || events[0].SDP != "v=0 offer" {
		t.Fatalf("bob events = %+v", events)
	}
	// The sender never receives its own signalling back.
	if events, _ := drain(t, registry, "alice", aliceCursor); len(events) != 0 {
		t.Fatalf("alice received her own offer: %+v", events)
	}

	if err := registry.Signal(call.ID, "bob", Event{Type: EventAnswer, SDP: "v=0 answer"}); err != nil {
		t.Fatal(err)
	}
	candidate := json.RawMessage(`{"candidate":"candidate:1 1 udp"}`)
	if err := registry.Signal(call.ID, "bob", Event{Type: EventCandidate, Candidate: candidate}); err != nil {
		t.Fatal(err)
	}
	events, _ = drain(t, registry, "alice", aliceCursor)
	if len(events) != 2 || events[0].Type != EventAnswer || events[1].Type != EventCandidate {
		t.Fatalf("alice events = %+v", events)
	}

	// Role and membership are both enforced.
	if err := registry.Signal(call.ID, "bob", Event{Type: EventOffer, SDP: "v=0"}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("callee sending an offer = %v, want ErrInvalidState", err)
	}
	if err := registry.Signal(call.ID, "alice", Event{Type: EventAnswer, SDP: "v=0"}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("caller sending an answer = %v, want ErrInvalidState", err)
	}
	if err := registry.Signal(call.ID, "carol", Event{Type: EventCandidate, Candidate: candidate}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stranger signalling = %v, want ErrNotFound", err)
	}
	if events, _ := drain(t, registry, "carol", 0); len(events) != 0 {
		t.Fatalf("carol received signalling for a call she is not in: %+v", events)
	}
	_ = bobCursor
}

func TestUnknownAndForeignCallsAreIndistinguishable(t *testing.T) {
	registry, _ := testRegistry(t)
	call, _ := registry.Invite(alice, bob)
	for name, action := range map[string]func(string, string) (*Call, error){
		"accept": registry.Accept,
		"reject": registry.Reject,
		"end":    registry.End,
		"fail":   registry.Fail,
	} {
		if _, err := action(call.ID, "carol"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s by a stranger = %v, want ErrNotFound", name, err)
		}
		if _, err := action("00000000-0000-4000-8000-000000000000", "alice"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s of a missing call = %v, want ErrNotFound", name, err)
		}
	}
}

func TestUnansweredInvitationTimesOut(t *testing.T) {
	registry, clock := testRegistry(t)
	call, _ := registry.Invite(alice, bob)
	_, bobCursor := drain(t, registry, "bob", 0)

	*clock = clock.Add(44 * time.Second)
	registry.Sweep()
	if current := registry.Current("alice"); current == nil {
		t.Fatal("the invitation expired early")
	}

	*clock = clock.Add(2 * time.Second)
	registry.Sweep()
	if registry.Current("alice") != nil || registry.Current("bob") != nil {
		t.Fatal("the invitation did not expire")
	}
	// Both sides learn why, because neither of them caused it.
	for user, cursor := range map[string]int64{"alice": 0, "bob": bobCursor} {
		events, _ := drain(t, registry, user, cursor)
		if len(events) != 1 || events[0].Type != EventEnded || events[0].Reason != ReasonTimeout {
			t.Fatalf("%s events = %+v", user, events)
		}
	}
	if stored, _ := registry.calls[call.ID]; stored.Reason != ReasonTimeout {
		t.Fatalf("call reason = %q", stored.Reason)
	}
}

func TestAcceptedCallEndsWhenAParticipantStopsPolling(t *testing.T) {
	registry, clock := testRegistry(t)
	call, _ := registry.Invite(alice, bob)
	if _, err := registry.Accept(call.ID, "bob"); err != nil {
		t.Fatal(err)
	}
	registry.Touch("alice")
	registry.Touch("bob")

	// Alice keeps polling; Bob's client goes away.
	for step := 0; step < 5; step++ {
		*clock = clock.Add(10 * time.Second)
		registry.Touch("alice")
		registry.Sweep()
	}
	if registry.Current("alice") != nil {
		if current := registry.Current("alice"); current != nil {
			t.Fatalf("call survived a departed participant: %+v", current)
		}
	}
	events, _ := drain(t, registry, "alice", 0)
	last := events[len(events)-1]
	if last.Type != EventEnded || last.Reason != ReasonDisconnected {
		t.Fatalf("alice's last event = %+v", last)
	}
}

func TestSetupTimeoutEndsACallThatNeverConnects(t *testing.T) {
	registry, clock := testRegistry(t)
	call, _ := registry.Invite(alice, bob)
	if _, err := registry.Accept(call.ID, "bob"); err != nil {
		t.Fatal(err)
	}
	// Both stay present, so only the negotiation deadline can end this call.
	for step := 0; step < 7; step++ {
		*clock = clock.Add(10 * time.Second)
		registry.Touch("alice")
		registry.Touch("bob")
		registry.Sweep()
	}
	if registry.calls[call.ID].Reason != ReasonFailed {
		t.Fatalf("reason = %q, want %q", registry.calls[call.ID].Reason, ReasonFailed)
	}
}

func TestConnectedCallSurvivesTheSetupTimeout(t *testing.T) {
	registry, clock := testRegistry(t)
	call, _ := registry.Invite(alice, bob)
	if _, err := registry.Accept(call.ID, "bob"); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.MarkConnected(call.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	for step := 0; step < 10; step++ {
		*clock = clock.Add(10 * time.Second)
		registry.Touch("alice")
		registry.Touch("bob")
		registry.Sweep()
	}
	if current := registry.Current("alice"); current == nil || current.Status != StatusConnected {
		t.Fatalf("a connected call was swept: %+v", current)
	}
}

func TestPollReturnsAsSoonAsAnEventIsQueued(t *testing.T) {
	registry := NewRegistry(testSettings())
	done := make(chan []Event, 1)
	go func() {
		events, _, _ := registry.Poll(context.Background(), "bob", 0, 2*time.Second)
		done <- events
	}()
	// Give the poll a moment to park before the invitation is created.
	time.Sleep(20 * time.Millisecond)
	started := time.Now()
	if _, err := registry.Invite(alice, bob); err != nil {
		t.Fatal(err)
	}
	events := <-done
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("parked poll took %s to notice an event", elapsed)
	}
	if len(events) != 1 || events[0].Type != EventIncoming {
		t.Fatalf("events = %+v", events)
	}
}

func TestPollReturnsEmptyAfterItsWait(t *testing.T) {
	registry := NewRegistry(testSettings())
	events, cursor, reset := registry.Poll(context.Background(), "bob", 0, 30*time.Millisecond)
	if len(events) != 0 || cursor != 0 || reset {
		t.Fatalf("idle poll = %+v, cursor %d, reset %v", events, cursor, reset)
	}
}

func TestPollStopsWhenTheClientDisconnects(t *testing.T) {
	registry := NewRegistry(testSettings())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		registry.Poll(ctx, "bob", 0, time.Minute)
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("a cancelled poll kept its goroutine parked")
	}
}

func TestCursorAheadOfTheServerAsksForAResync(t *testing.T) {
	registry := NewRegistry(testSettings())
	if _, err := registry.Invite(alice, bob); err != nil {
		t.Fatal(err)
	}
	// A cursor from an earlier process, or from a mailbox that was swept.
	_, cursor, reset := registry.Poll(context.Background(), "bob", 99, 0)
	if !reset || cursor != 1 {
		t.Fatalf("stale cursor = reset %v, cursor %d", reset, cursor)
	}
}

func TestMailboxKeepsOnlyItsMostRecentEvents(t *testing.T) {
	registry := NewRegistry(testSettings())
	registry.mu.Lock()
	for index := 0; index < mailboxDepth+20; index++ {
		registry.push("bob", Event{Type: EventCandidate, CallID: "x"})
	}
	registry.mu.Unlock()
	events, cursor, reset := registry.Poll(context.Background(), "bob", 0, 0)
	if !reset {
		t.Fatal("a client that fell behind the buffer must be told to resynchronise")
	}
	if len(events) != mailboxDepth || cursor != int64(mailboxDepth+20) {
		t.Fatalf("buffered %d events, cursor %d", len(events), cursor)
	}
}
