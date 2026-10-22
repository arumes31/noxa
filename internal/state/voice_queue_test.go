package state

import (
	"errors"
	"testing"
)

func TestVoiceQueueFIFOAndMembership(t *testing.T) {
	m := newTestManager(t)
	m.AddChannel(&Channel{ChannelID: 1})
	m.AddChannel(&Channel{ChannelID: 2})
	m.AddChannel(&Channel{ChannelID: 3, TextOnly: true})
	for _, id := range []string{"a", "b", "c"} {
		m.AddClient(&Client{ClientID: id})
		if err := m.JoinChannel(id, 1); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"b", "a", "b", "c"} {
		if _, err := m.UpdateVoiceQueue(1, id, "raise"); err != nil {
			t.Fatal(err)
		}
	}
	a, _ := m.GetClient("a")
	b, _ := m.GetClient("b")
	c, _ := m.GetClient("c")
	if b.HandRaisedOrder >= a.HandRaisedOrder || a.HandRaisedOrder >= c.HandRaisedOrder {
		t.Fatal("duplicate raise changed FIFO")
	}
	m.SetAudioState("b", true, false)
	next, err := m.UpdateVoiceQueue(1, "", "next")
	b, _ = m.GetClient("b")
	if err != nil || next != "b" || b.HandRaisedOrder != 0 || !b.HandCalled || !b.SelfMuted || b.ServerMuted {
		t.Fatalf("call changed controls or FIFO: %+v %v", b, err)
	}
	if err := m.MoveClient("a", 2); err != nil {
		t.Fatal(err)
	}
	a, _ = m.GetClient("a")
	if a.HandRaisedOrder != 0 || a.HandCalled {
		t.Fatal("moved hand survived")
	}
	if _, err := m.UpdateVoiceQueue(1, "a", "raise"); !errors.Is(err, ErrNotInChannel) {
		t.Fatal("accepted stale membership")
	}
	if _, err := m.UpdateVoiceQueue(3, "b", "raise"); !errors.Is(err, ErrTextChannel) {
		t.Fatal("accepted text channel")
	}
	if _, err := m.UpdateVoiceQueue(1, "", "clear"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"b", "c"} {
		member, _ := m.GetClient(id)
		if member.HandRaisedOrder != 0 || member.HandCalled {
			t.Fatal("clear retained a hand/call")
		}
	}
}

func TestVoiceQueueLeavingAndRejoiningLosesPosition(t *testing.T) {
	m := newTestManager(t)
	m.AddChannel(&Channel{ChannelID: 1})
	m.AddClient(&Client{ClientID: "a"})
	if err := m.JoinChannel("a", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := m.UpdateVoiceQueue(1, "a", "raise"); err != nil {
		t.Fatal(err)
	}
	if err := m.LeaveChannel("a"); err != nil {
		t.Fatal(err)
	}
	if err := m.JoinChannel("a", 1); err != nil {
		t.Fatal(err)
	}
	member, _ := m.GetClient("a")
	if member.HandRaisedOrder != 0 || member.HandCalled {
		t.Fatal("rejoin retained position")
	}
}
