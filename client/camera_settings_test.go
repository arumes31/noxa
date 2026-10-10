package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCameraAndOverlaySettingsDefaultsAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"settings_version":7}`), 0600); err != nil {
		t.Fatal(err)
	}
	s := loadSettingsAt(path)
	if !s.GamingOverlay || !s.GamingOverlayAnimate || s.GamingOverlayPosition != "center-left" || s.CameraBackground != "none" {
		t.Fatal("new settings defaults missing")
	}
	s.CameraDeviceID = "usb-camera"
	s.CameraBackground = "blur"
	s.GamingOverlay = false
	s.GamingOverlayAnimate = false
	if err := saveSettingsAt(path, s); err != nil {
		t.Fatal(err)
	}
	got := loadSettingsAt(path)
	if got.CameraDeviceID != "usb-camera" || got.CameraBackground != "blur" || got.GamingOverlay || got.GamingOverlayAnimate {
		t.Fatal("camera/overlay settings did not persist")
	}
}

func TestCameraAndOverlaySettingsRejectInvalidModes(t *testing.T) {
	a := &App{settings: DefaultSettings(), hotkeys: map[string]*hotkeyReg{}, settingsPath: filepath.Join(t.TempDir(), "settings.json")}
	for _, mutate := range []func(*Settings){
		func(s *Settings) { s.CameraBackground = "remote" },
		func(s *Settings) { s.CameraBackgroundScene = "https://remote/image" },
		func(s *Settings) { s.GamingOverlayPosition = "center" },
		func(s *Settings) { s.CameraFPS = 900 },
	} {
		s := a.GetSettings()
		mutate(&s)
		if a.SaveSettings(s) == "" {
			t.Fatal("accepted invalid camera/overlay setting")
		}
	}
}

func TestOverlayCompactDefaultsMigrationPreservesCustomization(t *testing.T) {
	for _, tc := range []struct {
		config, position string
		scale            int
	}{
		{`{"settings_version":10,"gaming_overlay_position":"top-right","gaming_overlay_scale":100}`, "center-left", 80},
		{`{"settings_version":10,"gaming_overlay_position":"custom","gaming_overlay_scale":150,"gaming_overlay_x":25,"gaming_overlay_y":50}`, "custom", 150},
		{`{"settings_version":11,"gaming_overlay_position":"top-right","gaming_overlay_scale":100}`, "top-right", 100},
	} {
		path := filepath.Join(t.TempDir(), "settings.json")
		if err := os.WriteFile(path, []byte(tc.config), 0600); err != nil {
			t.Fatal(err)
		}
		s := loadSettingsAt(path)
		if s.GamingOverlayPosition != tc.position || s.GamingOverlayScale != tc.scale {
			t.Fatalf("incorrect migration: %s -> %s/%d", tc.config, s.GamingOverlayPosition, s.GamingOverlayScale)
		}
		if tc.position == "custom" && (s.GamingOverlayX != 25 || s.GamingOverlayY != 50) {
			t.Fatal("lost custom coordinates")
		}
	}
}
