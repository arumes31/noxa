//go:build integration

package server

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/config"
	"noxa/internal/filetransfer"
	"noxa/internal/netproto"
	"noxa/internal/state"
)

func TestIntegrationRemovalRevalidatesAndQuiesces(t *testing.T) {
	for _, action := range []string{"kick", "ban"} {
		t.Run(action, func(t *testing.T) {
			db := integrationManagementStore(t)
			a := auth.New(db, zap.NewNop())
			create := func(name string) auth.IntegrationPrincipal {
				t.Helper()
				uid, err := a.RegisterUser(t.Context(), name, "integration-password")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET integration_enabled=TRUE WHERE unique_id=$1", uid); err != nil {
					t.Fatal(err)
				}
				p, err := a.AuthenticateIntegration(t.Context(), uid, "integration-password", "127.0.0.1")
				if err != nil {
					t.Fatal(err)
				}
				return p
			}
			owner, member := create("removal-owner"), create("removal-member")
			backend := serverRoleFixture()
			backend.policy.OwnerID = owner.UserID()
			backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.Connect, authorization.DownloadFiles}
			authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			sm := state.New(zap.NewNop())
			sm.AddChannel(testChannel(1))
			ft := &disconnectFileTransfer{revoked: make(chan func(filetransfer.Principal, int64, string) bool, 8)}
			srv := New(&config.Config{FileRoot: t.TempDir()}, zap.NewNop(), &Deps{Authority: authority, Auth: a, Bans: db, Groups: db, State: sm, FileTransfer: ft})
			add := func(id string) *Client {
				c := &Client{ID: id, Conn: newBlockingTCPConn()}
				c.setIdentity(member.UniqueID(), id, member.UserID(), false)
				srv.register(c)
				sm.AddClient(&state.Client{ClientID: id, UserID: member.UserID(), UniqueID: member.UniqueID()})
				if err := sm.MoveClient(id, 1); err != nil {
					t.Fatal(err)
				}
				return c
			}
			target, sibling := add("target"), add("sibling")
			remove := func(p auth.IntegrationPrincipal) error {
				if action == "kick" {
					result, err := srv.KickIntegrationMember(t.Context(), p, netproto.MemberKick{ClientID: target.ID, Reason: "Remove session"})
					if err == nil && (result.ClientID != target.ID || result.CleanupPending) {
						return errors.New("unexpected kick outcome")
					}
					return err
				}
				result, err := srv.BanIntegrationMember(t.Context(), p, netproto.MemberBan{ClientID: target.ID, DurationSeconds: 60, Reason: "Suspend account"})
				if err == nil && (result.UniqueID != member.UniqueID() || result.Persistence != netproto.BanSaved || result.CleanupPending) {
					return errors.New("unexpected ban outcome")
				}
				return err
			}
			if err := remove(auth.IntegrationPrincipal{}); !errors.Is(err, auth.ErrIntegrationDenied) {
				t.Fatalf("forged principal: %v", err)
			}
			if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET integration_enabled=FALSE WHERE id=$1", owner.UserID()); err != nil {
				t.Fatal(err)
			}
			if err := remove(owner); !errors.Is(err, auth.ErrIntegrationDenied) {
				t.Fatalf("disabled principal: %v", err)
			}
			if !target.isAuthed() || !sibling.isAuthed() {
				t.Fatal("denied removal changed sessions")
			}
			if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET integration_enabled=TRUE WHERE id=$1", owner.UserID()); err != nil {
				t.Fatal(err)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			fileDone := make(chan error, 1)
			go func() {
				fileDone <- srv.guardRoleFileTransfer(t.Context(), filetransfer.Principal{UserID: member.UserID(), SessionID: target.ID}, 1, "download", func(ctx context.Context) error {
					close(entered)
					select {
					case <-release:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				})
			}()
			<-entered
			removed := make(chan error, 1)
			go func() { removed <- remove(owner) }()
			select {
			case err := <-removed:
				t.Fatalf("removal crossed active file effect: %v", err)
			case <-time.After(25 * time.Millisecond):
			}
			if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET integration_enabled=FALSE WHERE id=$1", owner.UserID()); err != nil {
				t.Fatal(err)
			}
			once.Do(func() { close(release) })
			if err := <-fileDone; err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-removed:
				if !errors.Is(err, auth.ErrIntegrationDenied) {
					t.Fatalf("admission was not rechecked after waiting for the barrier: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("removal did not finish")
			}
			if !target.isAuthed() || !sibling.isAuthed() {
				t.Fatal("late admission denial revoked sessions")
			}
			if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET integration_enabled=TRUE WHERE id=$1", owner.UserID()); err != nil {
				t.Fatal(err)
			}
			if err := remove(owner); err != nil {
				t.Fatal(err)
			}
			if target.isAuthed() {
				t.Fatal("target remained live")
			}
			if sibling.isAuthed() != (action == "kick") {
				t.Fatal("wrong session/account removal scope")
			}
			admission := a.ValidateIntegration(t.Context(), member)
			if action == "ban" && !errors.Is(admission, auth.ErrIntegrationDenied) {
				t.Fatalf("ban did not close integration admission: %v", admission)
			}
			if action == "kick" && admission != nil {
				t.Fatal("session kick banned the account")
			}
			var audits int
			if err := db.DB().QueryRowContext(t.Context(), "SELECT COUNT(*) FROM audit_log WHERE action=$1 AND actor_unique_id=$2 AND target=$3", action, owner.UniqueID(), member.UniqueID()).Scan(&audits); err != nil {
				t.Fatal(err)
			}
			if audits != 1 {
				t.Fatalf("canonical successful audit count: %d", audits)
			}
			if action == "ban" {
				uncertain := create("unconfirmed-member")
				c := &Client{ID: "uncertain", Conn: newBlockingTCPConn()}
				c.setIdentity(uncertain.UniqueID(), "Uncertain", uncertain.UserID(), false)
				srv.register(c)
				sm.AddClient(&state.Client{ClientID: c.ID, UserID: uncertain.UserID(), UniqueID: uncertain.UniqueID()})
				if _, err := db.DB().ExecContext(t.Context(), "ALTER TABLE bans ADD CONSTRAINT reject_test_reason CHECK (reason <> 'force failure')"); err != nil {
					t.Fatal(err)
				}
				result, err := srv.BanIntegrationMember(t.Context(), owner, netproto.MemberBan{ClientID: c.ID, Reason: "force failure"})
				if err != nil || result.Persistence != netproto.BanUnconfirmed || result.UniqueID != uncertain.UniqueID() || result.ExpiresAt != 0 || result.CleanupPending || c.isAuthed() {
					t.Fatalf("lost unconfirmed revocation result: %+v %v", result, err)
				}
			}
		})
	}
}
