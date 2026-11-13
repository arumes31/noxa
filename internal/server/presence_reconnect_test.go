package server

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"go.uber.org/zap"

	"noxa/internal/broadcast"
	"noxa/internal/config"
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
	updating := &Client{announcesGoodbye: true}
	updating.updating.Store(true)
	if got := updating.departureReason(); got != "" {
		t.Fatalf("an update flag without a goodbye = %q, want the close classification", got)
	}
	updating.saidGoodbye.Store(true)
	if got := updating.departureReason(); got != netproto.DepartureUpdating {
		t.Fatalf("goodbye for an update = %q, want %q", got, netproto.DepartureUpdating)
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
	p.decorate(snapshot, func(channelID int64) bool { return channelID != 9 }, "", now.Add(time.Second))
	if len(snapshot.Departures) != 1 || snapshot.Departures["lost"] != netproto.DepartureConnectionLost {
		t.Fatalf("departures = %v, want only the visible lost session", snapshot.Departures)
	}
	if !slices.Equal(snapshot.Reconnected, []string{"back"}) {
		t.Fatalf("reconnected = %v, want only present members", snapshot.Reconnected)
	}

	later := &broadcast.TreeSnapshot{}
	p.decorate(later, func(int64) bool { return true }, "", now.Add(presenceDepartureWindow+time.Second))
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

func TestMarkReplacedSessionsSparesLiveParallelSessions(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	session := func(id string, lastActive time.Time) *Client {
		return &Client{ID: id, UniqueID: "alice", deviceID: "phone", lastActive: lastActive}
	}
	stale := session("stale", now.Add(-presenceStaleAfter-time.Second))
	live := session("live", now.Add(-5*time.Second))
	fresh := session("fresh", now)
	s := &TCPServer{clients: map[string]*Client{stale.ID: stale, live.ID: live, fresh.ID: fresh}}
	if !s.markReplacedSessions(fresh, now) {
		t.Fatal("a silent older session of the same device must count as replaced")
	}
	if !stale.replaced.Load() || live.replaced.Load() {
		t.Fatalf("replaced: stale=%v live=%v; a live parallel tab must stay untouched", stale.replaced.Load(), live.replaced.Load())
	}
	s = &TCPServer{clients: map[string]*Client{live.ID: live, fresh.ID: fresh}}
	if s.markReplacedSessions(fresh, now) {
		t.Fatal("a second live tab is not a reconnect")
	}
}

func TestGoodbyeReasonIsOptional(t *testing.T) {
	for payload, want := range map[string]bool{
		``:                    false,
		`{}`:                  false,
		`{"reason":""}`:       false,
		`{"reason":"other"}`:  false,
		`not json`:            false,
		`{"reason":"update"}`: true,
	} {
		if got := goodbyeIsUpdate([]byte(payload)); got != want {
			t.Errorf("goodbyeIsUpdate(%q) = %v, want %v", payload, got, want)
		}
	}
}

func updatingSnapshot(clients ...*broadcast.ClientInfo) *broadcast.TreeSnapshot {
	node := &broadcast.ChannelNode{Clients: clients}
	node.ChannelID = 7
	return &broadcast.TreeSnapshot{RootChannels: []*broadcast.ChannelNode{node}}
}

func TestPresenceTrackerKeepsUpdatingPlaceAndReturnsQuietly(t *testing.T) {
	var p presenceTracker
	now := time.Unix(1_000_000, 0)
	p.noteDeparture("old", "alice|dev", netproto.DepartureUpdating, 7, false, now)
	p.noteUpdating("alice|dev", broadcast.ClientInfo{ClientID: "old", UniqueID: "alice", Nickname: "Alice", ChannelID: 7}, false, now)

	during := updatingSnapshot()
	p.decorate(during, func(int64) bool { return true }, "bob|dev", now.Add(50*time.Second))
	if clients := during.RootChannels[0].Clients; len(clients) != 1 || clients[0].ClientID != "old" || !clients[0].Updating {
		t.Fatalf("kept place = %+v, want old marked updating", clients)
	}
	if len(during.Departures) != 0 {
		t.Fatalf("an updating member must not be reported as departed: %v", during.Departures)
	}
	own := updatingSnapshot()
	p.decorate(own, func(int64) bool { return true }, "alice|dev", now.Add(time.Second))
	if len(own.RootChannels[0].Clients) != 0 {
		t.Fatal("the returning device must not see its own kept place")
	}
	hidden := updatingSnapshot()
	p.decorate(hidden, func(int64) bool { return false }, "bob|dev", now.Add(time.Second))
	if len(hidden.RootChannels[0].Clients) != 0 {
		t.Fatal("a kept place in an invisible channel leaked")
	}

	if !p.noteReturn("new", "alice|dev", now.Add(59*time.Second)) {
		t.Fatal("a login within 60 s must be a quiet return")
	}
	if p.noteJoin("x", "alice|dev", false, now.Add(59*time.Second)) {
		t.Fatal("an updating departure must not also count as a lost connection")
	}
	after := updatingSnapshot(&broadcast.ClientInfo{ClientID: "new", ChannelID: 7})
	p.decorate(after, func(int64) bool { return true }, "bob|dev", now.Add(60*time.Second))
	if len(after.RootChannels[0].Clients) != 1 || !slices.Equal(after.Returned, []string{"new"}) || len(after.Reconnected) != 0 {
		t.Fatalf("after return: clients %d, returned %v, reconnected %v", len(after.RootChannels[0].Clients), after.Returned, after.Reconnected)
	}
	if after.Departures["old"] != netproto.DepartureReplaced {
		t.Fatalf("the kept place must vanish silently, departures = %v", after.Departures)
	}
	if p.expireUpdating("alice|dev", "old") {
		t.Fatal("a returned member must not expire into a leave")
	}
}

func TestPresenceTrackerUpdatingExpiresIntoLeave(t *testing.T) {
	var p presenceTracker
	now := time.Unix(1_000_000, 0)
	p.noteUpdating("alice|dev", broadcast.ClientInfo{ClientID: "old", ChannelID: 7}, false, now)
	if p.noteReturn("late", "alice|dev", now.Add(presenceUpdateWindow+time.Second)) {
		t.Fatal("a login after 60 s is not a quiet return")
	}
	late := updatingSnapshot()
	p.decorate(late, func(int64) bool { return true }, "", now.Add(presenceUpdateWindow+time.Second))
	if len(late.RootChannels[0].Clients) != 0 {
		t.Fatal("the place is kept past 60 s")
	}
	if p.expireUpdating("alice|dev", "other") {
		t.Fatal("expiry of another session must not end this kept place")
	}
	if !p.expireUpdating("alice|dev", "old") || p.expireUpdating("alice|dev", "old") {
		t.Fatal("expiry must end the kept place exactly once")
	}
	gone := updatingSnapshot()
	p.decorate(gone, func(int64) bool { return true }, "", now.Add(presenceUpdateWindow))
	if len(gone.RootChannels[0].Clients) != 0 || len(gone.Departures) != 0 {
		t.Fatalf("an expired place must read as a plain leave: %+v %v", gone.RootChannels[0].Clients, gone.Departures)
	}
}

func TestExpiredUpdatingDepartureBroadcastsLeave(t *testing.T) {
	bc := broadcast.New(zap.NewNop(), nil)
	s := New(&config.Config{}, zap.NewNop(), &Deps{Broadcast: bc})
	events, err := bc.Register("observer")
	if err != nil {
		t.Fatal(err)
	}
	s.presence.noteUpdating("alice|dev", broadcast.ClientInfo{ClientID: "old", ChannelID: 7}, false, time.Now())
	s.expireUpdatingDeparture("alice|dev", "old", "alice", "Alice")
	select {
	case raw := <-events:
		var env struct {
			Type string    `json:"type"`
			Data userEvent `json:"data"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatal(err)
		}
		if env.Type != eventUserLeft || env.Data.ClientID != "old" || env.Data.Reason != "" {
			t.Fatalf("expiry event = %+v, want a plain user_left", env)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no leave after the update window")
	}
	s.expireUpdatingDeparture("alice|dev", "old", "alice", "Alice")
	select {
	case raw := <-events:
		t.Fatalf("a second expiry broadcast %s", raw)
	case <-time.After(50 * time.Millisecond):
	}
}
