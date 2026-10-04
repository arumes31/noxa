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
	if len(got.Speakers) != 8 || len([]rune(got.Notification)) > 140 || strings.ContainsAny(got.Title, "\x00\r\n") || got.Position != "center-left" {
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

func TestGamingOverlayGrowsAroundLeftMidpoint(t *testing.T) {
	for _, height := range []int32{1080, 1440, 2160} {
		monitor := GamingOverlayMonitor{workWidth: 1920, workHeight: height, workLeft: -1920, workTop: 30}
		s := GamingOverlaySnapshot{Active: true}
		var previousTop, previousBottom int32
		for people := 1; people <= 8; people++ {
			s.Speakers = append(s.Speakers, GamingOverlaySpeaker{Name: "Speaker", Speaking: true})
			x, y, _, h := gamingOverlayPlacement(s, monitor)
			if x != -1904 || y+h/2 != 30+height*45/100 {
				t.Fatalf("lost left midpoint with %d speakers: %d,%d height %d", people, x, y, h)
			}
			if people > 1 && (y >= previousTop || y+h <= previousBottom) {
				t.Fatal("overlay did not grow in both directions")
			}
			previousTop, previousBottom = y, y+h
		}
	}
}

func TestGamingOverlayDefaultsAndSilentState(t *testing.T) {
	if !DefaultSettings().GamingOverlay || DefaultSettings().GamingOverlayPosition != "center-left" {
		t.Fatal("overlay must default to the left")
	}
	got := normalizeGamingOverlay(GamingOverlaySnapshot{Active: true, Speakers: []GamingOverlaySpeaker{{Name: "Quiet"}, {Name: "Muted", Speaking: true, Muted: true}}})
	if got.Active || len(got.Speakers) != 0 {
		t.Fatalf("silent overlay should be hidden: %+v", got)
	}
}

func TestGamingOverlayAnimationUsesIndependentSavedPreference(t *testing.T) {
	s := GamingOverlaySnapshot{Active: true, Speakers: []GamingOverlaySpeaker{{Name: "Alice", Speaking: true}}}
	settings := DefaultSettings()
	if !overlayPresentation(s, settings).Animate {
		t.Fatal("overlay must animate by default, independently of system motion settings")
	}
	settings.GamingOverlayAnimate = false
	s.Animate = true // The frontend cannot override the saved native preference.
	if overlayPresentation(s, settings).Animate {
		t.Fatal("ignored the overlay's disabled animation preference")
	}
}

func TestGamingOverlayMonitorPlacementAndBounds(t *testing.T) {
	monitors := []GamingOverlayMonitor{
		{ID: "primary", Primary: true, Width: 1920, Height: 1080, workWidth: 1920, workHeight: 1040},
		{ID: "left", Width: 1280, Height: 720, workLeft: -1280, workWidth: 1280, workHeight: 680},
	}
	s := normalizeGamingOverlay(GamingOverlaySnapshot{Active: true, Monitor: "left", Position: "custom", X: 100, Y: 100, Scale: 200, Opacity: 1, Speakers: []GamingOverlaySpeaker{{Name: "Alice", Speaking: true}}})
	area := selectOverlayMonitor(monitors, s.Monitor)
	x, y, width, height := gamingOverlayPlacement(s, area)
	if x < -1280 || y < 0 || x+width > 0 || y+height > 680 || width != 560 || s.Opacity != 20 {
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
	s := GamingOverlaySnapshot{Active: true}
	for range 12 {
		s.Speakers = append(s.Speakers, GamingOverlaySpeaker{Name: "Quiet"})
	}
	s.Speakers = append(s.Speakers, GamingOverlaySpeaker{Name: "Talking", Speaking: true})
	settings := DefaultSettings()
	got := overlayPresentation(s, settings)
	if len(got.Speakers) != 1 || got.Speakers[0].Name != "Talking" || len(s.Speakers) != 13 {
		t.Fatalf("speaker filter mutated input or retained quiet member: %+v", got)
	}
}
