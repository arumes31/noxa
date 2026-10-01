package filetransfer

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"noxa/internal/netproto"
	"noxa/internal/store"
)

func TestTransferAuthorizationRejectsUnboundAndRevokedTokens(t *testing.T) {
	s := New(Config{Addr: "127.0.0.1:0", RootDir: t.TempDir()}, newFakeFileStore(), nil)
	s.SetAccessGuard(func(ctx context.Context, p Principal, scope int64, direction string, effect func(context.Context) error) error {
		return effect(ctx)
	})
	id, token, err := s.InitUpload(t.Context(), 7, "", "legacy.txt", 1, "user", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.beginTransfer(t.Context(), token, id, nil); !errors.Is(err, ErrAccessRevoked) {
		t.Fatalf("unbound token accepted: %v", err)
	}
	ctx := WithPrincipal(t.Context(), Principal{UserID: 2, SessionID: "session"})
	id, token, err = s.InitUpload(ctx, 7, "", "bound.txt", 1, "user", 0)
	if err != nil {
		t.Fatal(err)
	}
	s.RevokeTransfers(func(p Principal, scope int64, direction string) bool { return false })
	if _, _, err := s.beginTransfer(t.Context(), token, id, nil); err == nil {
		t.Fatal("revoked token revived after permissions were restored")
	}
}

func TestConsumedUploadCannotCommitAfterRevocationAndRegrant(t *testing.T) {
	fs := newFakeFileStore()
	addr, s := startServer(t, fs)
	commitReached := make(chan struct{})
	resume := make(chan struct{})
	defer close(resume)
	calls := 0
	s.SetAccessGuard(func(ctx context.Context, p Principal, scope int64, direction string, effect func(context.Context) error) error {
		calls++
		if calls == 3 {
			close(commitReached)
			<-resume
		}
		return effect(ctx)
	})
	content := []byte("must never commit")
	id, token, err := s.InitUpload(WithPrincipal(t.Context(), Principal{UserID: 2, SessionID: "session"}), 7, "", "revoked.txt", int64(len(content)), "user", 0)
	if err != nil {
		t.Fatal(err)
	}
	conn := dialTransfer(t, addr, id, token)
	defer func() { _ = conn.Close() }()
	if err := netproto.WriteFrame(conn, &netproto.Frame{Type: frameChunk, Payload: content}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(conn, frameDigest, digestMsg{SHA256: sha256Hex(content)}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-commitReached:
	case <-time.After(3 * time.Second):
		t.Fatal("upload did not reach commit")
	}
	s.mu.Lock()
	active := s.activeTransfers[id]
	s.mu.Unlock()
	s.RevokeTransfers(func(Principal, int64, string) bool { return false })
	// The guard will allow access again, but this consumed bearer is dead.
	resume <- struct{}{}
	select {
	case <-active.done:
	case <-time.After(3 * time.Second):
		t.Fatal("upload did not finish")
	}
	if _, err := fs.GetFile(t.Context(), 7, "", "revoked.txt"); !errors.Is(err, store.ErrFileNotFound) {
		t.Fatalf("revoked upload committed: %v", err)
	}
}

func TestDownloadAuthorizationStopsBeforeNextChunk(t *testing.T) {
	fs := newFakeFileStore()
	addr, s := startServer(t, fs)
	content := bytes.Repeat([]byte("x"), 2*chunkSize)
	if err := os.MkdirAll(filepath.Join(s.cfg.RootDir, "7"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.cfg.RootDir, "7", "download.txt"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := fs.AddFile(t.Context(), store.FileRecord{ChannelID: 7, Name: "download.txt", Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	s.SetAccessGuard(func(ctx context.Context, p Principal, scope int64, direction string, effect func(context.Context) error) error {
		calls++
		if calls >= 3 {
			return ErrAccessRevoked
		}
		return effect(ctx)
	})
	id, token, err := s.InitDownload(WithPrincipal(t.Context(), Principal{UserID: 2, SessionID: "session"}), 7, "", "download.txt")
	if err != nil {
		t.Fatal(err)
	}
	conn := dialTransfer(t, addr, id, token)
	defer func() { _ = conn.Close() }()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	frame, err := netproto.ReadFrame(conn)
	if err != nil || frame.Type != frameChunk || len(frame.Payload) != chunkSize {
		t.Fatalf("first chunk: %+v %v", frame, err)
	}
	if status := readStatus(t, conn); status.OK {
		t.Fatal("revoked download completed")
	}
}

func TestTransferRevocationClosesIdleUpload(t *testing.T) {
	addr, s := startServer(t, newFakeFileStore())
	activated := make(chan struct{})
	var once sync.Once
	s.SetAccessGuard(func(ctx context.Context, p Principal, scope int64, direction string, effect func(context.Context) error) error {
		err := effect(ctx)
		once.Do(func() { close(activated) })
		return err
	})
	id, token, err := s.InitUpload(WithPrincipal(t.Context(), Principal{UserID: 2, SessionID: "session"}), 7, "", "idle.txt", 10, "user", 0)
	if err != nil {
		t.Fatal(err)
	}
	conn := dialTransfer(t, addr, id, token)
	defer func() { _ = conn.Close() }()
	select {
	case <-activated:
	case <-time.After(3 * time.Second):
		t.Fatal("token did not activate")
	}
	s.mu.Lock()
	active := s.activeTransfers[id]
	s.mu.Unlock()
	if active == nil {
		t.Fatal("missing active transfer")
	}
	s.RevokeTransfers(func(Principal, int64, string) bool { return false })
	select {
	case <-active.done:
	case <-time.After(3 * time.Second):
		t.Fatal("revoked idle upload stayed active")
	}
	entries, err := os.ReadDir(filepath.Join(s.cfg.RootDir, "7"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("idle upload left a partial file")
	}
}

func TestUploadAuthorizationRechecksChunkAndFinalCommit(t *testing.T) {
	for _, denyCall := range []int{2, 3} {
		t.Run(map[int]string{2: "chunk", 3: "final_commit"}[denyCall], func(t *testing.T) {
			fs := newFakeFileStore()
			addr, s := startServer(t, fs)
			calls := 0 // consumed only by this connection's guard
			s.SetAccessGuard(func(ctx context.Context, p Principal, scope int64, direction string, effect func(context.Context) error) error {
				if p.UserID != 2 || p.SessionID != "session" || scope != 7 || direction != "upload" {
					return ErrAccessRevoked
				}
				calls++
				if calls >= denyCall {
					return ErrAccessRevoked
				}
				return effect(ctx)
			})
			content := []byte("private upload")
			id, token, err := s.InitUpload(WithPrincipal(t.Context(), Principal{UserID: 2, SessionID: "session"}), 7, "", "upload.txt", int64(len(content)), "user", 0)
			if err != nil {
				t.Fatal(err)
			}
			conn := dialTransfer(t, addr, id, token)
			defer func() { _ = conn.Close() }()
			if err := netproto.WriteFrame(conn, &netproto.Frame{Type: frameChunk, Payload: content}); err != nil {
				t.Fatal(err)
			}
			if denyCall == 3 {
				if err := writeJSON(conn, frameDigest, digestMsg{SHA256: sha256Hex(content)}); err != nil {
					t.Fatal(err)
				}
			}
			if got := readStatus(t, conn); got.OK {
				t.Fatal("revoked upload committed")
			}
			if _, err := fs.GetFile(t.Context(), 7, "", "upload.txt"); err == nil {
				t.Fatal("revoked upload created metadata")
			}
			entries, err := os.ReadDir(filepath.Join(s.cfg.RootDir, "7"))
			if err != nil || len(entries) != 0 {
				t.Fatalf("revoked upload left files: %v %v", entries, err)
			}
		})
	}
}

func TestUploadCannotTakeAnotherMembersFile(t *testing.T) {
	for _, identical := range []bool{false, true} {
		t.Run(map[bool]string{false: "different_bytes", true: "identical_bytes"}[identical], func(t *testing.T) {
			fs := newFakeFileStore()
			addr, s := startServer(t, fs)
			s.SetAccessGuard(func(ctx context.Context, _ Principal, _ int64, _ string, effect func(context.Context) error) error {
				return effect(ctx)
			})
			content := []byte("new bytes")
			// Issue while the name is free; another member wins it before commit.
			id, token, err := s.InitUpload(WithPrincipal(t.Context(), Principal{UserID: 2, SessionID: "session"}), 7, "", "occupied.txt", int64(len(content)), "attacker", 0)
			if err != nil {
				t.Fatal(err)
			}
			original := []byte("original bytes")
			if identical {
				original = content
			}
			if err := os.MkdirAll(filepath.Join(s.cfg.RootDir, "7"), 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(s.cfg.RootDir, "7", "occupied.txt")
			if err := os.WriteFile(path, original, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := fs.AddFile(t.Context(), store.FileRecord{ChannelID: 7, Name: "occupied.txt", Size: int64(len(original)), Uploader: "owner", SHA256: sha256Hex(original)}); err != nil {
				t.Fatal(err)
			}
			conn := dialTransfer(t, addr, id, token)
			defer func() { _ = conn.Close() }()
			if err := netproto.WriteFrame(conn, &netproto.Frame{Type: frameChunk, Payload: content}); err != nil {
				t.Fatal(err)
			}
			if err := writeJSON(conn, frameDigest, digestMsg{SHA256: sha256Hex(content)}); err != nil {
				t.Fatal(err)
			}
			if readStatus(t, conn).OK {
				t.Error("upload took over another member's file")
			}
			rec, err := fs.GetFile(t.Context(), 7, "", "occupied.txt")
			if err != nil || rec.Uploader != "owner" {
				t.Fatalf("owner changed: %+v %v", rec, err)
			}
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, original) {
				t.Fatalf("blob changed: %q %v", got, err)
			}
		})
	}
}

func TestFileMutationOwnershipUsesCurrentScopedRights(t *testing.T) {
	for _, operation := range []string{"upload", "delete", "move", "link"} {
		for _, actor := range []string{"owner", "manager", "other", "wrong_scope"} {
			t.Run(operation+"/"+actor, func(t *testing.T) {
				fs := newFakeFileStore()
				s := New(Config{Addr: "127.0.0.1:0", RootDir: t.TempDir()}, fs, nil)
				s.SetAccessGuard(func(ctx context.Context, _ Principal, _ int64, _ string, effect func(context.Context) error) error {
					return effect(ctx)
				})
				if err := fs.AddFile(t.Context(), store.FileRecord{ChannelID: 7, Name: "owned.txt", Uploader: "owner"}); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Join(s.cfg.RootDir, "7"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(s.cfg.RootDir, "7", "owned.txt"), []byte("original"), 0o600); err != nil {
					t.Fatal(err)
				}
				scope := int64(7)
				if actor == "wrong_scope" {
					scope = 8
				}
				ctx := WithMutationRights(t.Context(), scope, actor, actor == "manager" || actor == "wrong_scope")
				var err error
				switch operation {
				case "upload":
					_, _, err = s.InitUpload(ctx, 7, "", "owned.txt", 8, actor, 0)
				case "delete":
					err = s.DeleteFile(ctx, 7, "", "owned.txt")
				case "move":
					err = s.MoveFile(ctx, 7, "", "owned.txt", 7, "", "moved.txt")
				case "link":
					_, _, err = s.CreateLink(ctx, 7, "", "owned.txt")
				}
				allowed := actor == "owner" || actor == "manager"
				if allowed && err != nil {
					t.Fatal(err)
				}
				if !allowed && !errors.Is(err, ErrAccessRevoked) {
					t.Fatalf("wrong ownership result: %v", err)
				}
			})
		}
	}
}
