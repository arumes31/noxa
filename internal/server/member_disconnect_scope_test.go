package server

import (
	"context"
	"testing"
	"time"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

func TestAcknowledgedDisconnectRejectsChangedChannel(t *testing.T) {
	backend := serverRoleFixture()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.Connect}
	backend.policy.Channels = append(backend.policy.Channels, authorization.ChannelPolicy{ChannelID: 2})
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority })
	defer env.stop()
	env.state.AddChannel(testChannel(1))
	env.state.AddChannel(testChannel(2))
	sender, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = sender.Close() }()
	target, targetID := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = target.Close() }()
	if err := env.state.MoveClient(targetID, 1); err != nil {
		t.Fatal(err)
	}
	// The dialog captured channel 1 before the member joined channel 2.
	if err := env.state.MoveClient(targetID, 2); err != nil {
		t.Fatal(err)
	}
	send(t, sender, netproto.MsgKickClient, map[string]any{"client_id": targetID, "expected_channel_id": 1, "ack_requested": true})
	send(t, sender, netproto.MsgPing, netproto.Ping{})
	if err := sender.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	got := readMemberActionFence(sender, nil)
	channel, _, exists := env.state.ClientChannelState(targetID)
	if got.err != nil || len(got.removed) != 0 || len(got.errors) != 1 || got.errors[0].OriginType != uint16(netproto.MsgKickClient) || !exists || channel != 2 {
		t.Fatalf("stale disconnect: %+v; membership %d/%v", got, channel, exists)
	}
}

func TestAcknowledgedDisconnectRejectsInvalidSource(t *testing.T) {
	authority := chatSendTestAuthority(t)
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority })
	defer env.stop()
	env.state.AddChannel(testChannel(1))
	sender, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = sender.Close() }()
	target, targetID := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = target.Close() }()
	if err := env.state.MoveClient(targetID, 1); err != nil {
		t.Fatal(err)
	}
	for _, msg := range []netproto.KickClient{
		{ExpectedChannelID: 0}, {ExpectedChannelID: -1}, {ExpectedChannelID: 2},
		{ExpectedChannelID: 1, FromServer: true}, {ExpectedChannelID: 1, Ban: true},
	} {
		msg.ClientID, msg.AckRequested = targetID, true
		send(t, sender, netproto.MsgKickClient, msg)
		send(t, sender, netproto.MsgPing, netproto.Ping{})
		if err := sender.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatal(err)
		}
		got := readMemberActionFence(sender, nil)
		channel, _, exists := env.state.ClientChannelState(targetID)
		if got.err != nil || len(got.removed) != 0 || len(got.errors) != 1 || got.errors[0].OriginType != uint16(netproto.MsgKickClient) || !exists || channel != 1 {
			t.Fatalf("invalid source %+v: %+v; membership %d/%v", msg, got, channel, exists)
		}
	}
}

func TestAcknowledgedDisconnectRechecksAfterTargetLock(t *testing.T) {
	authority := chatSendTestAuthority(t)
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority })
	defer env.stop()
	env.state.AddChannel(testChannel(1))
	env.state.AddChannel(testChannel(2))
	sender, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = sender.Close() }()
	targetConn, targetID := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = targetConn.Close() }()
	if err := env.state.MoveClient(targetID, 1); err != nil {
		t.Fatal(err)
	}
	target, _ := env.srv.clientByID(targetID)
	target.roleActionMu.Lock()
	locked := true
	defer func() {
		if locked {
			target.roleActionMu.Unlock()
		}
	}()
	send(t, sender, netproto.MsgKickClient, netproto.KickClient{ClientID: targetID, ExpectedChannelID: 1, AckRequested: true})
	send(t, sender, netproto.MsgPing, netproto.Ping{})
	if err := sender.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	done, accepted := make(chan memberActionFence, 1), make(chan struct{}, 4)
	go func() { done <- readMemberActionFence(sender, accepted) }()
	select {
	case <-accepted:
		t.Fatal("acknowledged while target lock held")
	case got := <-done:
		t.Fatalf("completed while target locked: %+v", got)
	case <-time.After(25 * time.Millisecond):
	}
	// Simulate the move that owns the target lock completing before disconnect.
	if err := env.state.MoveClient(targetID, 2); err != nil {
		t.Fatal(err)
	}
	target.roleActionMu.Unlock()
	locked = false
	got := <-done
	channel, _, exists := env.state.ClientChannelState(targetID)
	if got.err != nil || len(got.removed) != 0 || len(got.errors) != 1 || !exists || channel != 2 {
		t.Fatalf("source recheck: %+v; membership %d/%v", got, channel, exists)
	}
}
