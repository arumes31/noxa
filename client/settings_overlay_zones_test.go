package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// The game UI preset and zone avoidance are settings-page placement aids.
// They are new fields with non-zero defaults, so older files that never wrote
// them must load the defaults without a settingsVersion bump.
func TestOverlayGameUIDefaultsForLegacyFiles(t *testing.T) {
	def := DefaultSettings()
	if def.GamingOverlayGameUI != "none" || !def.GamingOverlayAvoidZones {
		t.Fatalf("defaults = %q/%v, want none/true", def.GamingOverlayGameUI, def.GamingOverlayAvoidZones)
	}
	path := filepath.Join(t.TempDir(), "settings.json")
	legacy := []byte(`{"settings_version":13,"gaming_overlay_position":"custom","gaming_overlay_x":40,"gaming_overlay_y":60}`)
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	got := loadSettingsAt(path)
	if got.GamingOverlayGameUI != "none" || !got.GamingOverlayAvoidZones {
		t.Fatalf("legacy file loaded %q/%v, want none/true", got.GamingOverlayGameUI, got.GamingOverlayAvoidZones)
	}
	if got.GamingOverlayPosition != "custom" || got.GamingOverlayX != 40 || got.GamingOverlayY != 60 {
		t.Fatalf("legacy placement changed: %+v", got)
	}
	if got.SettingsVersion != settingsVersion {
		t.Fatalf("version = %d, want %d", got.SettingsVersion, settingsVersion)
	}
}

func TestOverlayGameUIRoundTripAndExplicitOptOut(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	s := DefaultSettings()
	s.GamingOverlayGameUI = "mmo"
	s.GamingOverlayAvoidZones = false
	if err := saveSettingsAt(path, s); err != nil {
		t.Fatal(err)
	}
	got := loadSettingsAt(path)
	if got.GamingOverlayGameUI != "mmo" || got.GamingOverlayAvoidZones {
		t.Fatalf("round trip = %q/%v, want mmo/false", got.GamingOverlayGameUI, got.GamingOverlayAvoidZones)
	}
}

func TestSaveSettingsValidatesOverlayGameUI(t *testing.T) {
	a := &App{settings: DefaultSettings(), hotkeys: map[string]*hotkeyReg{}, settingsPath: filepath.Join(t.TempDir(), "settings.json")}
	for _, preset := range []string{"none", "mmo", "shooter", "moba"} {
		next := a.GetSettings()
		next.GamingOverlayGameUI = preset
		if msg := a.SaveSettings(next); msg != "" {
			t.Fatalf("preset %q rejected: %s", preset, msg)
		}
		if got := a.GetSettings().GamingOverlayGameUI; got != preset {
			t.Fatalf("saved preset = %q, want %q", got, preset)
		}
	}
	before := a.GetSettings()
	invalid := cloneSettings(before)
	invalid.GamingOverlayGameUI = "rts"
	if msg := a.SaveSettings(invalid); msg != "invalid gaming overlay game UI preset" {
		t.Fatalf("unexpected validation result %q", msg)
	}
	if got := a.GetSettings().GamingOverlayGameUI; got != before.GamingOverlayGameUI {
		t.Fatalf("rejected preset changed settings to %q", got)
	}
	if got := normalizeSettings(Settings{}).GamingOverlayGameUI; got != "none" {
		t.Fatalf("empty preset normalized to %q, want none", got)
	}
}

// The overlay itself must not change: presentation still only carries the
// existing placement fields, whatever the preset.
func TestOverlayPresentationIgnoresGameUIPreset(t *testing.T) {
	base := DefaultSettings()
	withPreset := base
	withPreset.GamingOverlayGameUI = "moba"
	withPreset.GamingOverlayAvoidZones = false
	snapshot := GamingOverlaySnapshot{Active: true, Speakers: []GamingOverlaySpeaker{{ID: "a", Name: "A", Speaking: true}}}
	if a, b := overlayPresentation(snapshot, base), overlayPresentation(snapshot, withPreset); !reflect.DeepEqual(a, b) {
		t.Fatalf("preset changed the overlay snapshot: %+v vs %+v", a, b)
	}
}

func TestDefaultSettingsSnapshotMatchesDefaults(t *testing.T) {
	a := &App{settings: DefaultSettings()}
	a.settings.Volume = 150
	if got := a.DefaultSettingsSnapshot(); !reflect.DeepEqual(got, DefaultSettings()) {
		t.Fatalf("snapshot differs from DefaultSettings: %+v", got)
	}
}
