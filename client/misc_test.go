package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestConfigDirCreatesAppDirectory(t *testing.T) {
	original := miscUserConfigDir
	miscUserConfigDir = func() (string, error) { return t.TempDir(), nil }
	t.Cleanup(func() { miscUserConfigDir = original })

	dir, err := configDir()
	if err != nil {
		t.Fatalf("configDir: %v", err)
	}
	if want := filepath.Join(filepath.Dir(dir), "noxa"); dir != want {
		t.Fatalf("configDir = %q, want %q", dir, want)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat configDir: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("configDir created %q, want directory", dir)
	}
}

func TestConfigDirReturnsUserConfigFailure(t *testing.T) {
	original := miscUserConfigDir
	miscUserConfigDir = func() (string, error) { return "", errors.New("no user config directory") }
	t.Cleanup(func() { miscUserConfigDir = original })

	if _, err := configDir(); err == nil {
		t.Fatal("configDir succeeded despite UserConfigDir failure")
	}
}

// The second case checks record separators and escape sequences cannot forge
// a second log record.
func TestLogChatWritesTimestampedDailyLog(t *testing.T) {
	t.Cleanup(closeDailyLogs)
	for _, tc := range []struct{ line, want string }{
		{"hello from test", "[2026-08-22 12:34:56] hello from test\n"},
		{
			"[channel] mallory: hi\r\n[2026-08-22 12:34:57] [channel] alice: forged\\n\x1b[2J\u2028",
			`[2026-08-22 12:34:56] [channel] mallory: hi\r\n[2026-08-22 12:34:57] [channel] alice: forged\\n\u001b[2J\u2028` + "\n",
		},
	} {
		closeDailyLogs()
		originalDir, originalNow := miscUserConfigDir, chatLogNow
		base := t.TempDir()
		miscUserConfigDir = func() (string, error) { return base, nil }
		chatLogNow = func() time.Time { return time.Date(2026, time.August, 22, 12, 34, 56, 0, time.UTC) }
		t.Cleanup(func() {
			miscUserConfigDir = originalDir
			chatLogNow = originalNow
		})

		(&App{}).LogChat(tc.line)
		closeDailyLogs()
		raw, err := os.ReadFile(filepath.Join(base, "noxa", "chat.log"))
		if err != nil {
			t.Fatalf("read chat log: %v", err)
		}
		if got := string(raw); got != tc.want {
			t.Fatalf("chat log = %q, want %q", got, tc.want)
		}
	}
}

func TestLogChatIgnoresConfigDirectoryFailure(t *testing.T) {
	original := miscUserConfigDir
	miscUserConfigDir = func() (string, error) { return "", errors.New("no user config directory") }
	t.Cleanup(func() { miscUserConfigDir = original })

	(&App{}).LogChat("must not reach the filesystem")
}

func TestClampLogLine(t *testing.T) {
	if got := clampLogLine("short"); got != "short" {
		t.Fatalf("clampLogLine(short) = %q", got)
	}
	long := "x" + strings.Repeat("é", maxChatLogLine)
	got := clampLogLine(long)
	if !strings.HasSuffix(got, " [truncated]") || !utf8.ValidString(got) || len(got) > maxChatLogLine+len(" [truncated]") {
		t.Fatalf("clampLogLine(long) = %d bytes, valid=%v", len(got), utf8.ValidString(got))
	}
}
