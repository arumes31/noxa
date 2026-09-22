package server

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"noxa/internal/authorization"
	"noxa/internal/config"
)

type pausedTextRead struct {
	ChatStore
	read, release chan struct{}
	once          sync.Once
}

func (b *pausedTextRead) GetServerSetting(ctx context.Context, key string) (string, uint32, error) {
	value, id, err := b.ChatStore.GetServerSetting(ctx, key)
	if key == "motd" {
		b.once.Do(func() { close(b.read); <-b.release })
	}
	return value, id, err
}

func TestServerTextResealCannotOverwriteConcurrentEdit(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	if err := env.chat.SetServerSetting(t.Context(), "motd", "old plaintext", 0); err != nil {
		t.Fatal(err)
	}
	b := &pausedTextRead{ChatStore: env.chat, read: make(chan struct{}), release: make(chan struct{})}
	env.srv.deps.Chat = b
	var release sync.Once
	defer release.Do(func() { close(b.release) })
	read, write := make(chan error, 1), make(chan error, 1)
	go func() { _, _, err := env.srv.serverSettingSealed(t.Context(), "motd"); read <- err }()
	select {
	case <-b.read:
	case <-time.After(3 * time.Second):
		t.Fatal("re-seal read missing")
	}
	go func() { write <- env.srv.setServerSettingAndAnnounce(t.Context(), "motd", "new text", "test") }()
	overtook := false
	select {
	case err := <-write:
		overtook = true
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Millisecond):
	}
	release.Do(func() { close(b.release) })
	if err := <-read; err != nil {
		t.Fatal(err)
	}
	if !overtook {
		if err := <-write; err != nil {
			t.Fatal(err)
		}
	}
	got := env.srv.serverSettingPlain(t.Context(), "motd")
	if overtook || got != "new text" {
		t.Fatalf("re-seal overwrote edit: overtook=%v value=%q", overtook, got)
	}
}

type textCommitStore struct {
	ChatStore
	writes    int
	afterSave context.CancelFunc
	writeErr  error
}

func (b *textCommitStore) SetServerSetting(ctx context.Context, key, value string, generation uint32) error {
	b.writes++
	if b.writeErr != nil {
		return b.writeErr
	}
	if err := b.ChatStore.SetServerSetting(ctx, key, value, generation); err != nil {
		return err
	}
	if b.afterSave != nil {
		b.afterSave()
	}
	return nil
}

func TestServerTextCommittedAuditAndCanceledWriter(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	b := &textCommitStore{ChatStore: newFakeChat(), afterSave: cancel}
	audit := &disconnectAuditContext{}
	authority, err := authorization.NewAuthority(t.Context(), serverRoleFixture(), func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	srv := New(&config.Config{}, zap.NewNop(), &Deps{Authority: authority, Chat: b, Groups: audit})
	if err := srv.setServerSettingAndAnnounce(ctx, "server_name", "Saved name", "actor"); err != nil || ctx.Err() == nil || audit.calls != 1 || audit.err != nil {
		t.Fatalf("committed save lost acknowledgement/audit: %v %+v", err, audit)
	}
	srv.serverTextMu.Lock()
	waitCtx, stop := context.WithTimeout(t.Context(), 20*time.Millisecond)
	err = srv.setServerSettingAndAnnounce(waitCtx, "server_name", "Canceled name", "actor")
	stop()
	srv.serverTextMu.Unlock()
	if !errors.Is(err, context.DeadlineExceeded) || b.writes != 1 || audit.calls != 1 {
		t.Fatalf("canceled writer persisted: %v writes=%d audit=%d", err, b.writes, audit.calls)
	}
	b.afterSave, b.writeErr = nil, errors.New("failed persistence")
	if err := srv.setServerSettingAndAnnounce(t.Context(), "server_name", "Failed name", "actor"); !errors.Is(err, b.writeErr) || audit.calls != 1 {
		t.Fatalf("failed save audit: %v %+v", err, audit)
	}
	if got := srv.serverSetting(t.Context(), "server_name"); got != "Saved name" {
		t.Fatal(got)
	}
	for _, key := range []string{"motd", "announcement"} {
		if err := srv.setServerSettingAndAnnounce(t.Context(), key, "Never plaintext", "actor"); !errors.Is(err, errChatKeysUnconfigured) {
			t.Fatal(err)
		}
	}
	if b.writes != 2 || audit.calls != 1 {
		t.Fatal("unconfigured encryption reached storage/audit")
	}
}
