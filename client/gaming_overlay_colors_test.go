package main

import (
	"context"
	"path/filepath"
	"testing"
)

func TestOverlayColorsPersistAndNormalize(t *testing.T) {
	s := DefaultSettings()
	if s.GamingOverlaySelfColor != "#43c798" || s.GamingOverlayBlueStart != 195 || s.GamingOverlayBlueEnd != 235 {
		t.Fatal("self must default green and peers blue")
	}
	s.GamingOverlaySelfColor = "#FF8800"
	s.GamingOverlayBlueStart, s.GamingOverlayBlueEnd = 180, 200
	s.GamingOverlayAnimate = false
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := saveSettingsAt(path, s); err != nil {
		t.Fatal(err)
	}
	loaded := loadSettingsAt(path)
	if loaded.GamingOverlaySelfColor != "#ff8800" || loaded.GamingOverlayBlueStart != 180 || loaded.GamingOverlayBlueEnd != 200 || loaded.GamingOverlayAnimate {
		t.Fatal("configured colors or motion preference lost")
	}
	s.GamingOverlaySelfColor = "invalid"
	s.GamingOverlayBlueStart, s.GamingOverlayBlueEnd = 999, -1
	s = normalizeSettings(s)
	if s.GamingOverlaySelfColor != "#43c798" || s.GamingOverlayBlueStart != 220 || s.GamingOverlayBlueEnd != 240 {
		t.Fatal("invalid preferences escaped bounds")
	}
	for _, value := range []string{"#123", "red", "#gg0000", "#+12345", "#1234567"} {
		if validOverlayColor(value) {
			t.Fatalf("malformed color accepted: %q", value)
		}
	}
}

type overlayColorPreview struct{ snapshot GamingOverlaySnapshot }

func (p *overlayColorPreview) Preview(s GamingOverlaySnapshot) { p.snapshot = s }
func (p *overlayColorPreview) Update(GamingOverlaySnapshot)    {}
func (p *overlayColorPreview) Close()                          {}

func TestOverlayNativePreviewUsesDraftColors(t *testing.T) {
	preview := &overlayColorPreview{}
	a := &App{ctx: context.Background(), overlay: preview}
	s := DefaultSettings()
	s.GamingOverlaySelfColor = "#ff8800"
	s.GamingOverlayBlueStart, s.GamingOverlayBlueEnd = 180, 200
	if err := a.PreviewGamingOverlay(s); err != "" {
		t.Fatal(err)
	}
	if preview.snapshot.Speakers[0].Color != "#ff8800" || preview.snapshot.Speakers[1].Color != "#36e0e0" {
		t.Fatal("native preview lost draft colors")
	}
}
