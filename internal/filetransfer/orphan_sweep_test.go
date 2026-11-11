package filetransfer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"noxa/internal/store"
)

// placeFile writes content at RootDir/rel with the given age.
func placeFile(t *testing.T, s *Server, rel string, age time.Duration) string {
	t.Helper()
	abs := filepath.Join(s.cfg.RootDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte("content of "+rel), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-age)
	if err := os.Chtimes(abs, old, old); err != nil {
		t.Fatal(err)
	}
	return abs
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// runSweep runs one complete pass.
func runSweep(t *testing.T, s *Server) OrphanSweepResult {
	t.Helper()
	s.mu.Lock()
	s.sweep.next = time.Time{}
	s.mu.Unlock()
	if err := s.PruneOrphanedFiles(context.Background(), time.Now()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	r := s.LastOrphanSweep()
	if !r.PassCompleted {
		t.Fatal("sweep pass did not complete")
	}
	return r
}

func TestOrphanSweepRemovesOnlyUnreferencedStaleFiles(t *testing.T) {
	fs := newFakeFileStore()
	s := New(Config{Addr: "127.0.0.1:0", RootDir: t.TempDir()}, fs, nil)
	ctx := context.Background()
	add := func(folder, name string) {
		if err := fs.AddFile(ctx, store.FileRecord{ChannelID: 7, Folder: folder, Name: name, Size: 1, SHA256: "x"}); err != nil {
			t.Fatal(err)
		}
	}

	kept := placeFile(t, s, "7/report.txt", 72*time.Hour)
	add("", "report.txt")
	keptNested := placeFile(t, s, "7/docs/sub/plan.md", 72*time.Hour)
	add("docs/sub", "plan.md")
	keptVersion := placeFile(t, s, "7/report.txt.v1", 72*time.Hour)
	add("", "report.txt.v1")

	orphan := placeFile(t, s, "7/docs/forgotten.bin", 48*time.Hour)
	freshOrphan := placeFile(t, s, "7/new-upload.bin", time.Minute) // could be committing right now
	staleLegacy := placeFile(t, s, "7/a.bin.part-0123456789abcdef", 2*time.Hour)
	freshLegacy := placeFile(t, s, "7/b.bin.part-fedcba9876543210", 10*time.Minute)
	staleResumable := placeFile(t, s, "7/c.bin.part-r"+strings.Repeat("a", 32), 30*time.Hour)
	freshResumable := placeFile(t, s, "7/d.bin.part-r"+strings.Repeat("b", 32), 2*time.Hour)
	assetNamespace := placeFile(t, s, "assets/logo.png", 90*24*time.Hour)
	nonCanonical := placeFile(t, s, "007/old.bin", 90*24*time.Hour)

	r := runSweep(t, s)
	for _, path := range []string{kept, keptNested, keptVersion, freshOrphan, freshLegacy, freshResumable, assetNamespace, nonCanonical} {
		if !exists(path) {
			t.Errorf("removed %s", path)
		}
	}
	for _, path := range []string{orphan, staleLegacy, staleResumable} {
		if exists(path) {
			t.Errorf("kept %s", path)
		}
	}
	if r.OrphanBlobs != 1 || r.StalePartials != 2 || r.MissingBlobs != 0 || r.OrphanBytes == 0 {
		t.Fatalf("result = %+v", r)
	}
}

func TestOrphanSweepKeepsDedupLinksStillReferenced(t *testing.T) {
	fs := newFakeFileStore()
	s := New(Config{Addr: "127.0.0.1:0", RootDir: t.TempDir()}, fs, nil)
	ctx := context.Background()
	original := placeFile(t, s, "7/a.bin", 72*time.Hour)
	linked := filepath.Join(s.cfg.RootDir, "7", "b.bin")
	if err := os.Link(original, linked); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	stray := filepath.Join(s.cfg.RootDir, "7", "c.bin") // its row was deleted
	if err := os.Link(original, stray); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.bin", "b.bin"} {
		if err := fs.AddFile(ctx, store.FileRecord{ChannelID: 7, Name: name, Size: 1, SHA256: "same"}); err != nil {
			t.Fatal(err)
		}
	}
	runSweep(t, s)
	if exists(stray) {
		t.Fatal("unreferenced link survived")
	}
	for _, path := range []string{original, linked} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != "content of 7/a.bin" {
			t.Fatalf("referenced link %s damaged: %q %v", path, got, err)
		}
	}
}

func TestOrphanSweepDryRunAndMissingBlobRows(t *testing.T) {
	fs := newFakeFileStore()
	s := New(Config{Addr: "127.0.0.1:0", RootDir: t.TempDir()}, fs, nil)
	ctx := context.Background()
	orphan := placeFile(t, s, "7/forgotten.bin", 48*time.Hour)
	placeFile(t, s, "7/present.txt", 48*time.Hour)
	for _, name := range []string{"present.txt", "gone.txt"} {
		if err := fs.AddFile(ctx, store.FileRecord{ChannelID: 7, Name: name, Size: 1, SHA256: name}); err != nil {
			t.Fatal(err)
		}
	}

	s.SetOrphanPolicy(OrphanPolicy{DryRun: true, DeleteMissingRows: true})
	r := runSweep(t, s)
	if !exists(orphan) || r.OrphanBlobs != 1 || !r.DryRun {
		t.Fatalf("dry run removed or miscounted: %+v", r)
	}
	if r.MissingBlobs != 1 || r.RowsRemoved != 0 {
		t.Fatalf("dry run touched rows: %+v", r)
	}
	if _, err := fs.GetFile(ctx, 7, "", "gone.txt"); err != nil {
		t.Fatal("dry run deleted a row")
	}

	s.SetOrphanPolicy(OrphanPolicy{}) // defaults: remove orphans, report rows
	r = runSweep(t, s)
	if exists(orphan) || r.MissingBlobs != 1 || r.RowsRemoved != 0 {
		t.Fatalf("default pass = %+v (orphan kept: %v)", r, exists(orphan))
	}
	if _, err := fs.GetFile(ctx, 7, "", "gone.txt"); err != nil {
		t.Fatal("rows are only reported by default")
	}

	s.SetOrphanPolicy(OrphanPolicy{DeleteMissingRows: true})
	r = runSweep(t, s)
	if r.RowsRemoved != 1 {
		t.Fatalf("missing-row cleanup = %+v", r)
	}
	if _, err := fs.GetFile(ctx, 7, "", "gone.txt"); err == nil {
		t.Fatal("row with a missing blob survived cleanup")
	}
	if _, err := fs.GetFile(ctx, 7, "", "present.txt"); err != nil {
		t.Fatal("row with a present blob was removed")
	}
}

func TestOrphanSweepIsThrottledDisabledAndSkipsBusyPartials(t *testing.T) {
	s := New(Config{Addr: "127.0.0.1:0", RootDir: t.TempDir()}, newFakeFileStore(), nil)
	busy := "7/e.bin.part-r" + strings.Repeat("c", 32)
	busyPath := placeFile(t, s, busy, 30*time.Hour)
	s.partials[filepath.FromSlash(busy)] = &partialUpload{path: filepath.FromSlash(busy), uploader: "u", active: &activeTransfer{}}
	runSweep(t, s)
	if !exists(busyPath) {
		t.Fatal("a partial a live transfer is writing was removed")
	}

	orphan := placeFile(t, s, "7/forgotten.bin", 48*time.Hour)
	// A pass ran just now: the next tick does nothing.
	if err := s.PruneOrphanedFiles(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if !exists(orphan) {
		t.Fatal("sweep ran again before its interval")
	}
	s.SetOrphanPolicy(OrphanPolicy{Disabled: true})
	s.mu.Lock()
	s.sweep.next = time.Time{}
	s.mu.Unlock()
	if err := s.PruneOrphanedFiles(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if !exists(orphan) {
		t.Fatal("disabled sweep removed a file")
	}
}

func TestOrphanSweepResumesAcrossTicks(t *testing.T) {
	s := New(Config{Addr: "127.0.0.1:0", RootDir: t.TempDir()}, newFakeFileStore(), nil)
	a := placeFile(t, s, "7/old.bin", 48*time.Hour)
	b := placeFile(t, s, "8/old.bin", 48*time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the tick's budget is already used up
	if err := s.PruneOrphanedFiles(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !exists(a) || !exists(b) {
		t.Fatal("an expired tick removed files")
	}
	if err := s.PruneOrphanedFiles(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if exists(a) || exists(b) {
		t.Fatal("the pass did not continue on the next tick")
	}
}

func TestSplitBlobPath(t *testing.T) {
	sep := string(filepath.Separator)
	for rel, want := range map[string][2]string{
		"7" + sep + "a.bin":                     {"", "a.bin"},
		"7" + sep + "docs" + sep + "a.bin":      {"docs", "a.bin"},
		"7" + sep + "d" + sep + "e" + sep + "f": {"d/e", "f"},
	} {
		folder, name, ok := splitBlobPath(7, rel)
		if !ok || folder != want[0] || name != want[1] {
			t.Fatalf("splitBlobPath(%q) = %q %q %v", rel, folder, name, ok)
		}
	}
	for _, rel := range []string{"8" + sep + "a.bin", "7", "77" + sep + "a.bin"} {
		if _, _, ok := splitBlobPath(7, rel); ok {
			t.Fatalf("splitBlobPath accepted %q", rel)
		}
	}
}

func TestOrphanSweepCountsRemovalsOfAnInterruptedChannel(t *testing.T) {
	s := New(Config{Addr: "127.0.0.1:0", RootDir: t.TempDir()}, newFakeFileStore(), nil)
	placeFile(t, s, "7/a.bin", 48*time.Hour)
	result := OrphanSweepResult{OrphanBlobs: 1, OrphanBytes: 9}
	s.mu.Lock()
	s.sweep.total.add(result)
	got := s.sweep.total
	s.mu.Unlock()
	if got.OrphanBlobs != 1 || got.OrphanBytes != 9 {
		t.Fatalf("total = %+v", got)
	}
}

// DM and private-group attachments live in scope 0 (audit #13): stale
// partials and unreferenced blobs there are swept too, referenced
// attachments and group folders stay.
func TestOrphanSweepCoversScopeZero(t *testing.T) {
	fs := newFakeFileStore()
	s := New(Config{Addr: "127.0.0.1:0", RootDir: t.TempDir()}, fs, nil)
	ctx := context.Background()
	att := strings.Repeat("c", 32) + ".vcx"
	kept := placeFile(t, s, "0/"+att, 72*time.Hour)
	if err := fs.AddFile(ctx, store.FileRecord{ChannelID: 0, Name: att, Size: 1, SHA256: "x"}); err != nil {
		t.Fatal(err)
	}
	keptGroup := placeFile(t, s, "0/private-groups/g1/"+att, 72*time.Hour)
	if err := fs.AddFile(ctx, store.FileRecord{ChannelID: 0, Folder: "private-groups/g1", Name: att, Size: 1, SHA256: "x"}); err != nil {
		t.Fatal(err)
	}
	stalePartial := placeFile(t, s, "0/"+strings.Repeat("d", 32)+".vcx.part-r"+strings.Repeat("e", 32), 30*time.Hour)
	orphan := placeFile(t, s, "0/"+strings.Repeat("f", 32)+".vcx", 48*time.Hour)

	r := runSweep(t, s)
	for _, path := range []string{kept, keptGroup} {
		if !exists(path) {
			t.Errorf("removed referenced %s", path)
		}
	}
	for _, path := range []string{stalePartial, orphan} {
		if exists(path) {
			t.Errorf("kept %s", path)
		}
	}
	if r.OrphanBlobs != 1 || r.StalePartials != 1 {
		t.Fatalf("result = %+v", r)
	}
}
