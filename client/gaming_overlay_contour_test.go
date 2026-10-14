package main

import (
	"bytes"
	"image"
	"image/color"
	"testing"
)

func TestOverlayNameContourPreservesGlyphEdgesAndSpaces(t *testing.T) {
	mask := image.NewAlpha(image.Rect(0, 0, 13, 16))
	for _, ink := range []struct{ x, top, bottom int }{{0, 3, 8}, {5, 8, 15}, {11, 4, 10}} {
		for y := ink.top; y < ink.bottom; y++ {
			mask.SetAlpha(ink.x, y, color.Alpha{A: 255})
		}
	}
	columns := overlayNameContour(mask, image.Pt(70, 79), 1)
	want := []overlayContourColumn{{70, 82, 87}, {72.5, 87, 94}, {75, 87, 94}, {77.5, 87, 94}, {80, 83, 89}}
	if len(columns) != len(want) {
		t.Fatalf("contour columns = %v", columns)
	}
	for i := range want {
		if columns[i] != want[i] {
			t.Fatalf("column %d = %+v, want %+v", i, columns[i], want[i])
		}
	}
	if got := overlayNameContour(image.NewAlpha(mask.Bounds()), image.Point{}, 1); len(got) != 0 {
		t.Fatal("empty text produced a rectangular comb")
	}
}

func TestOverlayRejectsUnavailableWorkAreaBeforeRasterizing(t *testing.T) {
	label := func(_ string, _, _, _ int, _ bool) (*image.Alpha, error) {
		t.Fatal("invalid work area reached font rasterization")
		return nil, nil
	}
	for _, size := range [][3]int{{0, 600, 100}, {800, 0, 100}, {800, 600, 0}} {
		if renderer, err := newOverlayRenderer(GamingOverlaySnapshot{Scale: size[2]}, size[0], size[1], label); err == nil || renderer != nil {
			t.Fatal("unavailable monitor work area must return an error instead of dividing by zero")
		}
	}
}

func TestSelectedOverlayPresetAndCachedForeground(t *testing.T) {
	if overlayMotionSpeed != 2.4 || overlayCombAmplitude != 8 {
		t.Fatal("selected 2.4x / 8px preset changed")
	}
	calls := 0
	label := func(text string, width, height, font int, centered bool) (*image.Alpha, error) {
		calls++
		mask := image.NewAlpha(image.Rect(0, 0, width, height))
		for x := 0; x < min(width, len([]rune(text))*8); x++ {
			if x%8 < 4 {
				for y := height/2 - 4; y < height/2+4; y++ {
					mask.SetAlpha(x, y, color.Alpha{A: 255})
				}
			}
		}
		return mask, nil
	}
	r, err := newOverlayRenderer(GamingOverlaySnapshot{Scale: 100, Speakers: []GamingOverlaySpeaker{{ID: "test", Name: "Nightshade", Speaking: true, Color: "#43c798"}}}, 800, 600, label)
	if err != nil {
		t.Fatal(err)
	}
	cached := calls
	first := bytes.Clone(r.render(0).Pix)
	frame := r.render(.2)
	if bytes.Equal(first, frame.Pix) {
		t.Fatal("halo and comb did not animate")
	}
	for y := range r.base.Bounds().Dy() {
		for x := range r.base.Bounds().Dx() {
			if r.base.RGBAAt(x, y).A == 255 {
				p := frame.PixOffset(x, y)
				if !bytes.Equal(first[p:p+4], frame.Pix[p:p+4]) {
					t.Fatalf("animation changed opaque foreground at %d,%d", x, y)
				}
			}
		}
	}
	if calls != cached {
		t.Fatal("animation rasterized glyphs again")
	}
	if allocations := testing.AllocsPerRun(3, func() { r.render(.5) }); allocations != 0 {
		t.Fatalf("animation allocated per frame: %v", allocations)
	}
	static := bytes.Clone(r.render(0).Pix)
	if !bytes.Equal(static, r.render(0).Pix) {
		t.Fatal("disabled motion is not stable")
	}
}
