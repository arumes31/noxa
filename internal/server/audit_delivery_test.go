package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"go.uber.org/zap"

	"noxa/internal/authorization"
	"noxa/internal/broadcast"
	"noxa/internal/config"
	"noxa/internal/netproto"
)

// A write that times out may leave half a frame on the wire, and every later
// write to that dead peer would stall again: the connection is closed.
func TestFailedWriteClosesTheConnection(t *testing.T) {
	server, peer := net.Pipe() // the peer never reads, so writes block
	defer func() { _ = peer.Close() }()
	s := &TCPServer{logger: zap.NewNop()}
	client := &Client{ID: "dead", Conn: server}
	frame := &netproto.Frame{Type: uint16(netproto.MsgPing), Payload: []byte("{}")}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := s.writeFrameLocked(ctx, client, frame); err == nil {
		t.Fatal("write to a peer that never reads succeeded")
	}
	started := time.Now()
	if err := s.writeFrameLocked(context.Background(), client, frame); !errors.Is(err, net.ErrClosed) && !errors.Is(err, errors.Unwrap(err)) {
		t.Logf("second write error: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 20*time.Millisecond {
		t.Fatalf("second write to the closed peer waited %v", elapsed)
	}
	if cause := client.closeCause.Load(); cause == nil || *cause != netproto.ControlCloseWriteTimeout {
		t.Fatalf("close cause = %v, want write_timeout", cause)
	}
}

// A stalled recipient may hold the metadata lock only briefly: another member
// still receives the same delivery well before the general write ceiling. The
// stalled member is not closed for it; once its writer frees, a resync
// snapshot replaces the delivery it missed.
func TestStalledRecipientDoesNotStallMetadataDelivery(t *testing.T) {
	backend := serverRoleFixture()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.Connect}
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority })
	defer env.stop()
	env.state.AddChannel(testChannel(1))
	stalled, stalledID := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = stalled.Close() }()
	actor, actorID := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = actor.Close() }()
	for _, conn := range []net.Conn{stalled, actor} {
		send(t, conn, netproto.MsgPing, netproto.Ping{})
		readOfType(t, conn, netproto.MsgPong)
	}
	target, ok := env.srv.clientByID(stalledID)
	if !ok {
		t.Fatal("stalled member missing")
	}
	target.wmu.Lock()

	started := time.Now()
	// The join is delivered to both members as a snapshot under the metadata
	// lock; the stalled member's writer holds it while waiting for wmu.
	send(t, actor, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: 1})
	readOfType(t, actor, netproto.MsgSnapshot)
	if elapsed := time.Since(started); elapsed >= roleWriteCeiling {
		t.Fatalf("delivery to a healthy member waited %v behind a stalled one", elapsed)
	} else {
		t.Logf("healthy member received the snapshot after %v", elapsed)
	}
	// Waiting out the short metadata ceiling skips that delivery; only a write
	// stuck for the whole write ceiling closes the member.
	time.Sleep(metadataWriteCeiling + 500*time.Millisecond)
	if cause := target.closeCause.Load(); cause != nil {
		target.wmu.Unlock()
		t.Fatalf("member behind a busy writer was closed: %v", *cause)
	}
	target.wmu.Unlock()
	// An older queued snapshot may come first; a resync built after the join
	// must follow.
	for deadline := time.Now().Add(roleWriteCeiling + 2*time.Second); time.Now().Before(deadline); {
		var snapshot broadcast.TreeSnapshot
		if err := json.Unmarshal(readOfType(t, stalled, netproto.MsgSnapshot).Payload, &snapshot); err != nil {
			t.Fatal(err)
		}
		for _, node := range snapshot.RootChannels {
			for _, member := range node.Clients {
				if member.ClientID == actorID && member.ChannelID == 1 {
					return
				}
			}
		}
	}
	t.Fatal("resync snapshot does not show the join the stalled member missed")
}

// Clients refetch nothing but the tree on a snapshot, so only membership,
// speaking and typing deliveries may be replaced by a resync.
func TestSnapshotRecoversOnlyTreeAndPresence(t *testing.T) {
	for payload, want := range map[string]bool{
		`{"type":"user_joined","data":{}}`:      true,
		`{"type":"speaking_changed","data":{}}`: true,
		roleResyncPayload:                       true,
		`{"type":"chat","data":{}}`:             false,
		`{"type":"poke","data":{}}`:             false,
		`{"type":"_role_channel_delivery","data":{"channel_id":1,"payload":{"type":"typing"}}}`: true,
		`{"type":"_role_channel_delivery","data":{"channel_id":1,"payload":{"type":"chat"}}}`:   false,
		`not json`: false,
	} {
		if got := snapshotRecovers([]byte(payload)); got != want {
			t.Errorf("snapshotRecovers(%s) = %v, want %v", payload, got, want)
		}
	}
}

// Without the role writer nothing turns a resync marker into a snapshot, so a
// client that dropped events is still closed to resync by reconnecting.
func TestLegacyServerClosesInsteadOfResync(t *testing.T) {
	bc := broadcast.New(zap.NewNop(), nil)
	New(&config.Config{}, zap.NewNop(), &Deps{Broadcast: bc})
	overflows := make(chan string, 1)
	bc.SetOverflowHandler(func(id string) { overflows <- id })
	ch, err := bc.Register("legacy")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i <= cap(ch); i++ {
		_ = bc.BroadcastToClient("legacy", []byte(`{"type":"user_joined","data":{}}`))
	}
	for len(ch) > 0 {
		<-ch
	}
	select {
	case <-overflows:
	case <-time.After(5 * time.Second):
		t.Fatal("a legacy client that dropped events was not closed")
	}
}

func TestStatusAndDisplayNameHaveASlowBudget(t *testing.T) {
	var b sessionRequestBudget
	now := time.Now()
	for i := 0; i < 5; i++ {
		if !b.allow(netproto.MsgSetStatus, now) {
			t.Fatal("legitimate status burst rejected")
		}
	}
	if b.allow(netproto.MsgDisplayNameSet, now) || b.allow(netproto.MsgSetStatus, now) {
		t.Fatal("status/name changes are not throttled")
	}
	if !b.allow(netproto.MsgChatSend, now) {
		t.Fatal("chat shares the status budget")
	}
	if !b.allow(netproto.MsgSetStatus, now.Add(2*time.Second)) {
		t.Fatal("status budget did not refill")
	}
}

func TestSlowModeMarksOnlyAcceptedSends(t *testing.T) {
	tr := newSlowTracker()
	now := time.Now()
	wait, finish := tr.reserve("u", 1, 60, "a", now)
	if wait != 0 || finish == nil {
		t.Fatalf("first send waits %v", wait)
	}
	// While the first send is processed, a second send of the same user
	// cannot pass too.
	if wait, second := tr.reserve("u", 1, 60, "x", now); wait <= 0 || second != nil {
		t.Fatal("a concurrent send passed slow mode")
	}
	// A rejected send (filter, store error) releases the slot.
	finish(false)
	wait, finish = tr.reserve("u", 1, 60, "b", now.Add(time.Second))
	if wait != 0 {
		t.Fatalf("resend after a rejection waits %v", wait)
	}
	finish(true)
	if wait, _ := tr.reserve("u", 1, 60, "c", now.Add(10*time.Second)); wait <= 0 {
		t.Fatal("a second message inside the window was allowed")
	}
	// A retry of the stored message reaches the store's idempotency check.
	wait, finish = tr.reserve("u", 1, 60, "b", now.Add(10*time.Second))
	if wait != 0 {
		t.Fatalf("retry of the stored message waits %v", wait)
	}
	finish(true)
	if wait, _ := tr.reserve("u", 1, 60, "", now.Add(10*time.Second)); wait <= 0 {
		t.Fatal("a send without a client id matched the recorded message")
	}
	if wait, _ := tr.reserve("u", 1, 60, "c", time.Now().Add(61*time.Second)); wait != 0 {
		t.Fatalf("send after the window waits %v", wait)
	}
}

func TestSpamIgnoresRetriesOfTheSameMessage(t *testing.T) {
	tr := newSpamTracker()
	now := time.Now()
	for i := 0; i < 5; i++ {
		if tr.record("u", [32]byte{7}, "msg-1", now.Add(time.Duration(i)*time.Second)) {
			t.Fatalf("retry %d of one message counted as spam", i)
		}
	}
	tr.record("u", [32]byte{7}, "msg-2", now)
	if !tr.record("u", [32]byte{7}, "msg-3", now) {
		t.Fatal("three distinct identical messages were not flagged")
	}
}

func TestChatTrackersForgetIdleUsers(t *testing.T) {
	now := time.Now()
	slow := newSlowTracker()
	spam := newSpamTracker()
	for i := 0; i <= trackerSoftLimit; i++ {
		uid := fmt.Sprintf("guest:%d", i)
		slow.check(uid, 1, 5, now)
		spam.record(uid, [32]byte{1}, "", now)
	}
	later := now.Add(time.Minute)
	slow.check("guest:new", 1, 5, later)
	spam.record("guest:new", [32]byte{1}, "", later)
	slow.mu.Lock()
	slowKeys := len(slow.last)
	slow.mu.Unlock()
	spam.mu.Lock()
	spamKeys := len(spam.recent)
	spam.mu.Unlock()
	if slowKeys != 1 || spamKeys != 1 {
		t.Fatalf("idle users kept: slow=%d spam=%d, want 1 each", slowKeys, spamKeys)
	}
}

// Saving the name a member already has changes nothing anyone can see, so it
// must not rebuild a snapshot for every recipient.
func TestUnchangedDisplayNameIsNotBroadcast(t *testing.T) {
	env := startTestEnvWithCapabilities(t, authorization.PokeMembers)
	defer env.stop()
	alice, aliceID := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = alice.Close() }()
	bob, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = bob.Close() }()
	current, _ := env.state.GetClient(aliceID)
	// Let both logins' snapshots reach bob before the unchanged rename.
	for settle := time.Now().Add(500 * time.Millisecond); time.Now().Before(settle); {
		_ = bob.SetReadDeadline(settle)
		if _, err := netproto.ReadFrame(bob); err != nil {
			break
		}
	}
	_ = bob.SetReadDeadline(time.Time{})
	send(t, alice, netproto.MsgDisplayNameSet, netproto.DisplayNameSet{Nickname: current.Nickname})
	var saved netproto.DisplayNameSaved
	if err := netproto.Decode(readOfType(t, alice, netproto.MsgDisplayNameSaved), &saved); err != nil || saved.Nickname != current.Nickname {
		t.Fatalf("unchanged name not acknowledged: %+v, %v", saved, err)
	}
	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		_ = bob.SetReadDeadline(deadline)
		f, err := netproto.ReadFrame(bob)
		if err != nil {
			return
		}
		if netproto.MessageType(f.Type) == netproto.MsgSnapshot {
			t.Fatal("an unchanged display name was broadcast as a snapshot")
		}
	}
}
