package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"go.uber.org/zap"

	"noxa/internal/authorization"
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
// still receives the same delivery well before the general write ceiling.
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
	actor, _ := dialAuthed(t, env.addr, "user-uid")
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
	defer target.wmu.Unlock()

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
	if wait := tr.wait("u", 1, 60, "a", now); wait != 0 {
		t.Fatalf("first send waits %v", wait)
	}
	// A send rejected later (filter, store error) is never marked: the resend
	// is allowed at once.
	if wait := tr.wait("u", 1, 60, "b", now.Add(time.Second)); wait != 0 {
		t.Fatalf("resend after a rejection waits %v", wait)
	}
	tr.mark("u", 1, 60, "b", now)
	if wait := tr.wait("u", 1, 60, "c", now.Add(10*time.Second)); wait <= 0 {
		t.Fatal("a second message inside the window was allowed")
	}
	// A retry of the stored message reaches the store's idempotency check.
	if wait := tr.wait("u", 1, 60, "b", now.Add(10*time.Second)); wait != 0 {
		t.Fatalf("retry of the stored message waits %v", wait)
	}
	if wait := tr.wait("u", 1, 60, "", now.Add(10*time.Second)); wait <= 0 {
		t.Fatal("a send without a client id matched the recorded message")
	}
	if wait := tr.wait("u", 1, 60, "c", now.Add(61*time.Second)); wait != 0 {
		t.Fatalf("send after the window waits %v", wait)
	}
}

func TestChatTrackersForgetIdleUsers(t *testing.T) {
	now := time.Now()
	slow := newSlowTracker()
	spam := newSpamTracker()
	for i := 0; i <= trackerSoftLimit; i++ {
		uid := fmt.Sprintf("guest:%d", i)
		slow.mark(uid, 1, 5, "", now)
		spam.record(uid, [32]byte{1}, now)
	}
	later := now.Add(time.Minute)
	slow.mark("guest:new", 1, 5, "", later)
	spam.record("guest:new", [32]byte{1}, later)
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
	send(t, bob, netproto.MsgPing, netproto.Ping{})
	readOfType(t, bob, netproto.MsgPong) // drain bob's join-time frames
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
