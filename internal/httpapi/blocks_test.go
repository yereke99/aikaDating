package httpapi

import (
	"context"
	"net/http"
	"testing"

	"aika/internal/calls"
	"aika/internal/domain"
)

type blockListResponse struct {
	Blocked []blockedUserResponse `json:"blocked"`
}

func TestBlockHidesThePersonInBothDirections(t *testing.T) {
	environment := testEnvironment(t, 123, 999)
	target := actors(t, environment, 123)
	viewer, err := environment.store.GetUserByTelegramID(context.Background(), 123)
	if err != nil {
		t.Fatal(err)
	}

	// Visible before the block.
	if response := getWithAuth(t, environment.router, "/api/users/"+target.ID); response.Code != http.StatusOK {
		t.Fatalf("profile before block = %d, body = %s", response.Code, response.Body.String())
	}

	blocked := decodeInto[blockListResponse](t, post(t, environment.router, "/api/users/"+target.ID+"/block", ""))
	if len(blocked.Blocked) != 1 || blocked.Blocked[0].ID != target.ID {
		t.Fatalf("block list = %+v", blocked.Blocked)
	}

	// Every way of reaching that person now reports them as unavailable, and none of them says
	// "blocked", so neither side can tell a block from a deleted account.
	for _, probe := range []struct{ method, path string }{
		{http.MethodGet, "/api/users/" + target.ID},
		{http.MethodGet, "/api/users/" + target.ID + "/photos"},
	} {
		response := getWithAuth(t, environment.router, probe.path)
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s after block = %d, body = %s", probe.path, response.Code, response.Body.String())
		}
	}
	for _, path := range []string{"/like", "/message"} {
		response := post(t, environment.router, "/api/users/"+target.ID+path, `{"message":"hello"}`)
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s after block = %d, body = %s", path, response.Code, response.Body.String())
		}
	}
	response := post(t, environment.router, "/api/calls", `{"user_id":"`+target.ID+`"}`)
	if response.Code != http.StatusNotFound {
		t.Fatalf("call after block = %d, body = %s", response.Code, response.Body.String())
	}

	// The rule reads both directions from one row: the blocked person is equally unable to reach
	// back, even though they never blocked anyone.
	hidden, err := environment.store.IsBlockedPair(context.Background(), target.ID, viewer.ID)
	if err != nil || !hidden {
		t.Fatalf("reverse direction = %v, %v", hidden, err)
	}
}

func TestBlockedProfileLeavesTheNearbyList(t *testing.T) {
	environment := testEnvironment(t, 123, 999)
	target := actors(t, environment, 123)
	viewer, err := environment.store.GetUserByTelegramID(context.Background(), 123)
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range []domain.User{viewer, target} {
		if err := environment.store.UpdateLocation(context.Background(), user.ID, 51.1, 71.4); err != nil {
			t.Fatal(err)
		}
	}

	before := decodeInto[struct {
		Users []struct {
			ID string `json:"id"`
		} `json:"users"`
	}](t, getWithAuth(t, environment.router, "/api/users/nearby?radius_km=20"))
	if len(before.Users) != 1 || before.Users[0].ID != target.ID {
		t.Fatalf("nearby before block = %+v", before.Users)
	}

	if response := post(t, environment.router, "/api/users/"+target.ID+"/block", ""); response.Code != http.StatusOK {
		t.Fatalf("block status = %d, body = %s", response.Code, response.Body.String())
	}
	after := decodeInto[struct {
		Users []struct {
			ID string `json:"id"`
		} `json:"users"`
	}](t, getWithAuth(t, environment.router, "/api/users/nearby?radius_km=20"))
	if len(after.Users) != 0 {
		t.Fatalf("nearby after block = %+v", after.Users)
	}
}

func TestUnblockRestoresVisibilityAndBlockingIsIdempotent(t *testing.T) {
	environment := testEnvironment(t, 123, 999)
	target := actors(t, environment, 123)

	// Blocking twice must not create a second row or fail.
	for attempt := 0; attempt < 2; attempt++ {
		response := post(t, environment.router, "/api/users/"+target.ID+"/block", "")
		if response.Code != http.StatusOK {
			t.Fatalf("block attempt %d = %d, body = %s", attempt, response.Code, response.Body.String())
		}
		if list := decodeInto[blockListResponse](t, response); len(list.Blocked) != 1 {
			t.Fatalf("block list after attempt %d = %+v", attempt, list.Blocked)
		}
	}

	request := authorized(t, environment.router, http.MethodDelete, "/api/users/"+target.ID+"/block", "")
	if request.Code != http.StatusOK {
		t.Fatalf("unblock = %d, body = %s", request.Code, request.Body.String())
	}
	if list := decodeInto[blockListResponse](t, request); len(list.Blocked) != 0 {
		t.Fatalf("block list after unblock = %+v", list.Blocked)
	}
	if response := getWithAuth(t, environment.router, "/api/users/"+target.ID); response.Code != http.StatusOK {
		t.Fatalf("profile after unblock = %d, body = %s", response.Code, response.Body.String())
	}
	// Unblocking someone who is not blocked is not an error either.
	if again := authorized(t, environment.router, http.MethodDelete, "/api/users/"+target.ID+"/block", ""); again.Code != http.StatusOK {
		t.Fatalf("repeated unblock = %d", again.Code)
	}
}

func TestBlockingYourselfIsRefused(t *testing.T) {
	environment := testEnvironment(t, 123, 999)
	actors(t, environment, 123)
	viewer, err := environment.store.GetUserByTelegramID(context.Background(), 123)
	if err != nil {
		t.Fatal(err)
	}
	response := post(t, environment.router, "/api/users/"+viewer.ID+"/block", "")
	if response.Code != http.StatusConflict {
		t.Fatalf("self block = %d, body = %s", response.Code, response.Body.String())
	}
	if body := decodeInto[errorEnvelope](t, response); body.Error.Code != "self_block" {
		t.Fatalf("code = %q", body.Error.Code)
	}
}

func TestBlockingDuringACallHangsItUp(t *testing.T) {
	environment := testEnvironment(t, 123, 999)
	target := actors(t, environment, 123)
	viewer, err := environment.store.GetUserByTelegramID(context.Background(), 123)
	if err != nil {
		t.Fatal(err)
	}

	invite := post(t, environment.router, "/api/calls", `{"user_id":"`+target.ID+`"}`)
	call := decodeInto[callResponse](t, invite).Call
	if _, err := environment.calls.Accept(call.ID, target.ID); err != nil {
		t.Fatal(err)
	}

	if response := post(t, environment.router, "/api/users/"+target.ID+"/block", ""); response.Code != http.StatusOK {
		t.Fatalf("block status = %d, body = %s", response.Code, response.Body.String())
	}
	if current := environment.calls.Current(viewer.ID); current != nil {
		t.Fatalf("the call survived the block: %+v", current)
	}
	// The other side is told, so their screen closes instead of freezing on a dead connection.
	events, _, _ := environment.calls.Poll(context.Background(), target.ID, 1, 0)
	if len(events) == 0 || events[len(events)-1].Type != calls.EventEnded {
		t.Fatalf("callee events = %+v", events)
	}
}
