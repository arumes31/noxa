package main

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"sync"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// (318) Bookmark export. The webview already strips credential fields; this
// side enforces the same default so a stale or tampered frontend cannot write
// passwords without the explicit opt-in, and it is the only place that can
// attach OS-protected saved passwords when the user does opt in.

const maxBookmarkExportBytes = 4 << 20

var bookmarkExportMu sync.Mutex

var credentialKeyPattern = regexp.MustCompile(`(?i)(^|_|[a-z])(password|passwd|passphrase|secret|token|credential|api_?key|private_?key)s?($|_|[A-Z])|^(pw|spw|server_pw|account_pw)$`)

func isCredentialKey(key string) bool { return credentialKeyPattern.MatchString(key) }

// stripCredentialFields removes credential-like keys recursively from decoded JSON.
func stripCredentialFields(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, inner := range typed {
			if isCredentialKey(key) {
				continue
			}
			out[key] = stripCredentialFields(inner)
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, inner := range typed {
			out = append(out, stripCredentialFields(inner))
		}
		return out
	default:
		return value
	}
}

type bookmarkCredentialLookup func(addr, nickname string) (loginPasswords, error)

// prepareBookmarkExport returns the JSON that is written to disk. Without
// includeCredentials every credential-like field is removed; with it, saved
// passwords for each bookmark's addr/nickname are added in plain text.
func prepareBookmarkExport(contents string, includeCredentials bool, lookup bookmarkCredentialLookup) ([]byte, error) {
	if len(contents) == 0 || len(contents) > maxBookmarkExportBytes || !strings.HasPrefix(strings.TrimSpace(contents), "{") {
		return nil, errors.New("invalid bookmark export")
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(contents), &payload); err != nil {
		return nil, err
	}
	rawList, _ := payload["bookmarks"].([]any)
	if rawList == nil {
		return nil, errors.New("bookmark export has no bookmark list")
	}
	payload["include_credentials"] = includeCredentials
	if !includeCredentials {
		payload["bookmarks"] = stripCredentialFields(rawList)
		return json.MarshalIndent(payload, "", "  ")
	}
	for _, item := range rawList {
		entry, ok := item.(map[string]any)
		if !ok || lookup == nil {
			continue
		}
		addr, _ := entry["addr"].(string)
		nickname, _ := entry["nickname"].(string)
		addr, nickname = strings.TrimSpace(addr), strings.TrimSpace(nickname)
		if addr == "" || nickname == "" {
			continue
		}
		saved, err := lookup(addr, nickname)
		if err != nil {
			continue // unavailable store: export without that bookmark's secrets
		}
		if saved.Password != "" {
			entry["password"] = saved.Password
		}
		if saved.ServerPassword != "" {
			entry["server_password"] = saved.ServerPassword
		}
	}
	return json.MarshalIndent(payload, "", "  ")
}

// SaveBookmarkExport writes the bookmark export chosen in the webview to a
// user-selected file. Paths and native error text are never returned.
func (a *App) SaveBookmarkExport(contents string, includeCredentials bool) string {
	if !bookmarkExportMu.TryLock() {
		return "busy"
	}
	defer bookmarkExportMu.Unlock()
	var lookup bookmarkCredentialLookup
	if includeCredentials {
		lookup = func(addr, nickname string) (loginPasswords, error) {
			a.loginCredentials.mu.Lock()
			defer a.loginCredentials.mu.Unlock()
			return a.loginPasswordStore().load(addr, nickname)
		}
	}
	return saveBookmarkExport(a.ctx, contents, includeCredentials, lookup, wailsRuntime.SaveFileDialog, writePrivateFileAtomic)
}

func saveBookmarkExport(ctx context.Context, contents string, includeCredentials bool, lookup bookmarkCredentialLookup, dialog func(context.Context, wailsRuntime.SaveDialogOptions) (string, error), write func(string, []byte) error) string {
	raw, err := prepareBookmarkExport(contents, includeCredentials, lookup)
	if err != nil {
		return "error"
	}
	path, err := dialog(ctx, wailsRuntime.SaveDialogOptions{DefaultFilename: "noxa-bookmarks.json", Filters: []wailsRuntime.FileFilter{{DisplayName: "noXa bookmarks", Pattern: "*.json"}}})
	if err != nil {
		return "error"
	}
	if path == "" {
		return "cancelled"
	}
	if err = write(path, raw); err != nil {
		return "error"
	}
	return "saved"
}
