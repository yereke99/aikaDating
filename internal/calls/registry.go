// Package calls holds the signalling state for one-to-one video calls.
//
// Media never passes through this process: the two browsers negotiate a direct RTCPeerConnection
// and exchange audio and video peer-to-peer. The registry only carries the small control messages
// that WebRTC cannot deliver by itself — the invitation, the answer to it, the SDP offer/answer
// pair and the ICE candidates — and it enforces who is allowed to send them.
//
// State is deliberately in memory. A call is a few seconds of coordination, not a record worth
// keeping, so nothing here writes to SQLite; a process restart simply ends the calls that were in
// flight, which is also what the participants' own connections do.
package calls

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

var (
	// ErrNotFound covers both a call that never existed and one the caller may not see. The two
	// are reported identically so a stranger cannot probe for live call IDs.
	ErrNotFound = errors.New("call not found")
	// ErrBusy is returned when the caller is already in a call.
	ErrBusy = errors.New("caller is already in a call")
	// ErrPeerBusy is returned when the person being called is already in a call.
	ErrPeerBusy = errors.New("callee is already in a call")
	// ErrIncomingPending is returned when both users called each other at the same time. The one
	// whose invitation arrived second is told to answer the first instead.
	ErrIncomingPending = errors.New("an incoming call from this user is already ringing")
	// ErrSelfCall is returned when a user tries to call themselves.
	ErrSelfCall = errors.New("cannot call yourself")
	// ErrInvalidState is returned when an action does not apply to the call's current status —
	// accepting a call that already ended, answering one that was never offered.
	ErrInvalidState = errors.New("call is not in a state that allows this action")
)

// Status is the server's view of a call. The client mirrors it but is never trusted for it.
type Status string

const (
	// StatusRinging means the invitation was delivered and nobody has answered yet. No camera or
	// microphone has been opened on either side.
	StatusRinging Status = "ringing"
	// StatusReceiverOpened means the callee opened the Mini App from the call notification and is
	// joining that exact invitation. The original ringing timeout no longer applies; accepting or
	// setup failure decides the outcome from here.
	StatusReceiverOpened Status = "receiver_opened"
	// StatusAccepted means the callee agreed and the two sides are negotiating.
	StatusAccepted Status = "accepted"
	// StatusConnected means at least one side reported an established peer connection.
	StatusConnected Status = "connected"
	// StatusEnded is terminal; Reason says why.
	StatusEnded Status = "ended"
)

// Reasons a call ended. They are sent to both participants so each can show the right message
// rather than a generic disconnect.
const (
	ReasonHangUp       = "hangup"
	ReasonRejected     = "rejected"
	ReasonCancelled    = "cancelled"
	ReasonTimeout      = "timeout"
	ReasonFailed       = "failed"
	ReasonDisconnected = "peer_disconnected"
)

// Event types pushed to a participant's mailbox.
const (
	EventIncoming  = "incoming_call"
	EventOpened    = "receiver_opened"
	EventAccepted  = "call_accepted"
	EventRejected  = "call_rejected"
	EventCancelled = "call_cancelled"
	EventEnded     = "call_ended"
	EventOffer     = "webrtc_offer"
	EventAnswer    = "webrtc_answer"
	EventCandidate = "ice_candidate"
	EventConnected = "call_connected"
)

// Peer is the little bit of profile a call screen needs. It is assembled by the HTTP layer from
// the authenticated user rows, never from a request body.
type Peer struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	PhotoURL    string `json:"photo_url,omitempty"`
}

// Call is one invitation and its outcome.
type Call struct {
	ID         string     `json:"id"`
	Status     Status     `json:"status"`
	Reason     string     `json:"reason,omitempty"`
	Caller     Peer       `json:"caller"`
	Callee     Peer       `json:"callee"`
	CreatedAt  time.Time  `json:"created_at"`
	OpenedAt   *time.Time `json:"receiver_opened_at,omitempty"`
	AcceptedAt *time.Time `json:"accepted_at,omitempty"`
	EndedAt    *time.Time `json:"ended_at,omitempty"`
}

// Role reports whether a user is the caller, and whether they belong to the call at all.
func (c *Call) role(userID string) (isCaller bool, member bool) {
	switch userID {
	case c.Caller.ID:
		return true, true
	case c.Callee.ID:
		return false, true
	default:
		return false, false
	}
}

func (c *Call) peerID(userID string) string {
	if userID == c.Caller.ID {
		return c.Callee.ID
	}
	return c.Caller.ID
}

// Event is one signalling message addressed to a single user.
type Event struct {
	Seq    int64     `json:"seq"`
	Type   string    `json:"type"`
	CallID string    `json:"call_id"`
	At     time.Time `json:"at"`
	// Peer is the other participant, sent with an invitation so the incoming-call screen can
	// render without a second request.
	Peer   *Peer  `json:"peer,omitempty"`
	Reason string `json:"reason,omitempty"`
	// SDP carries an offer or an answer. It is relayed verbatim: the server does not parse or
	// rewrite session descriptions.
	SDP string `json:"sdp,omitempty"`
	// Candidate is one trickled ICE candidate, relayed as the opaque object the browser produced.
	Candidate json.RawMessage `json:"candidate,omitempty"`
}

// Settings are the timeouts the registry enforces.
type Settings struct {
	InviteTimeout   time.Duration
	SetupTimeout    time.Duration
	PresenceTimeout time.Duration
}

// mailboxDepth bounds one user's queue. Signalling for a single call is a handful of messages plus
// its ICE candidates, so this holds several complete calls; a client that falls further behind is
// told to resynchronise rather than served a partial history.
const mailboxDepth = 128

type mailbox struct {
	// firstSeq is the sequence number of events[0]; seq is the last one assigned. Together they
	// let a poll tell "nothing new" from "you fell too far behind".
	firstSeq int64
	seq      int64
	events   []Event
	// lastSeen is presence: when a client last held the channel open. It stays zero for a mailbox
	// that only exists because something was queued for a user who has never polled.
	lastSeen time.Time
	// createdAt is age, used for cleanup, so a mailbox holding an undelivered event is never
	// dropped just because its owner has not polled yet.
	createdAt time.Time
	// notify is closed and replaced whenever an event is appended, which wakes every parked poll
	// at once without keeping a per-waiter list.
	notify chan struct{}
}

// Registry is the process-wide signalling state. Every method is safe for concurrent use.
type Registry struct {
	mu       sync.Mutex
	settings Settings
	now      func() time.Time
	calls    map[string]*Call
	// active maps a user to the call they are currently in, which is what makes "one call at a
	// time" enforceable rather than advisory.
	active  map[string]string
	boxes   map[string]*mailbox
	newID   func() (string, error)
	retired time.Duration
}

func NewRegistry(settings Settings) *Registry {
	return &Registry{
		settings: settings,
		now:      time.Now,
		calls:    make(map[string]*Call),
		active:   make(map[string]string),
		boxes:    make(map[string]*mailbox),
		newID:    newUUID,
		retired:  2 * time.Minute,
	}
}

// Run sweeps expired calls until the context is cancelled.
func (r *Registry) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.Sweep()
		}
	}
}

// Invite creates a ringing call from caller to callee.
//
// A repeated invitation to the same person while the first is still ringing returns that same
// call instead of a second one, so a double tap on the call button cannot produce two invitations.
func (r *Registry) Invite(caller, callee Peer) (*Call, error) {
	if caller.ID == callee.ID {
		return nil, ErrSelfCall
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()

	if existing := r.activeCall(caller.ID); existing != nil {
		isCaller, _ := existing.role(caller.ID)
		switch {
		case isCaller && existing.Callee.ID == callee.ID:
			return copyOf(existing), nil
		case !isCaller && existing.Caller.ID == callee.ID && existing.Status == StatusRinging:
			// Simultaneous dial. The invitation that arrived first stands; this caller is told to
			// answer it rather than opening a second, competing call.
			return copyOf(existing), ErrIncomingPending
		default:
			return nil, ErrBusy
		}
	}
	if r.activeCall(callee.ID) != nil {
		return nil, ErrPeerBusy
	}

	id, err := r.newID()
	if err != nil {
		return nil, err
	}
	call := &Call{ID: id, Status: StatusRinging, Caller: caller, Callee: callee, CreatedAt: now}
	r.calls[id] = call
	r.active[caller.ID] = id
	r.active[callee.ID] = id
	r.push(callee.ID, Event{Type: EventIncoming, CallID: id, Peer: &caller})
	return copyOf(call), nil
}

// Open records that the callee arrived through the existing invitation. It is idempotent while the
// call is already in that joining state, so duplicate Telegram launch callbacks cannot create or
// advance another call.
func (r *Registry) Open(callID, userID string) (*Call, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	call, err := r.member(callID, userID)
	if err != nil {
		return nil, err
	}
	if isCaller, _ := call.role(userID); isCaller {
		return nil, ErrInvalidState
	}
	switch call.Status {
	case StatusReceiverOpened:
		return copyOf(call), nil
	case StatusRinging:
	default:
		return nil, ErrInvalidState
	}
	now := r.now()
	call.Status = StatusReceiverOpened
	call.OpenedAt = &now
	r.push(call.Caller.ID, Event{Type: EventOpened, CallID: call.ID, Peer: &call.Callee})
	return copyOf(call), nil
}

// Accept moves a ringing call to accepted. Only the callee may do it.
func (r *Registry) Accept(callID, userID string) (*Call, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	call, err := r.member(callID, userID)
	if err != nil {
		return nil, err
	}
	if isCaller, _ := call.role(userID); isCaller {
		return nil, ErrInvalidState
	}
	if call.Status != StatusRinging && call.Status != StatusReceiverOpened {
		return nil, ErrInvalidState
	}
	now := r.now()
	call.Status = StatusAccepted
	call.AcceptedAt = &now
	// The caller is the one that creates the offer, so it is the caller that has to be told.
	r.push(call.Caller.ID, Event{Type: EventAccepted, CallID: call.ID, Peer: &call.Callee})
	return copyOf(call), nil
}

// Reject ends a ringing call. Only the callee may do it; the caller uses End, which reports the
// outcome as cancelled instead.
func (r *Registry) Reject(callID, userID string) (*Call, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	call, err := r.member(callID, userID)
	if err != nil {
		return nil, err
	}
	if isCaller, _ := call.role(userID); isCaller {
		return nil, ErrInvalidState
	}
	if call.Status != StatusRinging && call.Status != StatusReceiverOpened {
		return nil, ErrInvalidState
	}
	r.finish(call, ReasonRejected, userID)
	return copyOf(call), nil
}

// End terminates a call from either side. The reason is derived from the state it was in, so a
// client cannot mislabel a hang-up as a rejection.
func (r *Registry) End(callID, userID string) (*Call, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	call, err := r.member(callID, userID)
	if err != nil {
		return nil, err
	}
	if call.Status == StatusEnded {
		return copyOf(call), nil
	}
	isCaller, _ := call.role(userID)
	reason := ReasonHangUp
	if call.Status == StatusRinging || call.Status == StatusReceiverOpened {
		reason = ReasonCancelled
		if !isCaller {
			reason = ReasonRejected
		}
	}
	r.finish(call, reason, userID)
	return copyOf(call), nil
}

// Fail ends a call whose negotiation broke down, so the other side stops waiting immediately
// instead of sitting on a connecting spinner until the setup timeout.
func (r *Registry) Fail(callID, userID string) (*Call, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	call, err := r.member(callID, userID)
	if err != nil {
		return nil, err
	}
	if call.Status == StatusEnded {
		return copyOf(call), nil
	}
	r.finish(call, ReasonFailed, userID)
	return copyOf(call), nil
}

// MarkConnected records that a participant's peer connection came up, which stops the setup
// timeout from ending a call that is already running.
func (r *Registry) MarkConnected(callID, userID string) (*Call, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	call, err := r.member(callID, userID)
	if err != nil {
		return nil, err
	}
	if call.Status == StatusAccepted {
		call.Status = StatusConnected
		r.push(call.peerID(userID), Event{Type: EventConnected, CallID: call.ID})
	}
	return copyOf(call), nil
}

// Signal relays one WebRTC message to the other participant.
//
// The sender is the authenticated user and the recipient is derived from the call, so signalling
// can only ever travel between the two people the server itself paired.
func (r *Registry) Signal(callID, userID string, event Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	call, err := r.member(callID, userID)
	if err != nil {
		return err
	}
	// Offers and answers only make sense once the callee has agreed; relaying them earlier would
	// let a caller start negotiating against someone who never accepted.
	if call.Status != StatusAccepted && call.Status != StatusConnected {
		return ErrInvalidState
	}
	isCaller, _ := call.role(userID)
	switch event.Type {
	case EventOffer:
		if !isCaller {
			return ErrInvalidState
		}
	case EventAnswer:
		if isCaller {
			return ErrInvalidState
		}
	case EventCandidate:
	default:
		return ErrInvalidState
	}
	event.CallID = call.ID
	r.push(call.peerID(userID), event)
	return nil
}

// Current returns the call a user is in, if any. It is what a reopened Mini App uses to decide
// whether it should rejoin a screen or start clean.
func (r *Registry) Current(userID string) *Call {
	r.mu.Lock()
	defer r.mu.Unlock()
	return copyOf(r.activeCall(userID))
}

// Snapshot returns a call by ID without revealing it to clients. It is used by background work that
// already holds server-side call context, such as the Telegram notification retry loop.
func (r *Registry) Snapshot(callID string) *Call {
	r.mu.Lock()
	defer r.mu.Unlock()
	return copyOf(r.calls[callID])
}

// Present reports whether a user's client is currently holding the signalling channel open. It is
// how the server decides that an invitation has to be delivered through Telegram instead of only
// being queued for a Mini App nobody has open.
func (r *Registry) Present(userID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return !r.absent(userID, r.now())
}

// EndBetween hangs up any live call between two people. Blocking someone ends the conversation
// that is happening right now, not just the next one.
func (r *Registry) EndBetween(first, second string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	call := r.activeCall(first)
	if call == nil {
		return
	}
	if _, member := call.role(second); !member {
		return
	}
	r.finish(call, ReasonHangUp, "")
}

// Poll returns the events after `after`, parking for up to `wait` when there are none.
//
// It returns the instant an event is queued rather than on a timer, which is what keeps
// signalling latency down to one round trip.
func (r *Registry) Poll(ctx context.Context, userID string, after int64, wait time.Duration) ([]Event, int64, bool) {
	// Wall clock, not r.now: this is how long a request is held open, not a fact about a call, so
	// it must not move when a test drives the registry's clock.
	deadline := time.Now().Add(wait)
	for {
		r.mu.Lock()
		box := r.mailbox(userID)
		box.lastSeen = r.now()
		events, cursor, reset := box.since(after)
		notify := box.notify
		r.mu.Unlock()

		if reset || len(events) > 0 {
			return events, cursor, reset
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, cursor, false
		}
		timer := time.NewTimer(remaining)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, cursor, false
		case <-timer.C:
			return nil, cursor, false
		case <-notify:
			timer.Stop()
		}
	}
}

// Touch records that a user's client is alive without consuming events. Used by the actions that
// are not polls, so a busy signalling exchange also counts as presence.
func (r *Registry) Touch(userID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mailbox(userID).lastSeen = r.now()
}

// Sweep applies the timeouts. Called once a second by Run, and directly by tests.
func (r *Registry) Sweep() {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	for id, call := range r.calls {
		switch call.Status {
		case StatusRinging:
			if now.Sub(call.CreatedAt) >= r.settings.InviteTimeout {
				r.finish(call, ReasonTimeout, "")
			}
		case StatusReceiverOpened:
			if call.OpenedAt != nil && now.Sub(*call.OpenedAt) >= r.settings.SetupTimeout {
				r.finish(call, ReasonFailed, "")
			}
		case StatusAccepted:
			if call.AcceptedAt != nil && now.Sub(*call.AcceptedAt) >= r.settings.SetupTimeout {
				r.finish(call, ReasonFailed, "")
			}
		case StatusConnected, StatusEnded:
		}
		// A participant whose client stopped polling has closed the Mini App, lost its network or
		// been suspended by the OS. The other side is told rather than left on a frozen frame.
		if call.Status == StatusAccepted || call.Status == StatusConnected {
			if r.absent(call.Caller.ID, now) || r.absent(call.Callee.ID, now) {
				r.finish(call, ReasonDisconnected, "")
			}
		}
		if call.Status == StatusEnded && call.EndedAt != nil && now.Sub(*call.EndedAt) > r.retired {
			delete(r.calls, id)
		}
	}
	// Mailboxes that nobody has touched in a long time are dropped, so the maps cannot grow
	// without bound in a long-running process. Age is measured from the later of the last poll and
	// creation, so an invitation queued for someone who has not opened the app yet survives long
	// enough for them to arrive through the Telegram notification.
	for userID, box := range r.boxes {
		if _, busy := r.active[userID]; busy {
			continue
		}
		idleSince := box.lastSeen
		if idleSince.Before(box.createdAt) {
			idleSince = box.createdAt
		}
		if now.Sub(idleSince) > 10*time.Minute {
			delete(r.boxes, userID)
		}
	}
}

// --- internals -------------------------------------------------------------------------------

// activeCall returns the live call a user is in. Callers must hold the mutex.
func (r *Registry) activeCall(userID string) *Call {
	id, ok := r.active[userID]
	if !ok {
		return nil
	}
	call, ok := r.calls[id]
	if !ok || call.Status == StatusEnded {
		delete(r.active, userID)
		return nil
	}
	return call
}

// member resolves a call the user actually belongs to. A non-participant gets ErrNotFound, not a
// permission error, so call IDs cannot be probed. Callers must hold the mutex.
func (r *Registry) member(callID, userID string) (*Call, error) {
	call, ok := r.calls[callID]
	if !ok {
		return nil, ErrNotFound
	}
	if _, member := call.role(userID); !member {
		return nil, ErrNotFound
	}
	return call, nil
}

// finish marks a call ended and tells whoever did not cause it. Callers must hold the mutex.
func (r *Registry) finish(call *Call, reason, actorID string) {
	if call.Status == StatusEnded {
		return
	}
	now := r.now()
	call.Status = StatusEnded
	call.Reason = reason
	call.EndedAt = &now
	if r.active[call.Caller.ID] == call.ID {
		delete(r.active, call.Caller.ID)
	}
	if r.active[call.Callee.ID] == call.ID {
		delete(r.active, call.Callee.ID)
	}
	eventType := EventEnded
	switch reason {
	case ReasonRejected:
		eventType = EventRejected
	case ReasonCancelled:
		eventType = EventCancelled
	}
	for _, participant := range []string{call.Caller.ID, call.Callee.ID} {
		// The side that pressed the button already knows; it gets the outcome in its HTTP
		// response, so pushing an event to it as well would only race with its own cleanup.
		if participant == actorID {
			continue
		}
		r.push(participant, Event{Type: eventType, CallID: call.ID, Reason: reason})
	}
}

// mailbox returns a user's queue, creating it on first use.
//
// A new mailbox is deliberately left with a zero lastSeen. Queueing an event creates the recipient's
// mailbox, and stamping the clock there would make someone who has never opened the Mini App look
// like an active listener — which is exactly the case that has to be detected so the invitation can
// be delivered through Telegram instead. Only Poll and Touch mark a user as present.
//
// Callers must hold the mutex.
func (r *Registry) mailbox(userID string) *mailbox {
	box, ok := r.boxes[userID]
	if !ok {
		box = &mailbox{createdAt: r.now(), notify: make(chan struct{})}
		r.boxes[userID] = box
	}
	return box
}

// absent reports whether a participant is listening. A user with no mailbox, or one that has never
// polled, has no client on the other end. Callers hold the mutex.
func (r *Registry) absent(userID string, now time.Time) bool {
	box, ok := r.boxes[userID]
	if !ok || box.lastSeen.IsZero() {
		return true
	}
	return now.Sub(box.lastSeen) > r.settings.PresenceTimeout
}

// push appends an event and wakes every parked poll for that user. Callers must hold the mutex.
func (r *Registry) push(userID string, event Event) {
	box := r.mailbox(userID)
	box.seq++
	event.Seq = box.seq
	event.At = r.now()
	if len(box.events) == 0 {
		box.firstSeq = event.Seq
	}
	box.events = append(box.events, event)
	if len(box.events) > mailboxDepth {
		box.events = box.events[len(box.events)-mailboxDepth:]
		box.firstSeq = box.events[0].Seq
	}
	close(box.notify)
	box.notify = make(chan struct{})
}

// since returns everything after a cursor. `reset` means the client's cursor is older than what is
// still buffered, so it must resynchronise its call state instead of replaying a gap.
func (b *mailbox) since(after int64) (events []Event, cursor int64, reset bool) {
	if after > b.seq {
		// A cursor from a previous process, or from a mailbox that was swept. Start over.
		return nil, b.seq, true
	}
	if len(b.events) > 0 && after < b.firstSeq-1 {
		return append([]Event(nil), b.events...), b.seq, true
	}
	for _, event := range b.events {
		if event.Seq > after {
			events = append(events, event)
		}
	}
	return events, b.seq, false
}

func copyOf(call *Call) *Call {
	if call == nil {
		return nil
	}
	clone := *call
	return &clone
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
