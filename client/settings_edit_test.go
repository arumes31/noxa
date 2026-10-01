package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSettingsEditPreservesUnrelatedChanges(t *testing.T) {
	a := &App{settings: DefaultSettings(), settingsPath: filepath.Join(t.TempDir(), "settings.json")}
	dialog := a.GetSettings()
	audio := a.GetSettings()
	audio.BlockedUsers = []string{"alice"}
	audio.UserVolumes = map[string]int{"alice": 50}
	if err := a.SaveSettings(audio); err != "" {
		t.Fatal(err)
	}
	dialog.Theme = "light"
	if err := a.SaveSettings(dialog); err != "" {
		t.Fatal(err)
	}
	got := a.GetSettings()
	if len(got.BlockedUsers) != 1 || got.BlockedUsers[0] != "alice" || got.UserVolumes["alice"] != 50 || got.Theme != "light" {
		t.Fatalf("stale dialog overwrote unrelated changes: blocks=%v volumes=%v theme=%q", got.BlockedUsers, got.UserVolumes, got.Theme)
	}
	raw, err := os.ReadFile(a.settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "settings_base") {
		t.Fatal("edit baseline leaked into persisted settings")
	}
}

func TestSettingsEditRejectsConflictsAtomically(t *testing.T) {
	a := &App{settings: DefaultSettings(), settingsPath: filepath.Join(t.TempDir(), "settings.json")}
	first, second := a.GetSettings(), a.GetSettings()
	first.Volume = 50
	if err := a.SaveSettings(first); err != "" {
		t.Fatal(err)
	}
	second.Volume = 150
	second.Theme = "light"
	if err := a.SaveSettings(second); !strings.Contains(err, "changed elsewhere") {
		t.Fatalf("expected edit conflict, got %q", err)
	}
	got := a.GetSettings()
	if got.Volume != 50 || got.Theme != first.Theme {
		t.Fatalf("conflict partially committed: volume=%d theme=%q", got.Volume, got.Theme)
	}
	// Repeating an already applied edit is safe and keeps unrelated changes.
	if err := a.SaveSettings(first); err != "" {
		t.Fatal(err)
	}
}
