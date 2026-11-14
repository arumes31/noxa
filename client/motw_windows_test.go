package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMarkDownloadedWritesZoneIdentifier(t *testing.T) {
	path := filepath.Join(t.TempDir(), "received.exe")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	markDownloaded(path)
	got, err := os.ReadFile(path + ":Zone.Identifier")
	if err != nil {
		t.Skip("volume without alternate data streams:", err)
	}
	if string(got) != zoneIdentifier {
		t.Fatalf("zone identifier = %q", got)
	}
}
