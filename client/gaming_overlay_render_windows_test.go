//go:build windows

package main

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestNativeOverlayLabelsAndResolutionPreviews(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	mask, err := nativeOverlayLabel("Alex — speaking", 208, 26, 16, false)
	if err != nil {
		t.Fatal(err)
	}
	ink := 0
	for _, alpha := range mask.Pix {
		if alpha > 0 {
			ink++
		}
	}
	if ink < 50 {
		t.Fatal("native label rasterized without readable glyphs")
	}
	s := overlayPresentation(GamingOverlaySnapshot{Active: true, Speakers: []GamingOverlaySpeaker{
		{ID: "alex", Name: "Alex", Speaking: true},
		{ID: "sam", Name: "Sam", Speaking: true},
		{ID: "cari", Name: "Cari", Speaking: true},
	}}, DefaultSettings())
	for _, size := range []image.Point{{1920, 1080}, {2560, 1440}, {3840, 2160}} {
		monitor := GamingOverlayMonitor{workWidth: int32(size.X), workHeight: int32(size.Y)}
		x, y, w, h := gamingOverlayPlacement(s, monitor)
		r, err := newOverlayRenderer(s, int(w), int(h), nativeOverlayLabel)
		if err != nil {
			t.Fatal(err)
		}
		frame := r.render(0.2)
		if w != 224 || h != 172 {
			t.Fatalf("compact overlay changed size at %v: %dx%d", size, w, h)
		}
		directory := os.Getenv("NOXA_OVERLAY_PREVIEW_PATH")
		if directory == "" {
			continue
		}
		canvas := image.NewRGBA(image.Rect(0, 0, size.X, size.Y))
		draw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.RGBA{35, 42, 50, 255}), image.Point{}, draw.Src)
		grid := image.NewUniform(color.RGBA{42, 49, 57, 255})
		for px := 0; px < size.X; px += 120 {
			draw.Draw(canvas, image.Rect(px, 0, px+1, size.Y), grid, image.Point{}, draw.Src)
		}
		for py := 0; py < size.Y; py += 120 {
			draw.Draw(canvas, image.Rect(0, py, size.X, py+1), grid, image.Point{}, draw.Src)
		}
		origin := image.Pt(int(x), int(y))
		draw.DrawMask(canvas, frame.Bounds().Add(origin), frame, image.Point{}, image.NewUniform(color.Alpha{uint8(s.Opacity * 255 / 100)}), image.Point{}, draw.Over)
		title, err := nativeOverlayLabel(fmt.Sprintf("%d × %d · 40 px avatars · 80%% size", size.X, size.Y), 560, 44, 20, false)
		if err != nil {
			t.Fatal(err)
		}
		draw.DrawMask(canvas, title.Bounds().Add(image.Pt(20, size.Y-60)), image.White, image.Point{}, title, image.Point{}, draw.Over)
		file, err := os.Create(filepath.Join(directory, fmt.Sprintf("overlay-middle-left-%dx%d.png", size.X, size.Y)))
		if err != nil {
			t.Fatal(err)
		}
		err = png.Encode(file, canvas)
		closeErr := file.Close()
		if err != nil {
			t.Fatal(err)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
	}
}
