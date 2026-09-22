package server

import (
	"context"
	"errors"
	"testing"

	"noxa/internal/authorization"
	"noxa/internal/filetransfer"
	"noxa/internal/netproto"
)

type failingRoleRecordingStop struct{ RecordingBackend }

func (f failingRoleRecordingStop) Stop(int64) error { return errors.New("recorder unavailable") }

func TestRoleReconciliationRevokesAllSubsystemsBeforeReturning(t *testing.T) {
	for _, failMedia := range []bool{false, true} {
		name := "success"
		if failMedia {
			name = "recording stop fails"
		}
		t.Run(name, func(t *testing.T) {
			backend := serverRoleFixture()
			backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.ReadHistory, authorization.Connect, authorization.Speak, authorization.RecordChannel, authorization.DownloadFiles}
			var env *testEnv
			authority, err := authorization.NewAuthority(t.Context(), backend, func(ctx context.Context, before, after *authorization.RoleEvaluator) error {
				return env.srv.reconcileRolePolicy(ctx, before, after)
			})
			if err != nil {
				t.Fatal(err)
			}
			ft := &disconnectFileTransfer{revoked: make(chan func(filetransfer.Principal, int64, string) bool, 8)}
			env = startTestEnvDeps(t, nil, nil, func(d *Deps) {
				d.Authority = authority
				d.FileTransfer = ft
				if failMedia {
					d.Recorder = failingRoleRecordingStop{d.Recorder}
				}
			})
			defer env.stop()
			env.state.AddChannel(testChannel(1))
			pub, _ := testX25519(t)
			reader, info := dialSubscriptionClient(t, env.addr, "admin-uid", pub)
			defer func() { _ = reader.Close() }()
			send(t, reader, netproto.MsgChannelSubscribe, netproto.ChannelSubscribe{Subscribe: true, ChannelIDs: []int64{1}})
			_, keys, errs := readSubscriptionReply(t, reader)
			if len(keys) != 1 || len(errs) != 0 {
				t.Fatalf("subscribe keys=%v errors=%v", keys, errs)
			}
			owner, ownerID := dialAuthed(t, env.addr, "user-uid")
			defer func() { _ = owner.Close() }()
			for _, id := range []string{info.ClientID, ownerID} {
				if err := env.state.MoveClient(id, 1); err != nil {
					t.Fatal(err)
				}
			}
			env.srv.roleRecordingOwners.Store(int64(1), int64(1))
			policy, err := authority.ChangeRolePolicy(t.Context(), 2, authorization.RoleChange{Kind: authorization.RoleUpdate, ExpectedRevision: 1, Role: authorization.Role{ID: 10, Name: "@everyone"}})
			if policy.Revision != 2 || (failMedia && !errors.Is(err, authorization.ErrEnforcementPending)) || (!failMedia && err != nil) {
				t.Fatalf("commit revision=%d error=%v", policy.Revision, err)
			}
			select {
			case allowed := <-ft.revoked:
				if allowed(filetransfer.Principal{UserID: 1, SessionID: info.ClientID}, 1, "download") {
					t.Fatal("revoked file session survived reconciliation")
				}
				if !allowed(filetransfer.Principal{UserID: 2, SessionID: ownerID}, 1, "download") {
					t.Fatal("owner file session was revoked")
				}
			default:
				t.Fatal("file revocation was skipped")
			}
			if ch, _, _ := env.state.ClientChannelState(info.ClientID); ch != 0 {
				t.Fatal("revoked voice membership survived reconciliation")
			}
			if env.state.IsSubscribed(info.ClientID, 1) {
				t.Fatal("chat revocation was skipped after media failure")
			}
			key, _, err := env.srv.chatKeys.current(t.Context(), 1)
			if err != nil || key == keys[0].KeyID {
				t.Fatalf("chat key was not rotated: %d %v", key, err)
			}
			if failMedia {
				if err := authority.WithAccess(t.Context(), 2, 0, authorization.ManageRoles, func(*authorization.RoleEvaluator) error { t.Fatal("owner bypassed failed live revocation"); return nil }); !errors.Is(err, authorization.ErrAuthorizationUnavailable) {
					t.Fatal(err)
				}
			} else {
				env.voice.mu.Lock()
				refreshed := append([]string(nil), env.voice.refreshed...)
				env.voice.mu.Unlock()
				if len(refreshed) != 1 || refreshed[0] != ownerID {
					t.Fatalf("remaining voice refreshes=%v", refreshed)
				}
			}
		})
	}
}
