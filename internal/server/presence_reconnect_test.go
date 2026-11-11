package server

import (
	"slices"
	"testing"
	"time"

	"noxa/internal/broadcast"
	"noxa/internal/netproto"
)

func TestDepartureReasonClassifiesCloses(t *testing.T) {
	reason := func(read netproto.ControlCloseReason, cause netproto.ControlCloseReason, goodbyeFeature, saidGoodbye, replaced bool) string {
		c := &Client{announcesGoodbye: goodbyeFeature}
		c.readClose.Store(&read)
		if cause != "" {
			c.markCloseCause(cause)
		}
		c.saidGoodbye.Store(saidGoodbye)
		c.replaced.Store(replaced)
		return c.departureReason()
	}
	lost := netproto.DepartureConnectionLost
	cases := []struct {
		name                    string
		read, cause             netproto.ControlCloseReason
		feature, goodbye, moved bool
		want                    string
	}{
		{"legacy clean close is a leave", netproto.ControlCloseEOF, "", false, false, false, ""},
		{"clean close without promised goodbye is lost", netproto.ControlCloseEOF, "", true, false, false, lost},
		{"goodbye then close is a leave", netproto.ControlCloseEOF, "", true, true, false, ""},
		{"read timeout is lost", netproto.ControlCloseReadTimeout, "", false, false, false, lost},
		{"reset is lost", netproto.ControlCloseReadError, "", false, false, false, lost},
		{"server inactivity is lost", netproto.ControlCloseLocal, netproto.ControlCloseServerInactivity, false, false, false, lost},
		{"write failure is lost", netproto.ControlCloseLocal, "", false, false, false, lost},
		{"kick is not a lost connection", netproto.ControlCloseLocal, netproto.ControlCloseKicked, true, false, false, ""},
		{"shutdown is not a lost connection", netproto.ControlCloseLocal, netproto.ControlCloseShutdown, true, false, false, ""},
		{"replaced session stays silent", netproto.ControlCloseReadTimeout, "", true, false, true, netproto.DepartureReplaced},
	}
	for _, tc := range cases {
		if got := reason(tc.read, tc.cause, tc.feature, tc.goodbye, tc.moved); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestPresenceTrackerReconnectWindow(t *testing.T) {
	var p presenceTracker
	now := time.Unix(1_000_000, 0)
	p.noteDeparture("old", "alice|dev", netproto.DepartureConnectionLost, 7, false, now)
	if !p.noteJoin("new", "alice|dev", false, now.Add(30*time.Second)) {
		t.Fatal("a login of the lost device within the window must be a reconnect")
	}
	if p.noteJoin("newer", "alice|dev", false, now.Add(40*time.Second)) {
		t.Fatal("a reconnect consumes the lost record")
	}
	p.noteDeparture("a", "bob|dev", netproto.DepartureConnectionLost, 7, false, now)
	if p.noteJoin("b", "bob|dev", false, now.Add(presenceReconnectWindow+time.Second)) {
		t.Fatal("a login after the window is an ordinary join")
	}
	p.noteDeparture("c", "carol|dev", netproto.DepartureConnectionLost, 7, false, now)
	p.noteDeparture("d", "carol|dev", "", 7, false, now.Add(time.Second))
	if p.noteJoin("e", "carol|dev", false, now.Add(2*time.Second)) {
		t.Fatal("a deliberate leave clears the lost record")
	}
	if !p.noteJoin("f", "dave|dev", true, now) {
		t.Fatal("a login while the old session is registered is a reconnect")
	}
}

func TestPresenceTrackerDecoratesVisibleDepartures(t *testing.T) {
	var p presenceTracker
	now := time.Unix(1_000_000, 0)
	p.noteDeparture("lost", "a|1", netproto.DepartureConnectionLost, 7, false, now)
	p.noteDeparture("hidden", "b|1", netproto.DepartureConnectionLost, 7, true, now)
	p.noteDeparture("private", "c|1", netproto.DepartureConnectionLost, 9, false, now)
	p.noteDeparture("left", "d|1", "", 7, false, now)
	p.noteJoin("back", "a|1", false, now)
	p.noteJoin("gone", "e|1", true, now)

	snapshot := &broadcast.TreeSnapshot{RootChannels: []*broadcast.ChannelNode{{
		Clients: []*broadcast.ClientInfo{{ClientID: "back"}},
	}}}
	p.decorate(snapshot, func(channelID int64) bool { return channelID != 9 }, now.Add(time.Second))
	if len(snapshot.Departures) != 1 || snapshot.Departures["lost"] != netproto.DepartureConnectionLost {
		t.Fatalf("departures = %v, want only the visible lost session", snapshot.Departures)
	}
	if !slices.Equal(snapshot.Reconnected, []string{"back"}) {
		t.Fatalf("reconnected = %v, want only present members", snapshot.Reconnected)
	}

	later := &broadcast.TreeSnapshot{}
	p.decorate(later, func(int64) bool { return true }, now.Add(presenceDepartureWindow+time.Second))
	if len(later.Departures) != 0 {
		t.Fatalf("expired departures still reported: %v", later.Departures)
	}
}

func TestPresenceTrackerStaysBounded(t *testing.T) {
	var p presenceTracker
	now := time.Unix(1_000_000, 0)
	for i := 0; i < presenceTrackerMax+50; i++ {
		id := string(rune('a'+i%26)) + time.Duration(i).String()
		p.noteDeparture(id, id, netproto.DepartureConnectionLost, 1, false, now)
	}
	if len(p.departures) > presenceTrackerMax || len(p.lost) > presenceTrackerMax {
		t.Fatalf("tracker grew past its bound: %d departures, %d lost", len(p.departures), len(p.lost))
	}
}
