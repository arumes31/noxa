package main

import (
	"encoding/json"
	"net"
	"testing"
	"time"

	"noxa/internal/broadcast"
	"noxa/internal/e2ee/admission"
)

func rosterSnapshot(t *testing.T, clients ...*broadcast.ClientInfo) []byte {
	t.Helper()
	byChannel := map[int64][]*broadcast.ClientInfo{}
	var unassigned []*broadcast.ClientInfo
	for _, c := range clients {
		if c.ChannelID == 0 {
			unassigned = append(unassigned, c)
		} else {
			byChannel[c.ChannelID] = append(byChannel[c.ChannelID], c)
		}
	}
	snap := broadcast.TreeSnapshot{UnassignedClients: unassigned}
	for _, members := range [][]*broadcast.ClientInfo{byChannel[5], byChannel[6]} {
		snap.RootChannels = append(snap.RootChannels, &broadcast.ChannelNode{Clients: members})
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func mediaKeyTo(principal, device, channel string) MediaKeyRequest {
	return MediaKeyRequest{Principal: principal, Device: device, Kind: mediaKeyControlKind,
		Scope: admission.ControlScope{Type: "channel-media", ID: channel}, Data: json.RawMessage(`{}`)}
}

// P3-4: SendMediaKeyForTab refuses a target that is not a visible media
// device of the sender's own current voice channel, as the connection's own
// roster (snapshots and membership events) knows it.
func TestSendMediaKeyRefusesTargetsOutsideTheVoiceRoster(t *testing.T) {
	m := newConnManager(nil) //nolint:staticcheck // a nil Wails context deliberately disables frontend events
	m.clientID, m.deviceID = "c-self", "dev-self"
	m.noteRosterSnapshot(rosterSnapshot(t,
		&broadcast.ClientInfo{ClientID: "c-self", UniqueID: "me", ChannelID: 5, MediaDevice: "dev-self"},
		&broadcast.ClientInfo{ClientID: "c-bob", UniqueID: "bob", ChannelID: 5, MediaDevice: "dev-bob"},
		&broadcast.ClientInfo{ClientID: "c-me2", UniqueID: "me", ChannelID: 5, MediaDevice: "dev-me2"},
		&broadcast.ClientInfo{ClientID: "c-eve", UniqueID: "eve", ChannelID: 6, MediaDevice: "dev-eve"},
		&broadcast.ClientInfo{ClientID: "c-inv", UniqueID: "inv", ChannelID: 5, MediaDevice: "dev-inv", Status: "invisible"},
		&broadcast.ClientInfo{ClientID: "c-lob", UniqueID: "lob", MediaDevice: "dev-lob"},
	))
	for _, ok := range []MediaKeyRequest{mediaKeyTo("bob", "dev-bob", "5"), mediaKeyTo("me", "dev-me2", "5")} {
		if err := m.checkMediaKeyTarget(ok); err != nil {
			t.Fatalf("%s/%s must be allowed: %v", ok.Principal, ok.Device, err)
		}
	}
	for name, bad := range map[string]MediaKeyRequest{
		"other channel member":     mediaKeyTo("eve", "dev-eve", "5"),
		"other channel scope":      mediaKeyTo("eve", "dev-eve", "6"),
		"invisible":                mediaKeyTo("inv", "dev-inv", "5"),
		"not in a channel":         mediaKeyTo("lob", "dev-lob", "5"),
		"device of another member": mediaKeyTo("bob", "dev-me2", "5"),
		"own device":               mediaKeyTo("me", "dev-self", "5"),
		"unknown device":           mediaKeyTo("bob", "dev-x", "5"),
		"alias scope id":           mediaKeyTo("bob", "dev-bob", "05"),
		"wrong scope type": func() MediaKeyRequest {
			r := mediaKeyTo("bob", "dev-bob", "5")
			r.Scope.Type = "group"
			return r
		}(),
	} {
		if err := m.checkMediaKeyTarget(bad); err == nil {
			t.Fatalf("%s must be refused", name)
		}
		if out, err := m.sendMediaKey(bad); err == nil || out.Code != "not_recipient" {
			t.Fatalf("%s: sendMediaKey must refuse before sealing: %+v %v", name, out, err)
		}
	}
	// Membership events update the roster before the page sees them.
	m.noteRosterEvent(`{"type":"user_moved","data":{"client_id":"c-bob","channel_id":6}}`)
	if err := m.checkMediaKeyTarget(mediaKeyTo("bob", "dev-bob", "5")); err == nil {
		t.Fatal("a member who moved away gets no key")
	}
	m.noteRosterEvent(`{"type":"user_moved","data":{"client_id":"c-eve","channel_id":5}}`)
	if err := m.checkMediaKeyTarget(mediaKeyTo("eve", "dev-eve", "5")); err != nil {
		t.Fatal("a member who moved in gets keys", err)
	}
	m.noteRosterEvent(`{"type":"status_changed","data":{"client_id":"c-eve","status":"invisible"}}`)
	if err := m.checkMediaKeyTarget(mediaKeyTo("eve", "dev-eve", "5")); err == nil {
		t.Fatal("a member who went invisible gets no key")
	}
	m.noteRosterEvent(`{"type":"user_left","data":{"client_id":"c-me2"}}`)
	if err := m.checkMediaKeyTarget(mediaKeyTo("me", "dev-me2", "5")); err == nil {
		t.Fatal("a session that left gets no key")
	}
	m.noteRosterEvent(`{"type":"user_joined","data":{"client_id":"c-new","unique_id":"new","channel_id":5}}`)
	if err := m.checkMediaKeyTarget(mediaKeyTo("new", "", "5")); err == nil {
		t.Fatal("a session without a media device gets no key")
	}
	m.noteRosterEvent(`{"type":"media_device_changed","data":{"client_id":"c-new","media_device":"dev-new"}}`)
	if err := m.checkMediaKeyTarget(mediaKeyTo("new", "dev-new", "5")); err != nil {
		t.Fatal("an announced media device gets keys", err)
	}
	// After a disconnect nothing is a recipient until the next snapshot.
	m.mu.Lock()
	m.detachLocked()
	m.mu.Unlock()
	if err := m.checkMediaKeyTarget(mediaKeyTo("new", "dev-new", "5")); err == nil {
		t.Fatal("the roster belongs to its connection")
	}
}

// P3-10: a control queued before a reconnect is never emitted afterwards.
func TestRatchetControlQueueDroppedOnDetach(t *testing.T) {
	m := newConnManager(nil) //nolint:staticcheck // a nil Wails context deliberately disables frontend events
	local, remote := net.Pipe()
	defer func() { _ = remote.Close() }()
	m.mu.Lock()
	m.conn, m.closed = local, false
	m.mu.Unlock()
	entered, release := make(chan string, 4), make(chan struct{})
	m.OnRatchetControl(func(ctl RatchetControl) {
		entered <- ctl.LogicalID
		<-release
	})
	m.dispatchRatchetControl(RatchetControl{LogicalID: "first", Kind: mediaKeyControlKind})
	m.dispatchRatchetControl(RatchetControl{LogicalID: "queued", Kind: mediaKeyControlKind})
	if got := <-entered; got != "first" {
		t.Fatalf("first control: %q", got)
	}
	m.mu.Lock()
	m.detachLocked()
	m.mu.Unlock()
	close(release)
	select {
	case got := <-entered:
		t.Fatalf("a control of the old connection was emitted after detach: %q", got)
	case <-time.After(100 * time.Millisecond):
	}
	// A control dispatched while no connection is attached is dropped too.
	m.dispatchRatchetControl(RatchetControl{LogicalID: "late", Kind: mediaKeyControlKind})
	m.mu.Lock()
	queued := len(m.controlQueue)
	m.mu.Unlock()
	if queued != 0 {
		t.Fatal("no control may be queued without a connection")
	}
}
