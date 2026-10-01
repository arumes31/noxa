package main

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"noxa/internal/authorization"
	"noxa/internal/broadcast"
	"noxa/internal/chatcrypto"
	"noxa/internal/config"
	"noxa/internal/server"
	"noxa/internal/state"
	"noxa/internal/store"
)

type loadScopeKeys struct {
	server.ScopeKeyStore
	mu   sync.Mutex
	next uint32
	keys map[int64]map[uint32]*store.ScopeKey
}

func (s *loadScopeKeys) AllocScopeKeyID(context.Context, int64) (uint32, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	return s.next, nil
}
func (s *loadScopeKeys) CurrentScopeKey(_ context.Context, scope int64) (*store.ScopeKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var current *store.ScopeKey
	for _, key := range s.keys[scope] {
		if current == nil || key.KeyID > current.KeyID {
			current = key
		}
	}
	return current, nil
}
func (s *loadScopeKeys) GetScopeKey(_ context.Context, scope int64, id uint32) (*store.ScopeKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.keys[scope][id], nil
}
func (s *loadScopeKeys) InsertScopeKey(_ context.Context, scope int64, id uint32, wrapped []byte, kekID uint16) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.keys == nil {
		s.keys = make(map[int64]map[uint32]*store.ScopeKey)
	}
	if s.keys[scope] == nil {
		s.keys[scope] = make(map[uint32]*store.ScopeKey)
	}
	if s.keys[scope][id] != nil {
		return errors.New("duplicate test key")
	}
	s.keys[scope][id] = &store.ScopeKey{KeyID: id, Wrapped: append([]byte(nil), wrapped...), KEKID: kekID}
	return nil
}
func (s *loadScopeKeys) RotateScopeKey(ctx context.Context, scope int64, id uint32, wrapped []byte, kekID uint16) error {
	return s.InsertScopeKey(ctx, scope, id, wrapped, kekID)
}

func loadTestKeys(t *testing.T) (*loadScopeKeys, *chatcrypto.KEKRing) {
	t.Helper()
	kek, err := chatcrypto.LoadKEKRing(filepath.Join(t.TempDir(), "kek.ring"), "", true)
	if err != nil {
		t.Fatal(err)
	}
	return &loadScopeKeys{}, kek
}

type loadRoleStore struct {
	server.RoleStore
	policy authorization.RolePolicy
}

func (s *loadRoleStore) RolePolicy(context.Context) (authorization.RolePolicy, error) {
	return s.policy, nil
}

func TestRoleLoadtestUsesEncryptedAuthorizedChat(t *testing.T) {
	for _, allowed := range []bool{true, false} {
		for _, guest := range []bool{false, true} {
			t.Run(map[bool]string{true: "allowed", false: "denied"}[allowed]+"/"+map[bool]string{true: "guest", false: "account"}[guest], func(t *testing.T) {
				listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				addr := listener.Addr().String()
				_ = listener.Close()
				logger := zap.NewNop()
				sm := state.New(logger)
				sm.AddChannel(&state.Channel{ChannelID: 1, Name: "Load"})
				bc := broadcast.New(logger, sm)
				defer bc.Close()
				grants := []authorization.Capability{authorization.ViewChannel, authorization.ReadHistory, authorization.Connect}
				if allowed {
					grants = append(grants, authorization.SendMessages)
				}
				roles := &loadRoleStore{policy: authorization.RolePolicy{Revision: 1, OwnerID: 99, EveryoneID: 10, Roles: []authorization.Role{{ID: 10, Name: "@everyone", Permissions: grants}}, Channels: []authorization.ChannelPolicy{{ChannelID: 1}}}}
				authority, err := authorization.NewAuthority(t.Context(), roles, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
				if err != nil {
					t.Fatal(err)
				}
				keys, kek := loadTestKeys(t)
				srv := server.New(&config.Config{TCPAddr: addr}, logger, &server.Deps{Auth: fakeAuth{}, State: sm, Broadcast: bc, Roles: roles, Authority: authority, ScopeKeys: keys, ChatKEK: kek})
				if err := srv.EnsureGlobalScopeKey(t.Context()); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(t.Context())
				done := make(chan error, 1)
				go func() { done <- srv.Start(ctx) }()
				defer func() {
					cancel()
					shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
					defer stop()
					if err := srv.Shutdown(shutdown); err != nil {
						t.Error(err)
					}
					if err := <-done; err != nil {
						t.Error(err)
					}
				}()
				deadline := time.Now().Add(3 * time.Second)
				for {
					conn, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", addr)
					if err == nil {
						_ = conn.Close()
						break
					}
					if time.Now().After(deadline) {
						t.Fatal(err)
					}
					time.Sleep(10 * time.Millisecond)
				}
				var st stats
				opts := options{addr: addr, clients: 2, duration: time.Second, uniqueID: "lt-uid", password: "pw", anonymous: guest, authorizationModel: "roles-v1", channel: 1}
				err = run(t.Context(), opts, &st)
				if allowed {
					if err != nil || st.chatParticipants.Load() != 2 || st.chatRecv.Load() < 2 {
						t.Fatalf("authorized run: %v auth=%d sessions=%d confirmed=%d received=%d", err, st.authOK.Load(), st.sessionFail.Load(), st.chatParticipants.Load(), st.chatRecv.Load())
					}
				} else if err == nil || st.authOK.Load() != 2 || st.sessionFail.Load() != 2 || st.chatParticipants.Load() != 0 {
					t.Fatalf("denied chat counted as successful: %v auth=%d sessions=%d confirmed=%d", err, st.authOK.Load(), st.sessionFail.Load(), st.chatParticipants.Load())
				}
			})
		}
	}
}
