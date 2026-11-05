package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDailyLogWriterRotatesAndCapsDirectory(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	w, err := newDailyLogWriter(dir, "client.log")
	if err != nil {
		t.Fatal(err)
	}
	w.now = func() time.Time { return now }
	w.day = now.Format("20060102")
	if _, err := w.Write([]byte("first\n")); err != nil {
		t.Fatal(err)
	}
	now = now.Add(24 * time.Hour)
	if _, err := w.Write([]byte("second\n")); err != nil {
		t.Fatal(err)
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, "client-*.log")); len(matches) != 1 {
		t.Fatalf("rotated logs = %v", matches)
	}
	old := filepath.Join(dir, "client-20260701.log")
	if err := os.WriteFile(old, make([]byte, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "client-unrelated.log")
	if err := os.WriteFile(other, make([]byte, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	w.maxBytes = 16
	w.sincePrune = pruneCheckBytes - 1
	if _, err := w.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("oldest rotated log was not pruned")
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("another writer's log was pruned: %v", err)
	}
	_ = w.Close()
}

func TestAppendDailyLogReusesWriterAndCloseClearsCache(t *testing.T) {
	closeDailyLogs()
	dir := t.TempDir()
	appendDailyLog(dir, "chat.log", "one\n")
	appendDailyLog(dir, "chat.log", "two\n")
	chatLogMu.Lock()
	count := len(chatLogWriters)
	chatLogMu.Unlock()
	if count != 1 {
		t.Fatalf("cached writers = %d, want 1", count)
	}
	closeDailyLogs()
	chatLogMu.Lock()
	count = len(chatLogWriters)
	chatLogMu.Unlock()
	if count != 0 {
		t.Fatalf("cached writers after close = %d", count)
	}
	contents, err := os.ReadFile(filepath.Join(dir, "chat.log"))
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "one\ntwo\n" {
		t.Fatalf("contents = %q", contents)
	}
}

// Repeated size rotations within the same second keep every segment.
func TestDailyLogWriterSameSecondRotationsKeepSegments(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	w, err := newDailyLogWriter(dir, "chat.log")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	w.now = func() time.Time { return now }
	w.day = now.Format("20060102")
	w.rotateBytes = 8
	for i := range 5 {
		if _, err := fmt.Fprintf(w, "segment-%d\n", i); err != nil {
			t.Fatal(err)
		}
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "chat-*.log"))
	if len(matches) != 4 {
		t.Fatalf("rotated logs = %v, want four distinct segments", matches)
	}
	seen := map[string]bool{}
	for _, match := range matches {
		data, err := os.ReadFile(match)
		if err != nil {
			t.Fatal(err)
		}
		seen[string(data)] = true
	}
	for i := range 4 {
		if !seen[fmt.Sprintf("segment-%d\n", i)] {
			t.Fatalf("segment %d lost; rotated contents = %v", i, seen)
		}
	}
}

// The active file rotates once it reaches rotateBytes, so a same-day flood
// is subject to the directory cap rather than growing one file forever.
func TestDailyLogWriterRotatesOversizedActiveFile(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	w, err := newDailyLogWriter(dir, "chat.log")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	w.now = func() time.Time { return now }
	w.day = now.Format("20060102")
	w.rotateBytes = 8
	if _, err := w.Write([]byte("0123456789\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("next\n")); err != nil {
		t.Fatal(err)
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, "chat-*.log")); len(matches) != 1 {
		t.Fatalf("rotated logs = %v, want one", matches)
	}
	active, err := os.ReadFile(filepath.Join(dir, "chat.log"))
	if err != nil {
		t.Fatal(err)
	}
	if string(active) != "next\n" {
		t.Fatalf("active log = %q", active)
	}
}
