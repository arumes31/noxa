//go:build integration

package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"go.uber.org/zap"

	"noxa/internal/authorization"
	"noxa/internal/filetransfer"
	"noxa/internal/netproto"
	"noxa/internal/store"
)

func TestFileMutationAcknowledgementPostgres(t *testing.T) {
	for _, action := range []string{"delete", "rename", "move"} {
		outcomes := []string{"saved", "database failure", "denied", "unrequested"}
		if action == "delete" {
			outcomes = append(outcomes, "cleanup failure")
		} else {
			outcomes = append(outcomes, "blob failure")
		}
		if action == "move" {
			outcomes = append(outcomes, "destination denied")
		}
		for _, outcome := range outcomes {
			t.Run(action+"/"+outcome, func(t *testing.T) {
				db := integrationManagementStore(t)
				if _, err := db.DB().ExecContext(t.Context(), "INSERT INTO channels (id,name,channel_type) VALUES (1,'Source',2),(2,'Target',2)"); err != nil {
					t.Fatal(err)
				}
				backend := serverRoleFixture()
				backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel}
				backend.policy.Channels = append(backend.policy.Channels, authorization.ChannelPolicy{ChannelID: 2})
				authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
				if err != nil {
					t.Fatal(err)
				}
				root := t.TempDir()
				files := filetransfer.New(filetransfer.Config{Addr: "127.0.0.1:12335", RootDir: root}, db, zap.NewNop())
				defer func() { _ = files.Close() }()
				env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority, d.FileTransfer = authority, files })
				defer env.stop()
				env.state.AddChannel(testChannel(1))
				env.state.AddChannel(testChannel(2))
				uid, uploader := "user-uid", "user-uid"
				if outcome == "denied" || outcome == "destination denied" {
					uid = "admin-uid"
				}
				if outcome == "destination denied" {
					uploader = uid
				}
				if err := db.AddFile(t.Context(), store.FileRecord{ChannelID: 1, Folder: "docs", Name: "report.txt", Size: 7, SHA256: "fixture", Uploader: uploader}); err != nil {
					t.Fatal(err)
				}
				oldPath := filepath.Join(root, "1", "docs", "report.txt")
				if err := os.MkdirAll(filepath.Dir(oldPath), 0700); err != nil {
					t.Fatal(err)
				}
				if outcome == "cleanup failure" || outcome == "blob failure" {
					if err := os.Mkdir(oldPath, 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(oldPath, "keep"), []byte("fixture"), 0600); err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(oldPath, []byte("fixture"), 0600); err != nil {
					t.Fatal(err)
				}
				if outcome == "database failure" {
					const rejectMutation = `CREATE FUNCTION reject_file_mutation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture rejection'; END $$;
CREATE TRIGGER reject_file_mutation BEFORE DELETE OR UPDATE ON files FOR EACH ROW EXECUTE FUNCTION reject_file_mutation()`
					if _, err := db.DB().ExecContext(t.Context(), rejectMutation); err != nil {
						t.Fatal(err)
					}
				}
				conn, id := dialAuthed(t, env.addr, uid)
				defer func() { _ = conn.Close() }()
				kind, msg, want := fileMutationForTest(action, id, outcome != "unrequested")
				tx, err := db.DB().BeginTx(t.Context(), nil)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = tx.Rollback() }()
				if _, err := tx.ExecContext(t.Context(), "LOCK TABLE files IN SHARE MODE"); err != nil {
					t.Fatal(err)
				}
				send(t, conn, kind, msg)
				send(t, conn, netproto.MsgPing, netproto.Ping{})
				if err := conn.SetReadDeadline(time.Now().Add(8 * time.Second)); err != nil {
					t.Fatal(err)
				}
				done, accepted := make(chan fileMutationFence, 1), make(chan struct{}, 4)
				go func() { done <- readFileMutationFence(conn, accepted) }()
				denied := outcome == "denied" || outcome == "destination denied"
				if !denied {
					prefix := "UPDATE files%"
					if action == "delete" {
						prefix = "DELETE FROM files%"
					}
					deadline := time.Now().Add(2 * time.Second)
					for {
						var blocked bool
						if err := db.DB().QueryRowContext(t.Context(), "SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE $1)", prefix).Scan(&blocked); err != nil {
							t.Fatal(err)
						}
						if blocked {
							break
						}
						if time.Now().After(deadline) {
							t.Fatal("file mutation did not wait for database lock")
						}
						time.Sleep(10 * time.Millisecond)
					}
					select {
					case <-accepted:
						t.Fatal("acknowledged before database commit")
					case got := <-done:
						t.Fatalf("returned before database result: %+v", got)
					case <-time.After(25 * time.Millisecond):
					}
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
				got := <-done
				if got.err != nil {
					t.Fatal(got.err)
				}
				success := outcome == "saved" || outcome == "unrequested" || outcome == "cleanup failure"
				if success && outcome != "unrequested" {
					if len(got.saved) != 1 || got.saved[0] != want || len(got.errors) != 0 {
						t.Fatalf("result: %+v", got)
					}
				} else if len(got.saved) != 0 || (len(got.errors) == 0) != success {
					t.Fatalf("result: %+v", got)
				}
				_, sourceErr := db.GetFile(t.Context(), 1, "docs", "report.txt")
				if success {
					if !errors.Is(sourceErr, store.ErrFileNotFound) {
						t.Fatalf("source metadata remains: %v", sourceErr)
					}
				} else if sourceErr != nil {
					t.Fatalf("source metadata changed after rejection: %v", sourceErr)
				}
				if action != "delete" {
					target := int64(1)
					if action == "move" {
						target = 2
					}
					rec, destErr := db.GetFile(t.Context(), target, "archive", "renamed.txt")
					newPath := filepath.Join(root, strconv.FormatInt(target, 10), "archive", "renamed.txt")
					if success {
						data, err := os.ReadFile(newPath)
						if destErr != nil || rec.Uploader != uploader || rec.Size != 7 || err != nil || string(data) != "fixture" {
							t.Fatalf("destination metadata/blob: %+v / %v / %q / %v", rec, destErr, data, err)
						}
					} else {
						if !errors.Is(destErr, store.ErrFileNotFound) {
							t.Fatalf("destination metadata changed: %v", destErr)
						}
						if _, err := os.Stat(newPath); !errors.Is(err, os.ErrNotExist) {
							t.Fatalf("destination blob changed: %v", err)
						}
					}
				}
				if success && outcome != "cleanup failure" {
					if _, err := os.Stat(oldPath); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("old blob remains: %v", err)
					}
				} else {
					if outcome == "cleanup failure" || outcome == "blob failure" {
						oldPath = filepath.Join(oldPath, "keep")
					}
					data, err := os.ReadFile(oldPath)
					if err != nil || string(data) != "fixture" {
						t.Fatalf("old bytes changed: %q / %v", data, err)
					}
				}
			})
		}
	}
}
