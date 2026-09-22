package server

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

func TestRoleKickStopsPublishedLoginBeforeHandshakeResumes(t *testing.T) {
	authority, err := authorization.NewAuthority(t.Context(), serverRoleFixture(), func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	entered, resume := make(chan struct{}), make(chan struct{})
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) {
		d.Authority = authority
		d.ICEServers = func(string) []netproto.ICEServer { close(entered); <-resume; return nil }
	})
	defer env.stop()
	if err := env.spool.SpoolMessage(t.Context(), 2, 1, "user-uid", "ciphertext"); err != nil {
		t.Fatal(err)
	}
	var resumeOnce sync.Once
	defer resumeOnce.Do(func() { close(resume) })
	client := &Client{ID: "interrupted-login", Conn: newBlockingTCPConn()}
	client.beginAuthorizationNegotiation(netproto.AuthorizationModelRolesV1)
	env.srv.register(client)
	done := make(chan error, 1)
	go func() {
		done <- env.srv.finishAuth(t.Context(), client, authIdentity{uniqueID: "admin-uid", nickname: "Admin", userID: 1})
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("login did not reach handshake pause")
	}
	err = env.srv.withExclusiveRolePolicy(t.Context(), func(ctx context.Context) error {
		env.srv.roleMetadataMu.Lock()
		defer env.srv.roleMetadataMu.Unlock()
		_, err := env.srv.kickRoleMember(ctx, ctx.Value(roleLeaseKey{}).(roleLease).evaluator, 2, "user-uid", "", client.ID, "Leave")
		return err
	})
	resumeOnce.Do(func() { close(resume) })
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, authorization.ErrRoleForbidden) {
			t.Fatalf("revoked login resumed handshake: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("revoked login deadlocked")
	}
	if _, ok := env.state.GetClient(client.ID); ok || client.isAuthed() {
		t.Fatal("removed session was republished")
	}
	if env.spool.pendingCount() != 1 {
		t.Fatal("revoked handshake consumed offline messages")
	}
}

type pausedRoleAdmissionAuth struct {
	AuthBackend
	verified   chan struct{}
	resume     chan struct{}
	ban        atomic.Bool
	fail       bool
	finalCheck func()
}

func (a *pausedRoleAdmissionAuth) AuthenticateIdentifier(ctx context.Context, _ string, password string) (*auth.User, error) {
	user, err := a.AuthBackend.AuthenticateIdentifier(ctx, "admin-uid", password)
	close(a.verified)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-a.resume:
	}
	return user, err
}

func (a *pausedRoleAdmissionAuth) LookupActiveBan(ctx context.Context, uid, ip string) (*auth.Ban, error) {
	if a.ban.Load() && uid == "admin-uid" {
		if a.finalCheck != nil {
			a.finalCheck()
		}
		if a.fail {
			return nil, errors.New("private database failure")
		}
		return &auth.Ban{Reason: "account suspended"}, nil
	}
	return a.AuthBackend.LookupActiveBan(ctx, uid, ip)
}

func TestRoleFinalAdmissionRechecksCanonicalBan(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "ban committed during authentication", true: "final lookup fails"}[fail], func(t *testing.T) {
			authority, err := authorization.NewAuthority(t.Context(), serverRoleFixture(), func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			a := &pausedRoleAdmissionAuth{verified: make(chan struct{}), resume: make(chan struct{}), fail: fail}
			var finalChecks atomic.Int32
			var env *testEnv
			a.finalCheck = func() {
				finalChecks.Add(1)
				if env.srv.roleMetadataMu.TryLock() {
					env.srv.roleMetadataMu.Unlock()
					t.Error("final admission did not retain the ban/membership barrier")
				}
			}
			env = startTestEnvDeps(t, nil, nil, func(d *Deps) { a.AuthBackend = d.Auth; d.Auth, d.Authority = a, authority })
			defer env.stop()
			conn := dialRetry(t, env.addr)
			defer func() { _ = conn.Close() }()
			send(t, conn, netproto.MsgAuthenticate, netproto.Authenticate{Username: "member-alias", Password: "pw", AuthorizationModels: []string{netproto.AuthorizationModelRolesV1}})
			select {
			case <-a.verified:
			case <-t.Context().Done():
				t.Fatal(t.Context().Err())
			}
			env.srv.roleMetadataMu.Lock()
			a.ban.Store(true)
			env.srv.roleMetadataMu.Unlock()
			close(a.resume)
			var response netproto.AuthResponse
			if err := netproto.Decode(readOfType(t, conn, netproto.MsgAuthResponse), &response); err != nil {
				t.Fatal(err)
			}
			if response.OK || env.state.ClientCount() != 0 || finalChecks.Load() != 1 {
				t.Fatalf("late banned admission became live: response=%+v clients=%d checks=%d", response, env.state.ClientCount(), finalChecks.Load())
			}
		})
	}
}
