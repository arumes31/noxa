package main

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const maxStreamDiagnosticExportBytes = 16 << 20

var streamDiagnosticExportMu sync.Mutex

// SaveStreamDiagnostics saves only the caller's existing redacted JSON snapshot.
// Paths and native error text are never returned to the webview.
func (a *App) SaveStreamDiagnostics(contents string) string {
	if !streamDiagnosticExportMu.TryLock() {
		return "busy"
	}
	defer streamDiagnosticExportMu.Unlock()
	return saveStreamDiagnostics(a.ctx, contents, wailsRuntime.SaveFileDialog, writePrivateFileAtomic)
}

func saveStreamDiagnostics(ctx context.Context, contents string, dialog func(context.Context, wailsRuntime.SaveDialogOptions) (string, error), write func(string, []byte) error) string {
	if len(contents) == 0 || len(contents) > maxStreamDiagnosticExportBytes || !strings.HasPrefix(strings.TrimSpace(contents), "{") || !json.Valid([]byte(contents)) {
		return "error"
	}
	path, err := dialog(ctx, wailsRuntime.SaveDialogOptions{DefaultFilename: "noxa-stream-diagnostics.json", Filters: []wailsRuntime.FileFilter{{DisplayName: "JSON diagnostics", Pattern: "*.json"}}})
	if err != nil {
		return "error"
	}
	if path == "" {
		return "cancelled"
	}
	if err = write(path, []byte(contents)); err != nil {
		return "error"
	}
	return "saved"
}
