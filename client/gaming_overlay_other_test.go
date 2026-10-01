//go:build !windows

package main

import "testing"

func TestGamingOverlayUnavailablePlatform(t *testing.T) {
	a := &App{ctx: t.Context(), settings: DefaultSettings()}
	if a.GamingOverlayAvailable() || len(a.GetGamingOverlayMonitors()) != 0 {
		t.Fatal("unsupported platform advertises native overlay support")
	}
	const unavailable = "gaming overlay is available on Windows only"
	if got := a.PreviewGamingOverlay(DefaultSettings()); got != unavailable {
		t.Fatalf("preview returned %q, want %q", got, unavailable)
	}
	if got := a.UpdateGamingOverlay(GamingOverlaySnapshot{Active: true}); got != unavailable {
		t.Fatalf("active update returned %q, want %q", got, unavailable)
	}
	if got := a.UpdateGamingOverlay(GamingOverlaySnapshot{}); got != "" {
		t.Fatalf("inactive update returned %q", got)
	}
	if a.overlay != nil {
		t.Fatal("unsupported platform created an overlay")
	}
}
