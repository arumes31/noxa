package netproto

import "testing"

func testCall() CallSession {
	return CallSession{ID: "call", Caller: "alice", CreatedAt: 100, RingUntil: 130, Revision: 1, Participants: []CallParticipant{{UniqueID: "alice", ClientID: "a", State: "accepted"}, {UniqueID: "bob", ClientID: "b", State: "ringing"}, {UniqueID: "carol", ClientID: "c", State: "ringing"}}}
}

func TestPrivateCallAcceptanceAndIndependentLeave(t *testing.T) {
	c := testCall()
	for _, uid := range []string{"bob", "carol"} {
		var err error
		c, err = c.Change(uid, "accept", 110)
		if err != nil {
			t.Fatal(err)
		}
	}
	c, err := c.Change("alice", "leave", 120)
	if err != nil || c.EndedAt != 0 {
		t.Fatal("owner leaving terminated two remaining participants")
	}
	c, err = c.Change("bob", "leave", 121)
	if err != nil || c.EndedAt != 121 {
		t.Fatal("last peer leave did not end call")
	}
	if _, err := c.Change("alice", "accept", 122); err == nil {
		t.Fatal("ended call revived")
	}
}

func TestPrivateCallRingingTimeoutCancelAndUnauthorizedInput(t *testing.T) {
	c := testCall()
	if _, err := c.Change("outsider", "accept", 110); err == nil {
		t.Fatal("outsider joined")
	}
	if _, err := c.Change("bob", "cancel", 110); err == nil {
		t.Fatal("callee cancelled caller")
	}
	if _, err := c.Change("bob", "accept", 130); err == nil {
		t.Fatal("expired invitation accepted")
	}
	next, changed := c.Expire(130)
	if !changed || next.EndedAt != 130 || next.Participants[1].State != "missed" {
		t.Fatal("timeout not recorded")
	}
	if c.Participants[1].State != "ringing" {
		t.Fatal("timeout mutated original")
	}
	cancelled, err := c.Change("alice", "cancel", 110)
	if err != nil || cancelled.EndedAt != 110 {
		t.Fatal("cancel did not end ringing")
	}
	accepted, err := c.Change("bob", "accept", 110)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := accepted.Change("alice", "cancel", 111); err == nil {
		t.Fatal("caller cancelled an accepted call")
	}
	active, changed := accepted.Expire(130)
	if !changed || active.EndedAt != 0 || active.Participants[2].State != "missed" {
		t.Fatal("timeout ended active peers")
	}
}
