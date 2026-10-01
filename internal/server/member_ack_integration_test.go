//go:build integration

package server

import (
	"context"
	"testing"
	"time"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

func TestMemberBanAcknowledgementPostgres(t *testing.T) {
	for _, outcome := range []string{"saved", "pending", "unconfirmed", "denied", "unrequested"} {
		t.Run(outcome, func(t *testing.T) {
			db := integrationManagementStore(t)
			// Match the real TCP fixture identities so the ban's issuer FK is valid.
			if _, err := db.DB().ExecContext(t.Context(), "INSERT INTO users (id,unique_id,nickname) VALUES (1,'admin-uid','Admin'),(2,'user-uid','Owner')"); err != nil {
				t.Fatal(err)
			}
			if outcome == "unconfirmed" {
				if _, err := db.DB().ExecContext(t.Context(), "ALTER TABLE bans ADD CONSTRAINT reject_test_ban CHECK (reason <> 'fixture ban')"); err != nil {
					t.Fatal(err)
				}
			}
			backend := serverRoleFixture()
			backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.Connect}
			authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			env := startTestEnvDeps(t, nil, nil, func(d *Deps) {
				d.Authority, d.Bans, d.Groups = authority, db, db
				if outcome == "pending" {
					d.Voice = failingKickVoice{d.Voice}
				}
			})
			defer env.stop()
			senderUID, targetUID := "user-uid", "admin-uid"
			if outcome == "denied" {
				senderUID, targetUID = targetUID, senderUID
			}
			sender, _ := dialAuthed(t, env.addr, senderUID)
			defer func() { _ = sender.Close() }()
			targetConn, targetID := dialAuthed(t, env.addr, targetUID)
			defer func() { _ = targetConn.Close() }()
			siblingConn, siblingID := dialAuthed(t, env.addr, targetUID)
			defer func() { _ = siblingConn.Close() }()
			target, _ := env.srv.clientByID(targetID)
			sibling, _ := env.srv.clientByID(siblingID)
			msg := netproto.KickClient{ClientID: targetID, Ban: true, Reason: "fixture ban", DurationSeconds: 60, AckRequested: outcome != "unrequested"}
			// Block the INSERT in PostgreSQL, not a mock, before allowing commit.
			tx, err := db.DB().BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback() }()
			if _, err := tx.ExecContext(t.Context(), "LOCK TABLE bans IN SHARE MODE"); err != nil {
				t.Fatal(err)
			}
			send(t, sender, netproto.MsgKickClient, msg)
			send(t, sender, netproto.MsgPing, netproto.Ping{})
			if err := sender.SetReadDeadline(time.Now().Add(8 * time.Second)); err != nil {
				t.Fatal(err)
			}
			done, accepted := make(chan memberActionFence, 1), make(chan struct{}, 4)
			go func() { done <- readMemberActionFence(sender, accepted) }()
			if outcome != "denied" {
				deadline := time.Now().Add(2 * time.Second)
				for {
					var blocked bool
					if err := db.DB().QueryRowContext(t.Context(), "SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE 'INSERT INTO bans%')").Scan(&blocked); err != nil {
						t.Fatal(err)
					}
					if blocked {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("ban INSERT did not wait for the database lock")
					}
					time.Sleep(10 * time.Millisecond)
				}
				select {
				case <-accepted:
					t.Fatal("ban acknowledged before persistence result")
				case got := <-done:
					t.Fatalf("ban returned before persistence: %+v", got)
				case <-time.After(25 * time.Millisecond):
				}
				if !target.isAuthed() || !sibling.isAuthed() {
					t.Fatal("revoked before persistence attempt completed")
				}
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			got := <-done
			if got.err != nil {
				t.Fatal(got.err)
			}
			if outcome == "denied" {
				if len(got.removed) != 0 || len(got.errors) != 1 || got.errors[0].OriginType != uint16(netproto.MsgKickClient) || !target.isAuthed() || !sibling.isAuthed() {
					t.Fatalf("denied result: %+v", got)
				}
			} else {
				if len(got.errors) != 0 || target.isAuthed() || sibling.isAuthed() || !target.sessionRevoked() || !sibling.sessionRevoked() {
					t.Fatalf("revocation outcome: %+v", got)
				}
				if outcome == "unrequested" {
					if len(got.removed) != 0 {
						t.Fatalf("unrequested reply: %+v", got)
					}
				} else {
					persistence := netproto.BanSaved
					if outcome == "unconfirmed" {
						persistence = netproto.BanUnconfirmed
					}
					if len(got.removed) != 1 || !got.removed[0].Matches(msg) || got.removed[0].Persistence != persistence || got.removed[0].CleanupPending != (outcome == "pending") {
						t.Fatalf("ban result: %+v", got)
					}
				}
			}
			var bans int
			if err := db.DB().QueryRowContext(t.Context(), "SELECT count(*) FROM bans WHERE value=$1 AND banned_by=2 AND expires_at > NOW()", targetUID).Scan(&bans); err != nil {
				t.Fatal(err)
			}
			want := 1
			if outcome == "denied" || outcome == "unconfirmed" {
				want = 0
			}
			if bans != want {
				t.Fatalf("persisted bans = %d, want %d", bans, want)
			}
		})
	}
}
