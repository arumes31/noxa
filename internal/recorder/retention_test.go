package recorder

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func retainedCapture(t *testing.T, dir string, channel int64, ended time.Time, tracks int) (string, []string) {
	t.Helper()
	name := fmt.Sprintf("channel-%d-%032x.json", channel, channel)
	manifest := captureManifest{Version: 1, ChannelID: channel, StartedAt: ended.Add(-time.Hour), EndedAt: &ended}
	var files []string
	for i := range tracks {
		leaf := fmt.Sprintf("channel-%d-20260101-120000.000000000-%024x.webm", channel, i)
		manifest.Tracks = append(manifest.Tracks, &captureTrack{File: leaf})
		files = append(files, leaf)
		if err := os.WriteFile(filepath.Join(dir, leaf), []byte("media"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
		t.Fatal(err)
	}
	files = append(files, name)
	for _, leaf := range files {
		if err = os.Chtimes(filepath.Join(dir, leaf), ended, ended); err != nil {
			t.Fatal(err)
		}
	}
	return name, files
}
func assertRecordingFiles(t *testing.T, dir string, files []string, want bool) {
	t.Helper()
	for _, leaf := range files {
		_, err := os.Lstat(filepath.Join(dir, leaf))
		if want && err != nil || !want && !os.IsNotExist(err) {
			t.Fatalf("%s: exists want %v, error %v", leaf, want, err)
		}
	}
}
func TestRecordingRetentionBoundariesAndAbandonedSet(t *testing.T) {
	dir := privateTempDir(t)
	c := NewChannelRecorder(testConfig(dir), testLogger())
	defer func() { _ = c.Close() }()
	now := time.Now().UTC().Truncate(time.Second)
	_, old := retainedCapture(t, dir, 1, now.Add(-31*24*time.Hour), 2)
	_, boundary := retainedCapture(t, dir, 2, now.Add(-30*24*time.Hour), 1)
	_, recent := retainedCapture(t, dir, 3, now.Add(-time.Hour), 1)
	abandoned, crashed := retainedCapture(t, dir, 4, now.Add(-31*24*time.Hour), 1)
	raw, err := os.ReadFile(filepath.Join(dir, abandoned))
	if err != nil {
		t.Fatal(err)
	}
	var m captureManifest
	if err = json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	m.EndedAt = nil
	raw, _ = json.Marshal(m)
	if err = os.WriteFile(filepath.Join(dir, abandoned), raw, 0600); err != nil {
		t.Fatal(err)
	}
	oldTime := now.Add(-31 * 24 * time.Hour)
	if err = os.Chtimes(filepath.Join(dir, abandoned), oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "unrelated.txt"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = c.PruneExpiredRecordings(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	assertRecordingFiles(t, dir, old, false)
	assertRecordingFiles(t, dir, crashed, false)
	assertRecordingFiles(t, dir, boundary, true)
	assertRecordingFiles(t, dir, recent, true)
	assertRecordingFiles(t, dir, []string{"unrelated.txt"}, true)
}
func TestRecordingRetentionPreservesActiveCaptureAndThenExpires(t *testing.T) {
	dir := privateTempDir(t)
	c := NewChannelRecorder(testConfig(dir), testLogger())
	defer func() { _ = c.Close() }()
	c.processes.Exec = autoRecordingProcess
	session, err := c.Start(t.Context(), 1, &fakeTapRouter{})
	if err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(40 * 24 * time.Hour)
	if err = c.PruneExpiredRecordings(t.Context(), future); err != nil {
		t.Fatal(err)
	}
	assertRecordingFiles(t, dir, []string{filepath.Base(session.FilePath)}, true)
	if err = c.Stop(1); err != nil {
		t.Fatal(err)
	}
	if err = c.PruneExpiredRecordings(t.Context(), future); err != nil {
		t.Fatal(err)
	}
	assertRecordingFiles(t, dir, []string{filepath.Base(session.FilePath)}, false)
}
func TestRecordingRetentionBoundsDeletionAndResumes(t *testing.T) {
	dir := privateTempDir(t)
	c := NewChannelRecorder(testConfig(dir), testLogger())
	defer func() { _ = c.Close() }()
	now := time.Now().UTC()
	name, files := retainedCapture(t, dir, 1, now.Add(-31*24*time.Hour), maxPruneFiles+2)
	if err := c.PruneExpiredRecordings(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	remaining := 0
	for _, leaf := range files {
		if _, err := os.Stat(filepath.Join(dir, leaf)); err == nil {
			remaining++
		}
	}
	if remaining != 3 {
		t.Fatalf("remaining=%d, want 3 after 128 deletions", remaining)
	}
	assertRecordingFiles(t, dir, []string{name}, true)
	if err := c.PruneExpiredRecordings(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	assertRecordingFiles(t, dir, files, false)
}
func TestRecordingRetentionRejectsUnsafeManifest(t *testing.T) {
	for _, leaf := range []string{"../outside.webm", "C:/outside.webm", "unrelated.txt", "channel-1-invalid.webm"} {
		t.Run(leaf, func(t *testing.T) {
			dir := privateTempDir(t)
			c := NewChannelRecorder(testConfig(dir), testLogger())
			defer func() { _ = c.Close() }()
			now := time.Now().UTC()
			old := now.Add(-31 * 24 * time.Hour)
			name, files := retainedCapture(t, dir, 1, old, 1)
			raw, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			var m captureManifest
			if err = json.Unmarshal(raw, &m); err != nil {
				t.Fatal(err)
			}
			m.Tracks[0].File = leaf
			raw, _ = json.Marshal(m)
			if err = os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
				t.Fatal(err)
			}
			if err = os.Chtimes(filepath.Join(dir, name), old, old); err != nil {
				t.Fatal(err)
			}
			if err = c.PruneExpiredRecordings(t.Context(), now); err == nil {
				t.Fatal("unsafe manifest accepted")
			}
			assertRecordingFiles(t, dir, files, true)
		})
	}
}
func TestRecordingRetentionKeepsCleanupOwnedProcess(t *testing.T) {
	dir := privateTempDir(t)
	c := NewChannelRecorder(testConfig(dir), testLogger())
	defer c.closePruneScan()
	now := time.Now().UTC()
	_, files := retainedCapture(t, dir, 1, now.Add(-31*24*time.Hour), 1)
	// A corrupted manifest must never delete a process still tracked through
	// startup/stop cleanup, even when its channel identifier differs.
	c.processes.sessions[9] = &Session{FilePath: filepath.Join(dir, files[0])}
	if err := c.PruneExpiredRecordings(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	assertRecordingFiles(t, dir, files, true)
	delete(c.processes.sessions, 9)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := c.PruneExpiredRecordings(canceled, now); err == nil {
		t.Fatal("ignored cancellation")
	}
	assertRecordingFiles(t, dir, files, true)
}

func TestRecordingRetentionRejectsSymlinkLeaves(t *testing.T) {
	for _, kind := range []string{"manifest", "media"} {
		t.Run(kind, func(t *testing.T) {
			dir := privateTempDir(t)
			outside := privateTempDir(t)
			c := NewChannelRecorder(testConfig(dir), testLogger())
			defer func() { _ = c.Close() }()
			now := time.Now().UTC()
			old := now.Add(-31 * 24 * time.Hour)
			name, files := retainedCapture(t, dir, 1, old, 1)
			leaf := name
			if kind == "media" {
				leaf = files[0]
			}
			target := filepath.Join(outside, "protected")
			raw, err := os.ReadFile(filepath.Join(dir, leaf))
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(target, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if err = os.Chtimes(target, old, old); err != nil {
				t.Fatal(err)
			}
			if err = os.Remove(filepath.Join(dir, leaf)); err != nil {
				t.Fatal(err)
			}
			if err = os.Symlink(target, filepath.Join(dir, leaf)); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			_ = c.PruneExpiredRecordings(t.Context(), now)
			assertRecordingFiles(t, dir, files, true)
			assertRecordingFiles(t, outside, []string{"protected"}, true)
		})
	}
}
func TestRecordingRetentionCursorAndRootReplacement(t *testing.T) {
	dir := privateTempDir(t)
	c := NewChannelRecorder(testConfig(dir), testLogger())
	defer func() { _ = c.Close() }()
	now := time.Now().UTC()
	for i := range 256 {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("unrelated-%03d", i)), []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.PruneExpiredRecordings(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	if c.pruneScan == nil {
		t.Fatal("directory cursor was not retained")
	}
	_, files := retainedCapture(t, dir, 1, now.Add(-31*24*time.Hour), 1)
	for range 5 {
		if err := c.PruneExpiredRecordings(t.Context(), now); err != nil {
			t.Fatal(err)
		}
	}
	assertRecordingFiles(t, dir, files, false)
	c.closePruneScan()
	if err := c.PruneExpiredRecordings(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	moved := dir + "-moved"
	if err := os.Rename(dir, moved); err != nil {
		t.Skipf("platform cannot rename open directory: %v", err)
	}
	defer func() { c.closePruneScan(); _ = os.Remove(dir); _ = os.Rename(moved, dir) }()
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := c.PruneExpiredRecordings(t.Context(), now); err == nil {
		t.Fatal("root replacement not detected")
	}
	assertRecordingFiles(t, moved, []string{"unrelated-000"}, true)
}
func TestRecordingRetentionPreservesStartupReservation(t *testing.T) {
	dir := privateTempDir(t)
	c := NewChannelRecorder(testConfig(dir), testLogger())
	defer func() { _ = c.Close() }()
	now := time.Now().UTC()
	_, files := retainedCapture(t, dir, 1, now.Add(-31*24*time.Hour), 1)
	c.processes.starting[7] = &startReservation{}
	if err := c.PruneExpiredRecordings(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	assertRecordingFiles(t, dir, files, true)
	delete(c.processes.starting, 7)
	if err := c.PruneExpiredRecordings(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	assertRecordingFiles(t, dir, files, false)
}
