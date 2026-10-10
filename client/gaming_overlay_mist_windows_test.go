//go:build windows

package main

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeMistAuroraLabelsAndFrames(t *testing.T) {
	s := normalizeGamingOverlay(GamingOverlaySnapshot{Active: true, Style: "mist-aurora", Scale: 100,
		Speakers: []GamingOverlaySpeaker{{Name: "Sam", Speaking: true}, {Name: "NoxaUser", Speaking: true}, {Name: "TheLegendaryNightwalker", Speaking: true}},
	})
	r, err := newOverlayRenderer(s, 1920, 1080, nativeOverlayLabel)
	if err != nil {
		t.Fatal(err)
	}
	previous := 0.0
	for _, person := range r.mist.rows {
		if person.width <= previous {
			t.Fatal("native full-name wave width did not grow")
		}
		previous = person.width
	}
	if directory := os.Getenv("NOXA_OVERLAY_PREVIEW_PATH"); directory != "" {
		file, err := os.Create(filepath.Join(directory, "native-mist-aurora.png"))
		if err != nil {
			t.Fatal(err)
		}
		err = png.Encode(file, r.render(.7))
		closeErr := file.Close()
		if err != nil {
			t.Fatal(err)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
	}
}
