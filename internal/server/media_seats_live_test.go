package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"

	"noxa/internal/authorization"
	"noxa/internal/channels"
	"noxa/internal/netproto"
	"noxa/internal/state"
)

// assertEncryptedVoiceInvariant checks, for channel ch, that every live
// session with a media device (a voice peer) standing in ch holds a seat
// there, and that the channel never has more than MediaE2EEMaxVoice seats or
// distinct live media devices.
func assertEncryptedVoiceInvariant(t *testing.T, env *testEnv, ch int64) {
	t.Helper()
	devices := map[string]bool{}
	for _, m := range env.state.ChannelMembers(ch) {
		if m.MediaDevice == "" {
			continue
		}
		devices[m.UniqueID+"\x00"+m.MediaDevice] = true
		if env.srv.mediaSessionLive(m.ClientID) && env.srv.mediaSeats.seat(m.ClientID) != ch {
			t.Fatalf("live session %s with a voice peer in channel %d holds no seat there (seat %d)", m.ClientID, ch, env.srv.mediaSeats.seat(m.ClientID))
		}
	}
	if seats := env.srv.mediaSeats.others(ch, ""); seats > MediaE2EEMaxVoice || len(devices) > MediaE2EEMaxVoice {
		t.Fatalf("channel %d: %d seats, %d distinct media devices (cap %d)", ch, seats, len(devices), MediaE2EEMaxVoice)
	}
}

// twoSameDeviceSessions dials two live sessions of one account and device,
// both in channel 1 with an accepted encrypted offer.
func twoSameDeviceSessions(t *testing.T, env *testEnv) (c1 net.Conn, s1 string, c2 net.Conn, s2 string) {
	t.Helper()
	env.voice.answerSDP = "server-answer"
	c1, s1 = dialAuthed(t, env.addr, "user-uid")
	t.Cleanup(func() { _ = c1.Close() })
	c2, s2 = dialAuthed(t, env.addr, "user-uid")
	t.Cleanup(func() { _ = c2.Close() })
	placeVoiceTestClients(t, env, 1, s1, s2)
	for _, id := range []string{s1, s2} {
		cl, _ := env.srv.clientByID(id)
		cl.setDeviceID("dev-1")
	}
	for _, c := range []net.Conn{c1, c2} {
		send(t, c, netproto.MsgWebRTCOffer, netproto.WebRTCOffer{SDP: "offer", MediaE2EE: netproto.MediaE2EEVersion})
		readOfType(t, c, netproto.MsgWebRTCAnswer)
	}
	waitFor(t, "media devices announced", func() bool {
		a, _ := env.state.GetClient(s1)
		b, _ := env.state.GetClient(s2)
		return a.MediaDevice == "dev-1" && b.MediaDevice == "dev-1"
	})
	return c1, s1, c2, s2
}

// Fix4 review P2-1 (repro R1): a live second session of the same device holds
// a seat of its own; it never unseats the first, so neither can walk into a
// full channel, and the invariant holds on every path.
func TestMediaE2EESameDeviceLiveSessionsHoldSeparateSeats(t *testing.T) {
	env := startMediaE2EEEnv(t, true)
	c1, s1, _, s2 := twoSameDeviceSessions(t, env)
	if env.srv.mediaSeats.seat(s1) != 1 || env.srv.mediaSeats.seat(s2) != 1 {
		t.Fatalf("seats: s1=%d s2=%d, want both 1", env.srv.mediaSeats.seat(s1), env.srv.mediaSeats.seat(s2))
	}
	assertEncryptedVoiceInvariant(t, env, 1)

	env.state.AddChannel(testChannel(2))
	fillEncryptedChannel(t, env, 2)
	for _, id := range []string{s1, s2} {
		if err := env.srv.moveClientWithCause(context.Background(), id, 2, id, false); err != errMediaE2EEChannelFull {
			t.Fatalf("move of %s into a full channel: %v", id, err)
		}
		if voiceJoined(env, id, 2) {
			t.Fatalf("%s routed into the full channel", id)
		}
	}
	assertEncryptedVoiceInvariant(t, env, 2)

	// With exactly one seat free, only one of the two sessions gets it.
	env.state.AddChannel(testChannel(3))
	fillEncryptedChannel(t, env, 3)
	env.srv.mediaSeats.release("member-3-0")
	env.state.RemoveClient("member-3-0")
	if err := env.srv.moveClientWithCause(context.Background(), s1, 3, s1, false); err != nil {
		t.Fatalf("move into the free seat: %v", err)
	}
	if err := env.srv.moveClientWithCause(context.Background(), s2, 3, s2, false); err != errMediaE2EEChannelFull {
		t.Fatalf("second same-device session took a 65th seat: %v", err)
	}
	assertEncryptedVoiceInvariant(t, env, 3)
	assertEncryptedVoiceInvariant(t, env, 1)

	// s1 renegotiates in its new channel (it holds the seat).
	send(t, c1, netproto.MsgWebRTCOffer, netproto.WebRTCOffer{SDP: "renegotiate", MediaE2EE: netproto.MediaE2EEVersion})
	readOfType(t, c1, netproto.MsgWebRTCAnswer)
}

// The second session leaving does not leave the device uncounted while the
// first still has voice in the channel.
func TestMediaE2EELeavingSameDeviceSessionKeepsTheOtherCounted(t *testing.T) {
	env := startMediaE2EEEnv(t, true)
	_, s1, _, s2 := twoSameDeviceSessions(t, env)
	client2, _ := env.srv.clientByID(s2)
	if err := env.srv.leaveOwnChannelInContext(context.Background(), client2); err != nil {
		t.Fatal(err)
	}
	if env.srv.mediaSeats.seat(s1) != 1 || env.srv.mediaSeats.others(1, "") != 1 {
		t.Fatalf("after s2 left: seat(s1)=%d seats in 1=%d", env.srv.mediaSeats.seat(s1), env.srv.mediaSeats.others(1, ""))
	}
	assertEncryptedVoiceInvariant(t, env, 1)
}

// A session with a media device (a live voice peer) but no seat is never a
// free pass: its move takes a seat with the cap.
func TestMediaE2EEUnseatedVoiceSessionMeetsTheCap(t *testing.T) {
	env := startMediaE2EEEnv(t, true)
	_, userID := seatedVoiceMember(t, env)
	waitFor(t, "media device", func() bool { c, _ := env.state.GetClient(userID); return c.MediaDevice == "dev-1" })
	env.srv.mediaSeats.release(userID) // a lost seat, whatever the cause
	env.state.AddChannel(testChannel(2))
	env.state.AddChannel(testChannel(3))
	fillEncryptedChannel(t, env, 2)
	if err := env.srv.moveClientWithCause(context.Background(), userID, 2, userID, false); err != errMediaE2EEChannelFull {
		t.Fatalf("unseated voice session moved into a full channel: %v", err)
	}
	if err := env.srv.moveClientWithCause(context.Background(), userID, 3, userID, false); err != nil {
		t.Fatal(err)
	}
	if env.srv.mediaSeats.seat(userID) != 3 {
		t.Fatalf("seat after the move = %d, want 3", env.srv.mediaSeats.seat(userID))
	}
	assertEncryptedVoiceInvariant(t, env, 3)
}

// A dead session's seat that a reconnect replaced has its voice ended.
func TestMediaSeatsReplacementEndsVoiceAndUndoRestoresOnlyTheTaker(t *testing.T) {
	env := startMediaE2EEEnv(t, true)
	_, userID := seatedVoiceMember(t, env)
	waitFor(t, "media device", func() bool { c, _ := env.state.GetClient(userID); return c.MediaDevice == "dev-1" })
	if _, replaced, ok := env.srv.mediaSeats.take(1, "reconnect", mediaSeatID{"user-uid", "dev-1"}, MediaE2EEMaxVoice, func(string) bool { return false }); !ok || len(replaced) != 1 || replaced[0] != userID {
		t.Fatalf("replace: ok=%v replaced=%v", ok, replaced)
	}
	// The server-side wrapper ends a replaced session's voice.
	env.srv.endReplacedVoice([]string{userID})
	if c, _ := env.state.GetClient(userID); c.MediaDevice != "" {
		t.Fatal("replaced session kept its media device")
	}
	env.voice.mu.Lock()
	closed := false
	for _, id := range env.voice.closed {
		closed = closed || id == userID
	}
	env.voice.mu.Unlock()
	if !closed {
		t.Fatal("replaced session's peer was not closed")
	}

	// Undo restores the taker only: a replaced session is dead for good and
	// its voice was ended, so its seat is never restored.
	var m mediaSeats
	m.take(5, "old", mediaSeatID{"u", "d"}, 64, nil)
	undo, replaced, ok := m.take(5, "new", mediaSeatID{"u", "d"}, 64, func(string) bool { return false })
	if !ok || len(replaced) != 1 || m.seat("old") != 0 {
		t.Fatal("dead seat not replaced")
	}
	undo()
	if m.seat("old") != 0 || m.seat("new") != 0 || m.others(5, "") != 0 {
		t.Fatalf("undo: old=%d new=%d seats=%d", m.seat("old"), m.seat("new"), m.others(5, ""))
	}
}

// Fix4 review P3-1 (repro R2): a permission eviction parks the seat, so the
// channel it left no longer counts it; channel deletion parks seats too.
func TestMediaSeatsPermissionEvictionAndDeletionPark(t *testing.T) {
	backend := serverRoleFixture()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.Connect}
	var env *testEnv
	authority, err := authorization.NewAuthority(t.Context(), backend, func(ctx context.Context, before, after *authorization.RoleEvaluator) error {
		return env.srv.reconcileRoleMedia(ctx, before, after)
	})
	if err != nil {
		t.Fatal(err)
	}
	env = startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority })
	defer env.stop()
	env.state.AddChannel(testChannel(1))
	conn, memberID := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = conn.Close() }()
	if err := env.state.MoveClient(memberID, 1); err != nil {
		t.Fatal(err)
	}
	seatForTest(t, env.srv, 1, memberID, "admin-uid", "dev-1")
	_, err = authority.ChangeRolePolicy(t.Context(), 2, authorization.RoleChange{Kind: authorization.RoleUpdate, ExpectedRevision: 1, Role: authorization.Role{ID: 10, Name: "@everyone", Permissions: []authorization.Capability{authorization.ViewChannel}}})
	if err != nil {
		t.Fatal(err)
	}
	if ch, _, _ := env.state.ClientChannelState(memberID); ch != 0 {
		t.Fatal("member not evicted")
	}
	if got := env.srv.mediaSeats.others(1, ""); got != 0 {
		t.Fatalf("evicted member still counted in channel 1: %d seats", got)
	}
	if !parkedForTest(env.srv, memberID) {
		t.Fatal("evicted member's seat was released instead of parked (its peer lives on)")
	}

	seatForTest(t, env.srv, 7, "deleted-member", "u7", "d7")
	if err := env.srv.detachRoleDeletedChannels(channels.DeleteResult{ChannelIDs: []int64{7}, Members: []channels.DeletedMember{{ClientID: "deleted-member", ChannelID: 7}}}); err != nil {
		t.Fatal(err)
	}
	if got := env.srv.mediaSeats.others(7, ""); got != 0 || !parkedForTest(env.srv, "deleted-member") {
		t.Fatalf("deleted channel keeps %d seats", got)
	}
}

func parkedForTest(srv *TCPServer, client string) bool {
	srv.mediaSeats.mu.Lock()
	defer srv.mediaSeats.mu.Unlock()
	ch, ok := srv.mediaSeats.byClient[client]
	return ok && ch == 0
}

type failingTombstoneFT struct{ *fakeFileTransfer }

func (f failingTombstoneFT) TombstoneChannelData(int64) error { return errors.New("disk busy") }

// Fix5 review P3-1 (repro adopted): a role-channel deletion whose detach hook
// fails is not committed; members stay in the channel and keep their seats,
// so the channel admits no extra seats.
func TestMediaSeatsFailedRoleDeletionKeepsSeats(t *testing.T) {
	env := startMediaE2EEEnv(t, true)
	env.srv.deps.FileTransfer = failingTombstoneFT{&fakeFileTransfer{}}
	env.state.AddChannel(testChannel(1))
	var members []channels.DeletedMember
	for i := 0; i < 10; i++ {
		id := fmt.Sprintf("stay-%d", i)
		env.state.AddClient(&state.Client{ClientID: id, UniqueID: id, MediaDevice: "dev-" + id})
		if err := env.state.MoveClient(id, 1); err != nil {
			t.Fatal(err)
		}
		seatForTest(t, env.srv, 1, id, id, "dev-"+id)
		members = append(members, channels.DeletedMember{ClientID: id, ChannelID: 1})
	}
	if err := env.srv.detachRoleDeletedChannels(channels.DeleteResult{ChannelIDs: []int64{1}, Members: members}); err == nil {
		t.Fatal("hook did not fail")
	}
	if got := env.srv.mediaSeats.others(1, ""); got != 10 {
		t.Fatalf("failed deletion left %d seats in channel 1, want 10", got)
	}
	admitted := 0
	for i := 0; i < MediaE2EEMaxVoice; i++ {
		id := fmt.Sprintf("new-%d", i)
		if _, _, ok := env.srv.mediaSeats.take(1, id, mediaSeatID{id, "dev-" + id}, MediaE2EEMaxVoice, nil); ok {
			admitted++
		}
	}
	if admitted != MediaE2EEMaxVoice-10 {
		t.Fatalf("admitted %d newcomers, want %d", admitted, MediaE2EEMaxVoice-10)
	}
}

// Fix5 review P3-2 (repro adopted): an explicit or automatic channel deletion
// (ApplyChannelDeletion) parks the members' seats too.
func TestMediaSeatsExplicitDeletionParks(t *testing.T) {
	env := startMediaE2EEEnv(t, true)
	seatForTest(t, env.srv, 9, "m9", "u9", "d9")
	env.srv.ApplyChannelDeletion(channels.DeleteResult{RootID: 9, ChannelIDs: []int64{9}, Members: []channels.DeletedMember{{ClientID: "m9", ChannelID: 9}}}, "deleted")
	if env.srv.mediaSeats.others(9, "") != 0 || !parkedForTest(env.srv, "m9") {
		t.Fatalf("explicit deletion: seat %d, seats in 9: %d", env.srv.mediaSeats.seat("m9"), env.srv.mediaSeats.others(9, ""))
	}
}
