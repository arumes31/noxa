// wave7_test.go exercises the wave-7 file features end-to-end over real TCP:
// folders, version rotation (264), content dedup (275), and download links
// (267).
package filetransfer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"noxa/internal/netproto"
)

// uploadOne performs a complete upload of content under folder/name.
func uploadOne(t *testing.T, addr string, s *Server, folder, name string, content []byte) {
	t.Helper()
	id, token, err := s.InitUpload(context.Background(), 7, folder, name, int64(len(content)), "uid-1", 0)
	if err != nil {
		t.Fatalf("InitUpload %s/%s: %v", folder, name, err)
	}
	conn := dialTransfer(t, addr, id, token)
	defer func() { _ = conn.Close() }()
	if err := netproto.WriteFrame(conn, &netproto.Frame{Type: frameChunk, Payload: content}); err != nil {
		t.Fatalf("write chunk: %v", err)
	}
	if err := writeJSON(conn, frameDigest, digestMsg{SHA256: sha256Hex(content)}); err != nil {
		t.Fatalf("write digest: %v", err)
	}
	if st := readStatus(t, conn); !st.OK {
		t.Fatalf("upload %s/%s: status = %+v", folder, name, st)
	}
}

// TestFolderUpload verifies files land in folder paths and traversal is
// rejected (261).
func TestFolderUpload(t *testing.T) {
	fs := newFakeFileStore()
	addr, s := startServer(t, fs)

	uploadOne(t, addr, s, "docs/2024", "report.txt", []byte("foldered"))

	p := filepath.Join(s.cfg.RootDir, "7", "docs", "2024", "report.txt")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("file not at folder path: %v", err)
	}
	rec, err := fs.GetFile(context.Background(), 7, "docs/2024", "report.txt")
	if err != nil || rec.Folder != "docs/2024" {
		t.Fatalf("record = %+v, err=%v", rec, err)
	}

	for _, bad := range []string{
		"..", "../etc", `..\etc`, "a//b", "/abs", "a/", `a\b`,
		`C:\Windows`, `\\server\share`,
	} {
		if _, err := sanitizeFolder(bad); err == nil {
			t.Errorf("sanitizeFolder(%q) accepted", bad)
		}
	}
	for _, ok := range []string{"", "docs", "docs/2024", "a-b_c.d/e"} {
		if _, err := sanitizeFolder(ok); err != nil {
			t.Errorf("sanitizeFolder(%q) = %v", ok, err)
		}
	}
}

// TestVersionRotation verifies overwriting rotates old versions (264).
func TestVersionRotation(t *testing.T) {
	fs := newFakeFileStore()
	addr, s := startServer(t, fs)

	// Four versions of the same name: current + .v1..v3 kept, oldest dropped.
	for i := 1; i <= 4; i++ {
		uploadOne(t, addr, s, "", "log.txt", []byte(fmt.Sprintf("version-%d", i)))
	}

	for _, want := range []struct {
		name    string
		content string
	}{
		{"log.txt", "version-4"},
		{"log.txt.v1", "version-3"},
		{"log.txt.v2", "version-2"},
		{"log.txt.v3", "version-1"},
	} {
		raw, err := os.ReadFile(filepath.Join(s.cfg.RootDir, "7", want.name))
		if err != nil {
			t.Fatalf("read %s: %v", want.name, err)
		}
		if string(raw) != want.content {
			t.Fatalf("%s = %q, want %q", want.name, raw, want.content)
		}
		if _, err := fs.GetFile(context.Background(), 7, "", want.name); err != nil {
			t.Fatalf("store record for %s: %v", want.name, err)
		}
	}

	// Fifth upload: version-1 falls off the end.
	uploadOne(t, addr, s, "", "log.txt", []byte("version-5"))
	raw, _ := os.ReadFile(filepath.Join(s.cfg.RootDir, "7", "log.txt.v3"))
	if string(raw) != "version-2" {
		t.Fatalf(".v3 = %q, want version-2 (version-1 pruned)", raw)
	}
	versions, err := s.ListFileVersions(context.Background(), 7, "", "log.txt")
	if err != nil || len(versions) != 3 {
		t.Fatalf("versions = %+v, err=%v", versions, err)
	}

	// Identical re-upload of the current file: no rotation happens.
	uploadOne(t, addr, s, "", "log.txt", []byte("version-5"))
	versions, _ = s.ListFileVersions(context.Background(), 7, "", "log.txt")
	if len(versions) != 3 {
		t.Fatalf("identical re-upload rotated versions: %+v", versions)
	}
}

// TestUploadDedup verifies an identical blob is hard-linked instead of
// stored twice (275).
func TestUploadDedup(t *testing.T) {
	fs := newFakeFileStore()
	addr, s := startServer(t, fs)

	content := []byte("identical content for dedup")
	uploadOne(t, addr, s, "", "a.bin", content)
	uploadOne(t, addr, s, "docs", "b.bin", content)

	aInfo, err := os.Stat(filepath.Join(s.cfg.RootDir, "7", "a.bin"))
	if err != nil {
		t.Fatalf("stat a.bin: %v", err)
	}
	bInfo, err := os.Stat(filepath.Join(s.cfg.RootDir, "7", "docs", "b.bin"))
	if err != nil {
		t.Fatalf("stat b.bin: %v", err)
	}
	if !os.SameFile(aInfo, bInfo) {
		t.Fatal("dedup did not hard-link the blobs")
	}
	// Both rows exist independently.
	if _, err := fs.GetFile(context.Background(), 7, "", "a.bin"); err != nil {
		t.Fatalf("a.bin record: %v", err)
	}
	if _, err := fs.GetFile(context.Background(), 7, "docs", "b.bin"); err != nil {
		t.Fatalf("b.bin record: %v", err)
	}
}

// TestDownloadLink verifies the link registry serves the file and rejects
// unknown tokens (267).
func TestDownloadLink(t *testing.T) {
	fs := newFakeFileStore()
	addr, s := startServer(t, fs)

	uploadOne(t, addr, s, "", "shared.txt", []byte("link me"))

	token, expires, err := s.CreateLink(context.Background(), 7, "", "shared.txt")
	if err != nil {
		t.Fatalf("CreateLink: %v", err)
	}
	if expires.IsZero() {
		t.Fatal("no expiry on link")
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/dl/"+token, nil)
	s.Links().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("link status = %d", rec.Code)
	}
	body, _ := io.ReadAll(rec.Result().Body)
	if string(body) != "link me" {
		t.Fatalf("link body = %q", body)
	}

	rec = httptest.NewRecorder()
	s.Links().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/dl/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown token status = %d", rec.Code)
	}

	// Missing file: link creation fails.
	if _, _, err := s.CreateLink(context.Background(), 7, "", "ghost.txt"); err == nil {
		t.Fatal("link for missing file succeeded")
	}
}

func TestDownloadLinkRejectsSymlinkSwap(t *testing.T) {
	fs := newFakeFileStore()
	addr, s := startServer(t, fs)
	uploadOne(t, addr, s, "", "shared.txt", []byte("safe"))

	token, _, err := s.CreateLink(context.Background(), 7, "", "shared.txt")
	if err != nil {
		t.Fatalf("CreateLink: %v", err)
	}
	path := s.filePath(7, "", "shared.txt")
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove linked file: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("outside secret"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Skipf("symlink creation is not supported: %v", err)
	}

	rec := httptest.NewRecorder()
	s.Links().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/dl/"+token, nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("symlink-swapped link status = %d, want 404", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "outside secret") {
		t.Fatal("download link disclosed a symlink target outside the file root")
	}
}

// uploadStatus uploads content and returns the server's final status.
func uploadStatus(t *testing.T, addr string, s *Server, folder, name string, content []byte) statusMsg {
	t.Helper()
	id, token, err := s.InitUpload(context.Background(), 7, folder, name, int64(len(content)), "uid-1", 0)
	if err != nil {
		t.Fatalf("InitUpload %s/%s: %v", folder, name, err)
	}
	conn := dialTransfer(t, addr, id, token)
	defer func() { _ = conn.Close() }()
	if err := netproto.WriteFrame(conn, &netproto.Frame{Type: frameChunk, Payload: content}); err != nil {
		t.Fatalf("write chunk: %v", err)
	}
	if err := writeJSON(conn, frameDigest, digestMsg{SHA256: sha256Hex(content)}); err != nil {
		t.Fatalf("write digest: %v", err)
	}
	return readStatus(t, conn)
}

// A blob rename the OS refuses during version rotation (a file held open on
// Windows) must not leave the row and the blob apart (audit #10): the row is
// moved back and the upload fails, so the current version stays reachable.
func TestVersionRotationRollsBackWhenTheDiskRenameFails(t *testing.T) {
	fs := newFakeFileStore()
	addr, s := startServer(t, fs)
	uploadOne(t, addr, s, "", "log.txt", []byte("version-1"))

	orig := rotateRename
	rotateRename = func(*os.Root, string, string) error { return errors.New("file in use") }
	t.Cleanup(func() { rotateRename = orig })

	if st := uploadStatus(t, addr, s, "", "log.txt", []byte("version-2")); st.OK {
		t.Fatal("upload succeeded although rotation failed")
	}
	if _, err := fs.GetFile(context.Background(), 7, "", "log.txt"); err != nil {
		t.Fatalf("current row lost: %v", err)
	}
	if _, err := fs.GetFile(context.Background(), 7, "", "log.txt.v1"); err == nil {
		t.Fatal("row rotated to .v1 although its blob was not")
	}
	raw, err := os.ReadFile(filepath.Join(s.cfg.RootDir, "7", "log.txt"))
	if err != nil || string(raw) != "version-1" {
		t.Fatalf("current blob = %q, %v", raw, err)
	}
}

// A rotation step that fails after earlier steps succeeded restores every
// version, including the oldest one set aside (CodeRabbit on #309).
func TestFailedRotationRestoresEveryVersion(t *testing.T) {
	fs := newFakeFileStore()
	addr, s := startServer(t, fs)
	for i := 1; i <= 4; i++ { // log.txt + .v1..v3: rotation is full
		uploadOne(t, addr, s, "", "log.txt", []byte(fmt.Sprintf("version-%d", i)))
	}

	orig := rotateRename
	rotateRename = func(root *os.Root, oldPath, newPath string) error {
		if filepath.Base(oldPath) == "log.txt" { // the last step fails
			return errors.New("file in use")
		}
		return orig(root, oldPath, newPath)
	}
	t.Cleanup(func() { rotateRename = orig })

	if st := uploadStatus(t, addr, s, "", "log.txt", []byte("version-5")); st.OK {
		t.Fatal("upload succeeded although rotation failed")
	}
	for name, want := range map[string]string{"log.txt": "version-4", "log.txt.v1": "version-3", "log.txt.v2": "version-2", "log.txt.v3": "version-1"} {
		if _, err := fs.GetFile(context.Background(), 7, "", name); err != nil {
			t.Fatalf("row %s lost: %v", name, err)
		}
		raw, err := os.ReadFile(filepath.Join(s.cfg.RootDir, "7", name))
		if err != nil || string(raw) != want {
			t.Fatalf("%s = %q, %v; want %q", name, raw, err, want)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(s.cfg.RootDir, "7"))
	for _, e := range entries {
		if strings.Contains(e.Name(), ".rotating-") {
			t.Fatalf("set-aside version left behind: %s", e.Name())
		}
	}
}

// A row that cannot be written after the new blob was published undoes the
// rotation, so the previous file is current again (CodeRabbit on #309).
func TestFailedPublishRestoresThePreviousFile(t *testing.T) {
	fs := newFakeFileStore()
	addr, s := startServer(t, fs)
	uploadOne(t, addr, s, "", "log.txt", []byte("version-1"))

	fs.mu.Lock()
	fs.addErr = errors.New("database unavailable")
	fs.mu.Unlock()
	if st := uploadStatus(t, addr, s, "", "log.txt", []byte("version-2")); st.OK {
		t.Fatal("upload succeeded although its row was not written")
	}
	if _, err := fs.GetFile(context.Background(), 7, "", "log.txt"); err != nil {
		t.Fatalf("current row lost: %v", err)
	}
	if _, err := fs.GetFile(context.Background(), 7, "", "log.txt.v1"); err == nil {
		t.Fatal("rotation kept although the upload failed")
	}
	raw, err := os.ReadFile(filepath.Join(s.cfg.RootDir, "7", "log.txt"))
	if err != nil || string(raw) != "version-1" {
		t.Fatalf("current blob = %q, %v", raw, err)
	}
}
