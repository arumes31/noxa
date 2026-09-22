package server

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"go.uber.org/zap"

	"noxa/internal/authorization"
	"noxa/internal/config"
	"noxa/internal/netproto"
	"noxa/internal/state"
)

type disconnectAuditContext struct {
	AuditStore
	calls int
	err   error
}

func TestRoleCancelledKeyFanoutPreservesSubscriptions(t *testing.T) {
	backend := serverRoleFixture()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel}
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	sm := state.New(zap.NewNop())
	sm.AddChannel(testChannel(1))
	sm.AddClient(&state.Client{ClientID: "subscriber", UserID: 1, UniqueID: "member"})
	sm.Subscribe("subscriber", []int64{1})
	srv := New(&config.Config{}, zap.NewNop(), &Deps{State: sm, Authority: authority})
	c := &Client{ID: "subscriber"}
	c.setIdentity("member", "Member", 1, false)
	srv.clients[c.ID] = c
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if recipients := srv.channelSubscribers(ctx, 1); len(recipients) != 0 {
		t.Fatal("cancelled fanout returned recipients")
	}
	if !sm.IsSubscribed(c.ID, 1) {
		t.Fatal("notification cancellation was treated as revoked permission")
	}
}

func (a *disconnectAuditContext) AuditScoped(ctx context.Context, _, _, _, _ string, _ []int64) {
	a.calls++
	a.err = ctx.Err()
}

type cancelDisconnectNotification struct {
	*blockingTCPConn
	cancel context.CancelFunc
}

func (c *cancelDisconnectNotification) Write(p []byte) (int, error) { c.cancel(); return len(p), nil }

func TestRoleDisconnectAuditsAfterNotificationDeadline(t *testing.T) {
	authority, err := authorization.NewAuthority(t.Context(), serverRoleFixture(), func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	sm := state.New(zap.NewNop())
	sm.AddChannel(testChannel(1))
	audit := &disconnectAuditContext{}
	srv := New(&config.Config{}, zap.NewNop(), &Deps{State: sm, Authority: authority, Groups: audit})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	target := &Client{ID: "target", Conn: &cancelDisconnectNotification{blockingTCPConn: newBlockingTCPConn(), cancel: cancel}}
	target.setIdentity("member", "Member", 1, false)
	srv.clients[target.ID] = target
	sm.AddClient(&state.Client{ClientID: target.ID, UserID: 1, UniqueID: "member"})
	if err := sm.MoveClient(target.ID, 1); err != nil {
		t.Fatal(err)
	}
	err = srv.withRolePolicy(ctx, func(ctx context.Context) error {
		srv.roleMetadataMu.Lock()
		defer srv.roleMetadataMu.Unlock()
		_, err := srv.disconnectRoleMember(ctx, ctx.Value(roleLeaseKey{}).(roleLease).evaluator, 2, "owner", "", target.ID, 1, "Leave voice")
		return err
	})
	if err != nil || audit.calls != 1 || audit.err != nil {
		t.Fatalf("committed disconnect lost its audit context: calls=%d audit=%v result=%v", audit.calls, audit.err, err)
	}
}

func TestRoleNotificationUsesRemainingOperationDeadline(t *testing.T) {
	server, peer := net.Pipe()
	defer func() { _ = server.Close(); _ = peer.Close() }()
	srv := &TCPServer{logger: zap.NewNop(), deps: &Deps{Authority: &authorization.Authority{}}}
	client := &Client{ID: "slow", Conn: server}
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := srv.writeMessageInContext(ctx, client, netproto.MsgSubscriptionState, netproto.SubscriptionState{})
	var timeout net.Error
	if !errors.As(err, &timeout) || !timeout.Timeout() {
		t.Fatalf("expected bounded slow-reader error, got %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("notification renewed its own five-second window")
	}
	cancelled, stop := context.WithCancel(t.Context())
	stop()
	if err := srv.writeMessageInContext(cancelled, client, netproto.MsgSubscriptionState, netproto.SubscriptionState{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled notification attempted transport: %v", err)
	}
}

func TestRoleNotificationDeadlineWhileWriterBusy(t *testing.T) {
	server, peer := net.Pipe()
	defer func() { _ = server.Close(); _ = peer.Close() }()
	srv := &TCPServer{logger: zap.NewNop(), deps: &Deps{Authority: &authorization.Authority{}}}
	client := &Client{ID: "busy", Conn: server}
	client.wmu.Lock()
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		finished <- srv.writeMessageInContext(ctx, client, netproto.MsgSubscriptionState, netproto.SubscriptionState{})
	}()
	select {
	case err := <-finished:
		client.wmu.Unlock()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		client.wmu.Unlock()
		<-finished
		t.Fatal("cancelled notification remained blocked on the writer lock")
	}
}

func TestRoleChannelDisconnectRetainsLeaveLifecycle(t *testing.T) {
	backend := serverRoleFixture()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.Connect, authorization.Speak}
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority })
	defer env.stop()
	env.state.AddChannel(testChannel(1))
	member, memberID := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = member.Close() }()
	owner, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = owner.Close() }()
	if err := env.state.MoveClient(memberID, 1); err != nil {
		t.Fatal(err)
	}
	on := true
	if _, err := env.state.SetServerVoiceState(memberID, &on, &on); err != nil {
		t.Fatal(err)
	}
	oldGeneration, _, err := env.srv.chatKeys.EnsureScope(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	send(t, owner, netproto.MsgKickClient, netproto.KickClient{ClientID: memberID, Reason: "leave voice"})
	readEventOfType(t, member, eventKicked)
	current, ok := env.state.GetClient(memberID)
	if !ok || current.ChannelID != 0 || !current.ServerMuted || !current.ServerDeafened {
		t.Fatalf("channel disconnect changed session lifetime: %+v", current)
	}
	generation, _, err := env.srv.chatKeys.current(t.Context(), 1)
	if err != nil || generation == oldGeneration {
		t.Fatalf("channel kick skipped leave rotation: %d -> %d %v", oldGeneration, generation, err)
	}
}

func TestRoleChannelDisconnectRejectsInvisibleTarget(t *testing.T) {
	backend := serverRoleFixture()
	backend.policy.OwnerID = 3
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.Connect}
	backend.policy.Roles = append(backend.policy.Roles, authorization.Role{ID: 20, Name: "Moderator", Position: 1, Permissions: []authorization.Capability{authorization.DisconnectMembers}})
	backend.policy.Members = []authorization.RoleMember{{UserID: 1, RoleIDs: []int64{20}}}
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority })
	defer env.stop()
	env.state.AddChannel(testChannel(1))
	moderator, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = moderator.Close() }()
	member, memberID := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = member.Close() }()
	if err := env.state.MoveClient(memberID, 1); err != nil {
		t.Fatal(err)
	}
	env.state.SetStatus(memberID, "invisible", "")
	send(t, moderator, netproto.MsgKickClient, netproto.KickClient{ClientID: memberID})
	readRoleMediaDenial(t, moderator)
	if channel, _, _ := env.state.ClientChannelState(memberID); channel != 1 {
		t.Fatal("hidden target was disconnected")
	}
}
