package channels

import (
	"testing"

	"noxa/internal/state"
)

func TestPublicPoolTracksOccupancyAndCoalescesWakeups(t *testing.T) {
	sm := state.New(nil)
	m := New(nil, sm, nil)
	sm.AddChannel(&state.Channel{ChannelID: 1, SystemKind: "home"})
	sm.AddChannel(&state.Channel{ChannelID: 2, ParentID: 1, SystemKind: "public"})
	sm.AddChannel(&state.Channel{ChannelID: 3, ParentID: 1, SystemKind: "public"})
	if m.publicPoolNeedsWork() {
		t.Fatal("idle pool needs work")
	}
	sm.AddClient(&state.Client{ClientID: "one"})
	if _, err := m.MoveClient("one", 2); err != nil {
		t.Fatal(err)
	}
	if !m.publicPoolNeedsWork() {
		t.Fatal("occupied spare not detected")
	}
	for range 10 {
		m.requestPublicPool()
	}
	if len(m.publicPoolWake) != 1 {
		t.Fatal("wakeups not coalesced")
	}
	if _, err := m.LeaveClient("one"); err != nil {
		t.Fatal(err)
	}
	if m.publicPoolNeedsWork() {
		t.Fatal("empty pool changed")
	}
	if err := sm.MoveClient("one", 1); err == nil {
		t.Fatal("home accepted a voice membership")
	}
}
