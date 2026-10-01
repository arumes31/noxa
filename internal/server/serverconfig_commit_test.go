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
)

func TestExplicitWriteDeadlineBoundsLegacySocket(t *testing.T) {
	sender, receiver := net.Pipe()
	defer func() { _ = sender.Close(); _ = receiver.Close() }()
	srv := New(&config.Config{}, zap.NewNop(), &Deps{})
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- srv.writeMessageInContext(ctx, &Client{Conn: sender}, netproto.MsgServerConfigResponse, netproto.ServerConfig{})
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("unread socket write succeeded")
		}
	case <-time.After(300 * time.Millisecond):
		_ = sender.Close()
		<-done
		t.Fatal("explicit reply deadline did not interrupt the legacy socket write")
	}
}

type blockedConfigStore struct {
	*fakeChat
	firstSaved, releaseFirst, secondSaved chan struct{}
}

type configCommitStore struct {
	*fakeChat
	calls     int
	failure   error
	afterSave func()
}

func (b *configCommitStore) SetServerSettings(ctx context.Context, values map[string]string, id uint32) error {
	b.calls++
	if b.failure != nil {
		return b.failure
	}
	if err := b.fakeChat.SetServerSettings(ctx, values, id); err != nil {
		return err
	}
	if b.afterSave != nil {
		b.afterSave()
	}
	return nil
}

func TestServerConfigCommitFailureCancellationAndAudit(t *testing.T) {
	request := netproto.ServerConfig{MaxClients: 100, ClientTimeoutSeconds: 120, OpusBitrate: 64000, OpusFEC: true}
	authority, err := authorization.NewAuthority(t.Context(), serverRoleFixture(), func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	b := &configCommitStore{fakeChat: newFakeChat()}
	audit := &disconnectAuditContext{}
	srv := New(&config.Config{MaxClients: 25}, zap.NewNop(), &Deps{Chat: b, Authority: authority, Groups: audit})
	before := srv.serverConfig()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := srv.saveServerConfig(ctx, "actor", request); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, mutate := range []func(*netproto.ServerConfig){
		func(c *netproto.ServerConfig) { c.MaxClients = -1 }, func(c *netproto.ServerConfig) { c.MaxClients = 100001 },
		func(c *netproto.ServerConfig) { c.ClientTimeoutSeconds = 29 }, func(c *netproto.ServerConfig) { c.ClientTimeoutSeconds = 86401 },
		func(c *netproto.ServerConfig) { c.OpusBitrate = 5999 }, func(c *netproto.ServerConfig) { c.OpusBitrate = 510001 },
	} {
		invalid := request
		mutate(&invalid)
		if _, err := srv.saveServerConfig(t.Context(), "actor", invalid); !errors.Is(err, authorization.ErrRoleInvalid) {
			t.Fatalf("invalid limits: %v", err)
		}
	}
	if b.calls != 0 {
		t.Fatal("canceled or invalid input reached persistence")
	}
	srv.configSaveMu.Lock()
	waitCtx, stop := context.WithTimeout(t.Context(), 20*time.Millisecond)
	_, err = srv.saveServerConfig(waitCtx, "actor", request)
	stop()
	srv.configSaveMu.Unlock()
	if !errors.Is(err, context.DeadlineExceeded) || b.calls != 0 {
		t.Fatalf("canceled waiter reached persistence: %v", err)
	}
	b.failure = errors.New("transaction rolled back")
	if _, err := srv.saveServerConfig(t.Context(), "actor", request); !errors.Is(err, b.failure) {
		t.Fatal(err)
	}
	if srv.serverConfig() != before || audit.calls != 0 {
		t.Fatal("failed commit changed runtime or claimed an audit")
	}
	ctx, cancel = context.WithCancel(t.Context())
	defer cancel()
	b.failure, b.afterSave = nil, cancel
	got, err := srv.saveServerConfig(ctx, "actor", request)
	if err != nil || got != request || srv.serverConfig() != request || ctx.Err() == nil || audit.calls != 1 || audit.err != nil {
		t.Fatalf("completed commit lost publication/audit: got=%+v err=%v audit=%+v", got, err, audit)
	}
}

func (b *blockedConfigStore) SetServerSettings(ctx context.Context, values map[string]string, id uint32) error {
	if err := b.fakeChat.SetServerSettings(ctx, values, id); err != nil {
		return err
	}
	if values["max_clients_override"] == "100" {
		close(b.firstSaved)
		<-b.releaseFirst
	} else {
		close(b.secondSaved)
	}
	return nil
}

func TestServerConfigSerializesPersistenceAndPublication(t *testing.T) {
	b := &blockedConfigStore{fakeChat: newFakeChat(), firstSaved: make(chan struct{}), releaseFirst: make(chan struct{}), secondSaved: make(chan struct{})}
	srv := New(&config.Config{}, zap.NewNop(), &Deps{Chat: b})
	var once sync.Once
	defer once.Do(func() { close(b.releaseFirst) })
	first, second := make(chan error, 1), make(chan error, 1)
	request := netproto.ServerConfig{MaxClients: 100, ClientTimeoutSeconds: 120, OpusBitrate: 64000}
	go func() { first <- srv.applyServerConfig(t.Context(), &Client{Conn: newBlockingTCPConn()}, request) }()
	select {
	case <-b.firstSaved:
	case <-time.After(3 * time.Second):
		t.Fatal("first save missing")
	}
	next := request
	next.MaxClients = 200
	go func() { second <- srv.applyServerConfig(t.Context(), &Client{Conn: newBlockingTCPConn()}, next) }()
	overtook := false
	select {
	case <-b.secondSaved:
		overtook = true
	case <-time.After(30 * time.Millisecond):
	}
	once.Do(func() { close(b.releaseFirst) })
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	value, _, err := b.GetServerSetting(t.Context(), "max_clients_override")
	if overtook || err != nil || value != "200" || srv.serverConfig().MaxClients != 200 {
		t.Fatalf("save order diverged: overtook=%v stored=%q runtime=%+v err=%v", overtook, value, srv.serverConfig(), err)
	}
}
