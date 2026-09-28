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
	if !s.GamingOverlay || s.GamingOverlayPosition != "top-right" || s.CameraBackground != "none" {
		t.Fatal("new settings defaults missing")
	}
	s.CameraDeviceID = "usb-camera"
	s.CameraBackground = "blur"
	s.GamingOverlay = false
	if err := saveSettingsAt(path, s); err != nil {
		t.Fatal(err)
	}
	got := loadSettingsAt(path)
	if got.CameraDeviceID != "usb-camera" || got.CameraBackground != "blur" || got.GamingOverlay {
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
