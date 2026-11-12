package filetransfer

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"noxa/internal/netproto"
	"noxa/internal/store"
)

// dialTransferAt connects and sends an init frame with a resume offset.
func dialTransferAt(t *testing.T, addr, transferID, token string, offset int64) net.Conn {
	t.Helper()
	conn, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if err := writeJSON(conn, frameInit, initMsg{Token: token, TransferID: transferID, Offset: offset}); err != nil {
		t.Fatalf("write init: %v", err)
	}
	return conn
}

func sendChunks(t *testing.T, conn net.Conn, data []byte) {
	t.Helper()
	for off := 0; off < len(data); off += chunkSize {
		end := min(off+chunkSize, len(data))
		if err := netproto.WriteFrame(conn, &netproto.Frame{Type: frameChunk, Payload: data[off:end]}); err != nil {
			t.Fatalf("write chunk: %v", err)
		}
	}
}

// waitPartialSize waits until the server-side partial reached want bytes and
// no transfer holds it any more.
func waitPartialSize(t *testing.T, s *Server, rel string, want int64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		info, err := os.Stat(filepath.Join(s.cfg.RootDir, rel))
		if err == nil && info.Size() == want && !s.partialInUse(rel) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	info, err := os.Stat(filepath.Join(s.cfg.RootDir, rel))
	t.Fatalf("partial %s = %v, %v; want %d bytes and idle", rel, info, err, want)
}

func TestResumableUploadContinuesAfterInterruption(t *testing.T) {
	fs := newFakeFileStore()
	addr, s := startServer(t, fs)
	content := bytes.Repeat([]byte("0123456789abcdef"), 8192) // 128 KiB, 4 chunks
	sum := sha256Hex(content)
	ctx := context.Background()

	id, token, offset, resumable, err := s.InitResumableUpload(ctx, 7, "docs", "big.bin", int64(len(content)), "uid-1", 0, sum)
	if err != nil || !resumable || offset != 0 {
		t.Fatalf("first init = %v resumable=%v offset=%d", err, resumable, offset)
	}
	conn := dialTransferAt(t, addr, id, token, 0)
	half := content[:2*chunkSize]
	sendChunks(t, conn, half)
	_ = conn.Close() // the network drops mid-upload

	rel := resumablePartialPath(7, "docs", "big.bin", "uid-1", int64(len(content)), sum)
	waitPartialSize(t, s, rel, int64(len(half)))
	if fs.addedCount() != 0 {
		t.Fatal("an interrupted upload committed a file row")
	}

	id, token, offset, resumable, err = s.InitResumableUpload(ctx, 7, "docs", "big.bin", int64(len(content)), "uid-1", 0, strings.ToUpper(sum))
	if err != nil || !resumable || offset != int64(len(half)) {
		t.Fatalf("resume init = %v resumable=%v offset=%d, want %d", err, resumable, offset, len(half))
	}
	conn = dialTransferAt(t, addr, id, token, offset)
	defer func() { _ = conn.Close() }()
	sendChunks(t, conn, content[offset:])
	if err := writeJSON(conn, frameDigest, digestMsg{SHA256: sum}); err != nil {
		t.Fatal(err)
	}
	if st := readStatus(t, conn); !st.OK {
		t.Fatalf("resumed upload failed: %s", st.Error)
	}
	got, err := os.ReadFile(s.filePath(7, "docs", "big.bin"))
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("stored file differs (%d bytes, %v)", len(got), err)
	}
	if _, err := os.Stat(filepath.Join(s.cfg.RootDir, rel)); !os.IsNotExist(err) {
		t.Fatalf("partial left behind after commit: %v", err)
	}
	if s.partialInUse(rel) || len(s.partials) != 0 {
		t.Fatalf("partial registry not cleared: %d entries", len(s.partials))
	}
}

func TestResumableUploadRejectsWrongDigestAndDropsPartial(t *testing.T) {
	addr, s := startServer(t, newFakeFileStore())
	content := []byte("resumable content")
	declared := sha256Hex(content)
	id, token, _, resumable, err := s.InitResumableUpload(context.Background(), 7, "", "a.txt", int64(len(content)), "uid-1", 0, declared)
	if err != nil || !resumable {
		t.Fatalf("init = %v resumable=%v", err, resumable)
	}
	conn := dialTransferAt(t, addr, id, token, 0)
	defer func() { _ = conn.Close() }()
	other := []byte("resumable CONTENT") // same size, different bytes
	sendChunks(t, conn, other)
	_ = writeJSON(conn, frameDigest, digestMsg{SHA256: sha256Hex(other)})
	if st := readStatus(t, conn); st.OK || !strings.Contains(st.Error, "checksum") {
		t.Fatalf("status = %+v, want checksum mismatch", st)
	}
	rel := resumablePartialPath(7, "", "a.txt", "uid-1", int64(len(content)), declared)
	if _, err := os.Stat(filepath.Join(s.cfg.RootDir, rel)); !os.IsNotExist(err) {
		t.Fatalf("a partial with the wrong content was kept: %v", err)
	}
}

func TestResumableUploadRefusesOffsetBeyondHeldBytes(t *testing.T) {
	addr, s := startServer(t, newFakeFileStore())
	content := []byte("0123456789")
	id, token, _, _, err := s.InitResumableUpload(context.Background(), 7, "", "a.txt", int64(len(content)), "uid-1", 0, sha256Hex(content))
	if err != nil {
		t.Fatal(err)
	}
	conn := dialTransferAt(t, addr, id, token, 5) // the server holds nothing yet
	defer func() { _ = conn.Close() }()
	if st := readStatus(t, conn); st.OK || !strings.Contains(st.Error, "beyond") {
		t.Fatalf("status = %+v, want offset refusal", st)
	}
}

func TestSingleShotUploadRefusesOffset(t *testing.T) {
	addr, s := startServer(t, newFakeFileStore())
	id, token, err := s.InitUpload(context.Background(), 7, "", "a.txt", 10, "uid-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	conn := dialTransferAt(t, addr, id, token, 4)
	defer func() { _ = conn.Close() }()
	if st := readStatus(t, conn); st.OK || !strings.Contains(st.Error, "cannot resume") {
		t.Fatalf("status = %+v, want refusal", st)
	}
}

func TestResumablePartialsAreLimitedPerUploader(t *testing.T) {
	s := New(Config{Addr: "127.0.0.1:0", RootDir: t.TempDir()}, newFakeFileStore(), nil)
	ctx := context.Background()
	for i := 0; i < maxResumablePartialsPerUploader; i++ {
		name := "f" + string(rune('a'+i)) + ".bin"
		if _, _, _, resumable, err := s.InitResumableUpload(ctx, 7, "", name, 10, "uid-1", 0, sha256Hex([]byte(name))); err != nil || !resumable {
			t.Fatalf("upload %d: %v resumable=%v", i, err, resumable)
		}
	}
	_, token, offset, resumable, err := s.InitResumableUpload(ctx, 7, "", "extra.bin", 10, "uid-1", 0, sha256Hex([]byte("extra")))
	if err != nil || resumable || offset != 0 || token == "" {
		t.Fatalf("over the limit = %v resumable=%v offset=%d token=%q, want a plain upload", err, resumable, offset, token)
	}
	// Another uploader is unaffected.
	if _, _, _, resumable, err := s.InitResumableUpload(ctx, 7, "", "other.bin", 10, "uid-2", 0, sha256Hex([]byte("o"))); err != nil || !resumable {
		t.Fatalf("other uploader: %v resumable=%v", err, resumable)
	}
	// Partials also count against the uploader quota before they commit.
	s2 := New(Config{Addr: "127.0.0.1:0", RootDir: t.TempDir()}, newFakeFileStore(), nil)
	if _, _, _, resumable, _ := s2.InitResumableUpload(ctx, 7, "", "a.bin", 700<<10, "uid-1", 1, sha256Hex([]byte("a"))); !resumable {
		t.Fatal("first partial within quota was not resumable")
	}
	if _, _, _, resumable, _ := s2.InitResumableUpload(ctx, 7, "", "b.bin", 700<<10, "uid-1", 1, sha256Hex([]byte("b"))); resumable {
		t.Fatal("partials beyond the uploader quota were resumable")
	}
}

func TestResumableUploadTakesOverAStaleConnection(t *testing.T) {
	addr, s := startServer(t, newFakeFileStore())
	content := bytes.Repeat([]byte("x"), 3*chunkSize)
	sum := sha256Hex(content)
	ctx := context.Background()
	id, token, _, _, err := s.InitResumableUpload(ctx, 7, "", "a.bin", int64(len(content)), "uid-1", 0, sum)
	if err != nil {
		t.Fatal(err)
	}
	stale := dialTransferAt(t, addr, id, token, 0)
	defer func() { _ = stale.Close() }()
	sendChunks(t, stale, content[:chunkSize])
	rel := resumablePartialPath(7, "", "a.bin", "uid-1", int64(len(content)), sum)
	deadline := time.Now().Add(3 * time.Second)
	for !s.partialInUse(rel) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	// The stale connection never closes on the client side (a dead network);
	// the next attempt must not wait for the server's read timeout.
	id, token, offset, _, err := s.InitResumableUpload(ctx, 7, "", "a.bin", int64(len(content)), "uid-1", 0, sum)
	if err != nil {
		t.Fatal(err)
	}
	fresh := dialTransferAt(t, addr, id, token, offset)
	defer func() { _ = fresh.Close() }()
	sendChunks(t, fresh, content[offset:])
	_ = writeJSON(fresh, frameDigest, digestMsg{SHA256: sum})
	if st := readStatus(t, fresh); !st.OK {
		t.Fatalf("takeover upload failed: %s (offset %d)", st.Error, offset)
	}
}

func TestPartialNamesAreRecognized(t *testing.T) {
	rel := resumablePartialPath(7, "docs", "a.bin", "uid-1", 10, strings.Repeat("ab", 32))
	if !isResumablePartialName(filepath.Base(rel)) || isLegacyPartialName(filepath.Base(rel)) {
		t.Fatalf("resumable partial %q misclassified", rel)
	}
	if !isLegacyPartialName("a.bin.part-0123456789abcdef") || isResumablePartialName("a.bin.part-0123456789abcdef") {
		t.Fatal("legacy partial misclassified")
	}
	for _, name := range []string{"a.bin", ".part-0123456789abcdef", "a.part-XYZ", "a.bin.part-r0123"} {
		if isResumablePartialName(name) || isLegacyPartialName(name) {
			t.Fatalf("%q classified as a partial", name)
		}
	}
	if resumablePartialPath(7, "docs", "a.bin", "uid-1", 10, strings.Repeat("ab", 32)) == resumablePartialPath(7, "docs", "a.bin", "uid-2", 10, strings.Repeat("ab", 32)) {
		t.Fatal("two uploaders share a partial")
	}
}

func TestAbandonedReservationsDoNotBlockResume(t *testing.T) {
	s := New(Config{Addr: "127.0.0.1:0", RootDir: t.TempDir(), MaxSizeMB: 1}, newFakeFileStore(), nil)
	ctx := context.Background()
	// A refused token keeps no reservation.
	if _, _, _, _, err := s.InitResumableUpload(ctx, 7, "", "huge.bin", 5<<20, "uid-1", 0, sha256Hex([]byte("huge"))); err == nil {
		t.Fatal("an oversized upload was issued")
	}
	if len(s.partials) != 0 {
		t.Fatalf("refused upload left %d reservations", len(s.partials))
	}
	// Tokens issued but never used expire; their reservations stop counting.
	for i := 0; i < maxResumablePartialsPerUploader; i++ {
		name := "f" + string(rune('a'+i)) + ".bin"
		if _, _, _, resumable, err := s.InitResumableUpload(ctx, 7, "", name, 10, "uid-1", 0, sha256Hex([]byte(name))); err != nil || !resumable {
			t.Fatalf("upload %d: %v resumable=%v", i, err, resumable)
		}
	}
	s.mu.Lock()
	for _, tr := range s.transfers {
		tr.Expires = time.Now().Add(-time.Second)
	}
	s.mu.Unlock()
	if _, _, _, resumable, err := s.InitResumableUpload(ctx, 7, "", "next.bin", 10, "uid-1", 0, sha256Hex([]byte("next"))); err != nil || !resumable {
		t.Fatalf("expired reservations still block resume: %v resumable=%v", err, resumable)
	}
	if len(s.partials) != 1 {
		t.Fatalf("registry holds %d entries, want only the live reservation", len(s.partials))
	}
}

func TestReservationsWithPartialsOnDiskSurvivePruning(t *testing.T) {
	s := New(Config{Addr: "127.0.0.1:0", RootDir: t.TempDir()}, newFakeFileStore(), nil)
	ctx := context.Background()
	sum := sha256Hex([]byte("kept"))
	if _, _, _, resumable, err := s.InitResumableUpload(ctx, 7, "", "kept.bin", 10, "uid-1", 0, sum); err != nil || !resumable {
		t.Fatal(err)
	}
	rel := resumablePartialPath(7, "", "kept.bin", "uid-1", 10, sum)
	if err := os.MkdirAll(filepath.Dir(filepath.Join(s.cfg.RootDir, rel)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.cfg.RootDir, rel), []byte("half"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	for _, tr := range s.transfers {
		tr.Expires = time.Now().Add(-time.Second)
	}
	s.mu.Unlock()
	s.pruneIdlePartials("", time.Now())
	if _, ok := s.partials[rel]; !ok {
		t.Fatal("a partial with bytes on disk lost its reservation")
	}
	_, _, offset, resumable, err := s.InitResumableUpload(ctx, 7, "", "kept.bin", 10, "uid-1", 0, sum)
	if err != nil || !resumable || offset != 4 {
		t.Fatalf("resume = %v resumable=%v offset=%d, want 4", err, resumable, offset)
	}
}

// A stored file named like an upload partial could be dedup-linked to another
// member's blob and then truncated by a resume (audit #1): such names can no
// longer be created, by upload or by rename.
func TestPartialNamesCannotBeCreated(t *testing.T) {
	fs := newFakeFileStore()
	_, s := startServer(t, fs)
	ctx := context.Background()
	for _, name := range []string{"a.txt" + resumablePartialPrefix + strings.Repeat("ab", 16), "a.txt" + legacyPartialPrefix + strings.Repeat("cd", 8)} {
		if _, _, err := s.InitUpload(ctx, 7, "", name, 10, "uid-1", 0); !errors.Is(err, ErrInvalidName) {
			t.Fatalf("upload as %q = %v, want ErrInvalidName", name, err)
		}
		if err := s.RenameFile(ctx, 7, "", "kept.txt", "", name); !errors.Is(err, ErrInvalidName) {
			t.Fatalf("rename to %q = %v, want ErrInvalidName", name, err)
		}
	}
	// Ordinary names that merely contain the marker stay valid.
	if _, _, err := s.InitUpload(ctx, 7, "", "notes.part-review.txt", 10, "uid-1", 0); err != nil {
		t.Fatalf("ordinary name refused: %v", err)
	}
}

// A partial path that a stored file row already names (planted before such
// names were refused) is never resumed into, so the linked blob stays intact.
func TestResumeNeverWritesIntoAStoredFile(t *testing.T) {
	fs := newFakeFileStore()
	_, s := startServer(t, fs)
	ctx := context.Background()
	victim := []byte("victim report contents")
	victimPath := s.filePath(7, "", "report.pdf")
	if err := os.MkdirAll(filepath.Dir(victimPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(victimPath, victim, 0o600); err != nil {
		t.Fatal(err)
	}
	_ = fs.AddFile(ctx, store.FileRecord{ChannelID: 7, Name: "report.pdf", Size: int64(len(victim)), SHA256: sha256Hex(victim), Uploader: "victim"})

	sum := sha256Hex([]byte("attacker bytes"))
	rel := resumablePartialPath(7, "", "a.txt", "attacker", int64(len(victim)), sum)
	if err := os.Link(victimPath, filepath.Join(s.cfg.RootDir, rel)); err != nil {
		t.Skipf("hard links unsupported here: %v", err)
	}
	_ = fs.AddFile(ctx, store.FileRecord{ChannelID: 7, Name: filepath.Base(rel), Size: int64(len(victim)), SHA256: sha256Hex(victim), Uploader: "attacker"})

	_, _, _, resumable, err := s.InitResumableUpload(ctx, 7, "", "a.txt", int64(len(victim)), "attacker", 0, sum)
	if err != nil {
		t.Fatal(err)
	}
	if resumable {
		t.Fatal("resume offered into a stored file's path")
	}
	if got, err := os.ReadFile(victimPath); err != nil || !bytes.Equal(got, victim) {
		t.Fatalf("victim file changed: %q, %v", got, err)
	}
}
