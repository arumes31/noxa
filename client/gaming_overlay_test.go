package main

import (
	"strings"
	"testing"
)

func TestGamingOverlaySnapshotBoundsAndPrivacy(t *testing.T) {
	s := GamingOverlaySnapshot{Active: true, Title: "call\x00\r\ninjected", Status: "Muted", Notification: strings.Repeat("x", 400), Position: "invalid"}
	for range 30 {
		s.Speakers = append(s.Speakers, GamingOverlaySpeaker{Name: strings.Repeat("a", 120), Speaking: true})
	}
	got := normalizeGamingOverlay(s)
	if len(got.Speakers) != 8 || len([]rune(got.Notification)) > 140 || strings.ContainsAny(got.Title, "\x00\r\n") || got.Position != "top-right" {
		t.Fatalf("unbounded overlay: %+v", got)
	}
	if len([]rune(got.Speakers[0].Name)) > 48 {
		t.Fatal("speaker name not bounded")
	}
	s.Active = false
	got = normalizeGamingOverlay(s)
	if got.Title != "" || len(got.Speakers) != 0 || got.Notification != "" {
		t.Fatal("inactive overlay retains private content")
	}
}

func TestGamingOverlayDefaultsAndText(t *testing.T) {
	if !DefaultSettings().GamingOverlay {
		t.Fatal("gaming overlay must be enabled by default")
	}
	s := normalizeGamingOverlay(GamingOverlaySnapshot{Active: true, Title: "Squad", Status: "Muted", Speakers: []GamingOverlaySpeaker{{Name: "Alice", Speaking: true}, {Name: "Bob", Muted: true}}})
	text := gamingOverlayText(s)
	if !strings.Contains(text, "Squad") || !strings.Contains(text, "Alice") || !strings.Contains(text, "Bob") || !strings.Contains(text, "Muted") {
		t.Fatalf("missing voice state: %q", text)
	}
}

func TestGamingOverlayMonitorPlacementAndBounds(t *testing.T) {
	monitors := []GamingOverlayMonitor{
		{ID: "primary", Primary: true, Width: 1920, Height: 1080, workWidth: 1920, workHeight: 1040},
		{ID: "left", Width: 1280, Height: 720, workLeft: -1280, workWidth: 1280, workHeight: 680},
	}
	s := normalizeGamingOverlay(GamingOverlaySnapshot{Active: true, Monitor: "left", Position: "custom", X: 100, Y: 100, Scale: 200, Opacity: 1})
	area := selectOverlayMonitor(monitors, s.Monitor)
	x, y, width, height := gamingOverlayPlacement(s, area)
	if x < -1280 || y < 0 || x+width > 0 || y+height > 680 || width != 696 || s.Opacity != 20 {
		t.Fatalf("overlay outside selected monitor: %d,%d %dx%d %+v", x, y, width, height, s)
	}
	if got := selectOverlayMonitor(monitors, "unplugged"); got.ID != "primary" {
		t.Fatalf("unplugged monitor did not fall back: %+v", got)
	}
	s.Scale = 900
	s.X = -10
	s.Y = 200
	s.Opacity = 500
	s = normalizeGamingOverlay(s)
	if s.Scale != 200 || s.X != 0 || s.Y != 100 || s.Opacity != 100 {
		t.Fatalf("unbounded presentation: %+v", s)
	}
}

func TestGamingOverlaySpeakersOnlyFiltersBeforeDisplayLimit(t *testing.T) {
	s := GamingOverlaySnapshot{Active: true, Speakers: []GamingOverlaySpeaker{{Name: "Quiet"}, {Name: "Talking", Speaking: true}}}
	settings := DefaultSettings()
	settings.GamingOverlaySpeakersOnly = true
	got := overlayPresentation(s, settings)
	if len(got.Speakers) != 1 || got.Speakers[0].Name != "Talking" || len(s.Speakers) != 2 {
		t.Fatalf("speaker filter mutated input or retained quiet member: %+v", got)
	}
}
