package main

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mistTestLabel(text string, width, height, _ int, _ bool) (*image.Alpha, error) {
	mask := image.NewAlpha(image.Rect(0, 0, width, height))
	// A bounded line of glyphs with gaps lets us inspect the background too.
	for x := 0; x < min(width, len([]rune(text))*8); x++ {
		if x%8 < 4 {
			mask.SetAlpha(x, height/2, color.Alpha{A: 255})
		}
	}
	return mask, nil
}

func TestMistAuroraOverlayFollowsNameAndKeepsTextReadable(t *testing.T) {
	var previousWidth int
	for _, name := range []string{"Sam", "NoxaUser", "TheLegendaryNightwalker", strings.Repeat("W", 48)} {
		s := normalizeGamingOverlay(GamingOverlaySnapshot{Active: true, Style: "mist-aurora", Scale: 100,
			Speakers: []GamingOverlaySpeaker{{Name: name, Speaking: true}}})
		r, err := newOverlayRenderer(s, 1400, 400, mistTestLabel)
		if err != nil {
			t.Fatal(err)
		}
		if width := r.base.Bounds().Dx(); width <= previousWidth {
			t.Fatalf("name %q did not extend the overlay: %d <= %d", name, width, previousWidth)
		} else {
			previousWidth = width
		}
		first := bytes.Clone(r.render(0).Pix)
		frame := r.render(0.7)
		if bytes.Equal(first, frame.Pix) {
			t.Fatal("mist and aurora did not animate")
		}
		// The gaps between glyphs retain animation, capped at 10% opacity.
		columns := 0
		for x := 70; x < frame.Bounds().Dx()-18; x++ {
			if (x-70)%8 < 4 {
				continue
			}
			visible := false
			for y := 79; y <= 85; y++ {
				alpha := frame.RGBAAt(x, y).A
				if alpha > 26 {
					t.Fatalf("animation hides name %q at %d,%d: alpha=%d", name, x, y, alpha)
				}
				visible = visible || alpha > 0
			}
			if visible {
				columns++
			}
		}
		if columns < len([]rune(name))*2 {
			t.Fatalf("animation did not continue behind full name %q: %d columns", name, columns)
		}
		above, below := false, false
		for y := range frame.Bounds().Dy() {
			for x := range frame.Bounds().Dx() {
				if frame.RGBAAt(x, y).A == 0 {
					continue
				}
				if y < 70 {
					above = true
				}
				if y > 114 {
					below = true
				}
				if x < 2 || x >= frame.Bounds().Dx()-2 || y < 2 || y >= frame.Bounds().Dy()-2 {
					t.Fatalf("animation clipped at %d,%d", x, y)
				}
			}
		}
		if !above || !below {
			t.Fatal("animation must extend above and below avatar")
		}
		frozen := bytes.Clone(r.render(0).Pix)
		if !bytes.Equal(frozen, r.render(0).Pix) {
			t.Fatal("disabled motion is not stable")
		}
	}
}

func TestDefaultOverlayMigratesExistingProfiles(t *testing.T) {
	if DefaultSettings().GamingOverlayStyle != "default" {
		t.Fatal("new profiles must use Default")
	}
	for _, legacy := range []string{"", "bars", "mist-aurora", "default"} {
		s := DefaultSettings()
		s.GamingOverlayStyle, s.GamingOverlayAnimate = legacy, false
		got := normalizeSettings(s)
		if got.GamingOverlayStyle != "default" || got.GamingOverlayAnimate {
			t.Fatalf("profile %q did not migrate without changing motion: %+v", legacy, got)
		}
		if snapshot := normalizeGamingOverlay(GamingOverlaySnapshot{Active: true, Style: legacy}); snapshot.Style != "default" {
			t.Fatalf("snapshot %q did not migrate", legacy)
		}
	}
}

func TestDefaultOverlayCompactOverlapProtectsEveryName(t *testing.T) {
	s := normalizeGamingOverlay(GamingOverlaySnapshot{Active: true, Scale: 100, Speakers: []GamingOverlaySpeaker{
		{Name: "TheLegendaryNightwalker", Speaking: true}, {Name: "Sam", Speaking: true},
	}})
	r, err := newOverlayRenderer(s, 800, 800, mistTestLabel)
	if err != nil {
		t.Fatal(err)
	}
	if r.mist == nil || r.base.Bounds().Dy() != 256 || r.mist.rows[1].center-r.mist.rows[0].center != 72 {
		t.Fatal("Default must use compact mist/aurora rows with outer animation space")
	}
	for _, row := range r.mist.rows {
		for y := row.text.Min.Y; y < row.text.Max.Y; y++ {
			for x := row.text.Min.X; x < row.text.Max.X; x++ {
				if r.mist.mask.AlphaAt(x, y).A > 26 {
					t.Fatal("overlapping rows overwrite another name's opacity cap")
				}
			}
		}
	}
}

func TestDefaultOverlayUsesEachMembersConfiguredTint(t *testing.T) {
	s := normalizeGamingOverlay(GamingOverlaySnapshot{Active: true, Scale: 100, Speakers: []GamingOverlaySpeaker{
		{Name: "Alice", Speaking: true, Color: "#43c798"}, {Name: "Sam", Speaking: true, Color: "#369dc7"},
	}})
	r, err := newOverlayRenderer(s, 800, 800, mistTestLabel)
	if err != nil {
		t.Fatal(err)
	}
	if r.mist.rows[0].tint == r.mist.rows[1].tint {
		t.Fatal("individual tint lost")
	}
	for i, row := range r.mist.rows {
		if got := r.base.RGBAAt(38, int(row.center)-19); got.G != row.tint.G || got.B != row.tint.B {
			t.Fatalf("member %d border lost its tint: %v", i, got)
		}
	}
	for _, invalid := range []string{"", "#invalid", strings.Repeat("a", 100)} {
		if overlayTint(invalid) != overlayTint("") {
			t.Fatal("invalid tint did not use safe default")
		}
	}
	for _, custom := range []string{"#ff0000", "#ffffff", "#000000", "#00ff00"} {
		if normalizeOverlayColor(custom) != custom {
			t.Fatalf("configured color was changed: %s", custom)
		}
	}
}

func TestMistAuroraOverlayPreferenceRoundtrip(t *testing.T) {
	s := DefaultSettings()
	s.GamingOverlayStyle = "mist-aurora"
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := saveSettingsSnapshot(path, s); err != nil {
		t.Fatal(err)
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var loaded Settings
	if err := json.Unmarshal(encoded, &loaded); err != nil {
		t.Fatal(err)
	}
	if loaded.GamingOverlayStyle != "default" {
		t.Fatal("design did not persist to settings file")
	}
	got := overlayPresentation(GamingOverlaySnapshot{Active: true, Speakers: []GamingOverlaySpeaker{{Name: "Sam", Speaking: true}}}, normalizeSettings(s))
	if got.Style != "default" {
		t.Fatalf("style lost: %q", got.Style)
	}
	s.GamingOverlayStyle = "invalid"
	if normalizeSettings(s).GamingOverlayStyle != "default" {
		t.Fatal("unknown style must fall back to Default")
	}
}

func TestMistAuroraOverlayFitsEightSpeakersOnSmallWorkArea(t *testing.T) {
	for _, scale := range []int{75, 80, 100, 200} {
		s := GamingOverlaySnapshot{Active: true, Style: "mist-aurora", Scale: scale}
		for range 8 {
			s.Speakers = append(s.Speakers, GamingOverlaySpeaker{Name: strings.Repeat("W", 48), Speaking: true})
		}
		r, err := newOverlayRenderer(normalizeGamingOverlay(s), 320, 600, mistTestLabel)
		if err != nil {
			t.Fatal(err)
		}
		if r.frame.Bounds().Dx() > 320 || r.frame.Bounds().Dy() > 600 {
			t.Fatal("overlay left work area")
		}
		for _, person := range r.mist.rows {
			if !person.text.In(r.base.Bounds()) {
				t.Fatalf("speaker name clipped: %v in %v", person.text, r.base.Bounds())
			}
		}
		r.render(.3)
	}
}

func BenchmarkMistAuroraOverlayEightSpeakers(b *testing.B) {
	s := GamingOverlaySnapshot{Active: true, Style: "mist-aurora", Scale: 100}
	for range 8 {
		s.Speakers = append(s.Speakers, GamingOverlaySpeaker{Name: "TheLegendaryNightwalker", Speaking: true})
	}
	r, err := newOverlayRenderer(normalizeGamingOverlay(s), 1920, 1080, mistTestLabel)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.render(float64(i) / 20)
	}
}
