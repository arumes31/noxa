package filetransfer

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"go.uber.org/zap"

	"noxa/internal/store"
)

// TestOrphanSweepAgainstPostgres runs the sweep on the real files table,
// including nested folders and version rows, so the path mapping between
// rows and blobs is checked against the production store.
func TestOrphanSweepAgainstPostgres(t *testing.T) {
	url := os.Getenv("NOXA_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("NOXA_TEST_DATABASE_URL is required for this database test")
	}
	db, err := store.New(url, zap.NewNop(), 2, 1, time.Minute)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	var channelID int64
	if err := db.DB().QueryRowContext(ctx, `INSERT INTO channels(name,channel_type) VALUES('orphan sweep',2) RETURNING id`).Scan(&channelID); err != nil {
		t.Fatalf("channel: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.DB().ExecContext(context.Background(), `DELETE FROM channels WHERE id=$1`, channelID)
	})

	s := New(Config{Addr: "127.0.0.1:0", RootDir: t.TempDir()}, db, nil)
	ch := strconv.FormatInt(channelID, 10)
	for _, rec := range []store.FileRecord{
		{ChannelID: channelID, Folder: "", Name: "keep.txt", Size: 1, SHA256: "a"},
		{ChannelID: channelID, Folder: "docs/2026", Name: "plan.md", Size: 1, SHA256: "b"},
		{ChannelID: channelID, Folder: "", Name: "keep.txt.v1", Size: 1, SHA256: "c"},
		{ChannelID: channelID, Folder: "", Name: "missing.txt", Size: 1, SHA256: "d"},
	} {
		if err := db.AddFile(ctx, rec); err != nil {
			t.Fatalf("add %s: %v", rec.Name, err)
		}
	}
	keep := placeFile(t, s, ch+"/keep.txt", 72*time.Hour)
	nested := placeFile(t, s, ch+"/docs/2026/plan.md", 72*time.Hour)
	version := placeFile(t, s, ch+"/keep.txt.v1", 72*time.Hour)
	orphan := placeFile(t, s, ch+"/docs/2026/old.md", 72*time.Hour)

	r := runSweep(t, s)
	for _, path := range []string{keep, nested, version} {
		if !exists(path) {
			t.Errorf("referenced blob removed: %s", path)
		}
	}
	if exists(orphan) {
		t.Errorf("orphan kept: %s", orphan)
	}
	if r.OrphanBlobs != 1 || r.MissingBlobs != 1 {
		t.Fatalf("result = %+v", r)
	}
	if _, err := db.GetFile(ctx, channelID, "", "missing.txt"); err != nil {
		t.Fatalf("a missing blob's row must only be reported by default: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.cfg.RootDir, ch, "docs", "2026")); err != nil {
		t.Fatalf("folder removed: %v", err)
	}
}
