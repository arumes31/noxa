package main

import (
	"context"
	"errors"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStreamDiagnosticSave(t *testing.T) {
	raw := "{\n  \"recording\": {\"samples\": []}\n}"
	path := filepath.Join(t.TempDir(), "diagnostics.json")
	dialog := func(_ context.Context, o wailsRuntime.SaveDialogOptions) (string, error) {
		if o.DefaultFilename != "noxa-stream-diagnostics.json" || o.Filters[0].Pattern != "*.json" {
			t.Fatal(o)
		}
		return path, nil
	}
	if got := saveStreamDiagnostics(context.Background(), raw, dialog, writePrivateFileAtomic); got != "saved" {
		t.Fatal(got)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != raw {
		t.Fatalf("snapshot changed: %v", err)
	}
	for _, value := range []string{"", "[]", "{broken}", strings.Repeat("x", maxStreamDiagnosticExportBytes+1)} {
		if saveStreamDiagnostics(context.Background(), value, func(context.Context, wailsRuntime.SaveDialogOptions) (string, error) {
			t.Fatal("invalid JSON opened dialog")
			return "", nil
		}, writePrivateFileAtomic) != "error" {
			t.Fatal("invalid accepted")
		}
	}
	for _, tc := range []struct {
		path string
		err  error
		want string
	}{{"", nil, "cancelled"}, {"", errors.New("private path"), "error"}} {
		got := saveStreamDiagnostics(context.Background(), raw, func(context.Context, wailsRuntime.SaveDialogOptions) (string, error) { return tc.path, tc.err }, func(string, []byte) error { t.Fatal("unexpected write"); return nil })
		if got != tc.want {
			t.Fatal(got)
		}
	}
	if saveStreamDiagnostics(context.Background(), raw, dialog, func(string, []byte) error { return errors.New("private path") }) != "error" {
		t.Fatal("write error lost")
	}
	data, _ = os.ReadFile(path)
	if string(data) != raw {
		t.Fatal("failed save changed old file")
	}
}
func TestStreamDiagnosticSaveAtomicFailure(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "occupied")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFileAtomic(target, []byte("{}")); err == nil {
		t.Fatal("replace directory succeeded")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "occupied" {
		t.Fatal("temporary file leaked")
	}
}
func TestStreamDiagnosticSaveBusy(t *testing.T) {
	streamDiagnosticExportMu.Lock()
	defer streamDiagnosticExportMu.Unlock()
	if (&App{}).SaveStreamDiagnostics("{}") != "busy" {
		t.Fatal("second dialog admitted")
	}
}
