package server

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"noxa/internal/authorization"
	"noxa/internal/config"
	"noxa/internal/netproto"
	"noxa/internal/store"
)

type roleBanDeleteStore struct {
	*fakeBanAdmin
	entered chan struct{}
	release chan struct{}
	failure error
	cancel  context.CancelFunc
}

func (b *roleBanDeleteStore) DeleteBan(ctx context.Context, id int64) error {
	if b.entered != nil {
		b.entered <- struct{}{}
		select {
		case <-b.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if b.failure != nil {
		return b.failure
	}
	if err := b.fakeBanAdmin.DeleteBan(ctx, id); err != nil {
		return err
	}
	if b.cancel != nil {
		b.cancel()
	}
	return nil
}

func TestRoleBanRemovalWaitsForPersistenceAndCurrentPermission(t *testing.T) {
	backend := serverRoleFixture()
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	b := &roleBanDeleteStore{fakeBanAdmin: &fakeBanAdmin{bans: []store.BanRecord{{ID: 17}, {ID: 18}}}, entered: make(chan struct{}, 1), release: make(chan struct{})}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority; d.BanAdmin = b })
	defer env.stop()
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(b.release) }) }
	defer release()
	owner, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = owner.Close() }()
	member, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = member.Close() }()
	send(t, member, netproto.MsgRoleBanRemove, netproto.BanRemove{BanID: 17})
	if got := readError(t, member); got.Code != errCodePermissionDenied {
		t.Fatal(got)
	}
	select {
	case <-b.entered:
		t.Fatal("unauthorized deletion")
	default:
	}
	send(t, owner, netproto.MsgRoleBanRemove, netproto.BanRemove{BanID: 17})
	select {
	case <-b.entered:
	case <-time.After(time.Second):
		t.Fatal("delete not started")
	}
	if err := owner.SetReadDeadline(time.Now().Add(40 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	for {
		frame, err := netproto.ReadFrame(owner)
		if err != nil {
			var timeout net.Error
			if !errors.As(err, &timeout) || !timeout.Timeout() {
				t.Fatal(err)
			}
			break
		}
		if netproto.MessageType(frame.Type) == netproto.MsgRoleBanRemoved || netproto.MessageType(frame.Type) == netproto.MsgError {
			t.Fatalf("result before persistence: %+v", frame)
		}
	}
	release()
	if err := owner.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var result netproto.RoleBanRemoved
	if err := netproto.Decode(readOfType(t, owner, netproto.MsgRoleBanRemoved), &result); err != nil || result.BanID != 17 {
		t.Fatalf("ack: %+v %v", result, err)
	}
	rows, err := b.ListBans(t.Context())
	if err != nil || len(rows) != 1 || rows[0].ID != 18 {
		t.Fatalf("remaining bans: %+v %v", rows, err)
	}
	for _, id := range []int64{0, -1} {
		send(t, owner, netproto.MsgRoleBanRemove, netproto.BanRemove{BanID: id})
		if got := readError(t, owner); got.Code != errCodeMalformed {
			t.Fatal(got)
		}
	}
}

func TestRoleBanRemovalKeepsCommittedAuditAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	b := &roleBanDeleteStore{fakeBanAdmin: &fakeBanAdmin{bans: []store.BanRecord{{ID: 17}}}, cancel: cancel}
	audit := &disconnectAuditContext{}
	authority, err := authorization.NewAuthority(t.Context(), serverRoleFixture(), func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	srv := New(&config.Config{}, zap.NewNop(), &Deps{Authority: authority, BanAdmin: b, Groups: audit})
	if err := srv.removeRoleBan(ctx, "actor", 17); err != nil || audit.calls != 1 || audit.err != nil || ctx.Err() == nil {
		t.Fatalf("lost committed audit: %v %+v", err, audit)
	}
	if err := srv.removeRoleBan(ctx, "actor", 17); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if audit.calls != 1 {
		t.Fatal("canceled delete audited")
	}
}

func TestRoleBanRemovalFailureDoesNotAcknowledgeOrAudit(t *testing.T) {
	b := &roleBanDeleteStore{fakeBanAdmin: &fakeBanAdmin{bans: []store.BanRecord{{ID: 17}}}, failure: errors.New("private persistence failure")}
	audit := &disconnectAuditContext{}
	srv := New(&config.Config{}, zap.NewNop(), &Deps{BanAdmin: b, Groups: audit})
	if err := srv.removeRoleBan(t.Context(), "actor", 17); !errors.Is(err, b.failure) {
		t.Fatal(err)
	}
	if audit.calls != 0 {
		t.Fatal("failed delete audited as successful")
	}
	rows, _ := b.ListBans(t.Context())
	if len(rows) != 1 {
		t.Fatal("failed delete changed bans")
	}
}

func TestRoleBanRemovalAcknowledgesCommitAfterRequestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	b := &roleBanDeleteStore{fakeBanAdmin: &fakeBanAdmin{bans: []store.BanRecord{{ID: 17}}}, cancel: cancel}
	authority, err := authorization.NewAuthority(t.Context(), serverRoleFixture(), func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	srv := New(&config.Config{}, zap.NewNop(), &Deps{Authority: authority, BanAdmin: b})
	sender, receiver := net.Pipe()
	defer func() { _ = sender.Close(); _ = receiver.Close() }()
	client := &Client{Conn: sender}
	client.setIdentity("user-uid", "Owner", 2, false)
	frame, err := netproto.Encode(netproto.MsgRoleBanRemove, netproto.BanRemove{BanID: 17})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- srv.handleRoleBanRemove(ctx, client, frame) }()
	if err := receiver.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	response, err := netproto.ReadFrame(receiver)
	if err != nil {
		t.Fatal(err)
	}
	var result netproto.RoleBanRemoved
	if netproto.MessageType(response.Type) != netproto.MsgRoleBanRemoved || netproto.Decode(response, &result) != nil || result.BanID != 17 || !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("committed reply: %+v %+v %v", response, result, ctx.Err())
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRoleBanRemovalRequiresConfiguredAuthority(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	client, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = client.Close() }()
	send(t, client, netproto.MsgRoleBanRemove, netproto.BanRemove{BanID: 17})
	if got := readError(t, client); got.Code != errCodeUnavailable {
		t.Fatal(got)
	}
}
