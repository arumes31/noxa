package nativecapture

import (
	"bytes"
	"math"
	"testing"
	"time"
)

func TestCropRectMatchesFrameGeometry(t *testing.T) {
	cases := []struct {
		w, h int
		r    *Region
		want pixelRect
	}{
		{1920, 1080, nil, pixelRect{0, 0, 1920, 1080}},
		{1920, 1080, &Region{0.25, 0.25, 0.5, 0.5}, pixelRect{480, 270, 960, 540}},
		// Odd products round down to even values (share-frame-geometry.js cropRect).
		{1001, 777, &Region{0.1, 0.1, 0.3, 0.3}, pixelRect{100, 76, 300, 232}},
		// Out of range values stay inside the frame, at least 2 x 2.
		{640, 480, &Region{0.999, 1.5, 0.5, -1}, pixelRect{638, 478, 2, 2}},
		{640, 480, &Region{-1, 0, 2, 1}, pixelRect{0, 0, 640, 480}},
	}
	for _, c := range cases {
		if got := cropRect(c.w, c.h, c.r); got != c.want {
			t.Errorf("cropRect(%d, %d, %+v) = %+v, want %+v", c.w, c.h, c.r, got, c.want)
		}
	}
}

func TestPlacementFitsCroppedPictureInBox(t *testing.T) {
	// A 16:9 picture in the left half of a 16:9 canvas: full width, centred vertically.
	dst, uv := placement(Layer{X: 0, Y: 0, W: 0.5, H: 1}, 1280, 720, 1920, 1080)
	if !nearRect(dst, rectF{0, 0.25, 0.5, 0.5}) || uv != (rectF{0, 0, 1, 1}) {
		t.Fatalf("placement = %+v %+v", dst, uv)
	}
	// Cropping half the width away makes the picture 8:9: it fills the box height.
	dst, uv = placement(Layer{X: 0, Y: 0, W: 0.5, H: 1, Crop: Crop{L: 0.25, R: 0.25}}, 1280, 720, 1920, 1080)
	if !nearRect(dst, rectF{0, 0, 0.5, 1}) || !nearRect(uv, rectF{0.25, 0, 0.5, 1}) {
		t.Fatalf("cropped placement = %+v %+v", dst, uv)
	}
	// A square camera in a wide box is centred horizontally.
	dst, _ = placement(Layer{X: 0.5, Y: 0.5, W: 0.5, H: 0.5}, 1000, 1000, 64, 64)
	if !nearRect(dst, rectF{0.5, 0.5, 0.5, 0.5}) {
		t.Fatalf("square placement = %+v", dst)
	}
	dst, _ = placement(Layer{X: 0, Y: 0, W: 1, H: 0.5}, 1000, 1000, 64, 64)
	if !nearRect(dst, rectF{0.25, 0, 0.5, 0.5}) {
		t.Fatalf("wide box placement = %+v", dst)
	}
	// No picture yet: the box itself (drawn neutral).
	if dst, _ = placement(Layer{X: 0.1, Y: 0.2, W: 0.3, H: 0.4}, 1280, 720, 0, 0); dst != (rectF{0.1, 0.2, 0.3, 0.4}) {
		t.Fatalf("empty placement = %+v", dst)
	}
}

func nearRect(a, b rectF) bool {
	const eps = 1e-9
	return math.Abs(a.X-b.X) < eps && math.Abs(a.Y-b.Y) < eps && math.Abs(a.W-b.W) < eps && math.Abs(a.H-b.H) < eps
}

func TestFadeProgress(t *testing.T) {
	start := time.Unix(100, 0)
	for _, c := range []struct {
		after time.Duration
		ms    int
		p     float64
		done  bool
	}{
		{0, 300, 0, false},
		{150 * time.Millisecond, 300, 0.5, false},
		{300 * time.Millisecond, 300, 1, true},
		{time.Second, 300, 1, true},
		{-time.Second, 300, 0, false}, // clock step back
		{0, 0, 1, true},
	} {
		p, done := fadeProgress(start, start.Add(c.after), c.ms)
		if math.Abs(p-c.p) > 1e-9 || done != c.done {
			t.Errorf("fadeProgress(+%v, %d) = %v %v, want %v %v", c.after, c.ms, p, done, c.p, c.done)
		}
	}
}

func TestClampSceneBoundsUntrustedInput(t *testing.T) {
	in := Scene{Take: 7, Transition: Transition{Kind: "wipe", MS: 99999}, Layers: []Layer{
		{Kind: LayerDisplay, Source: Source{Monitor: 1, Studio: true, Region: &Region{}}, Camera: "x", X: 0.9, W: 0.5, H: math.NaN(), Crop: Crop{L: 2, T: -1}},
		{Kind: LayerOverlay, Overlay: 3},
		{Kind: LayerOverlay, Overlay: 0, X: 0.5},
		{Kind: "script"},
		{Kind: LayerCamera, Camera: "cam", Source: Source{Window: 5}, Audio: true, W: 1, H: 1},
	}}
	out := clampScene(in, 1)
	if out.Transition != (Transition{"fade", 1500}) || out.Take != 7 || len(out.Layers) != 3 {
		t.Fatalf("clampScene = %+v", out)
	}
	d := out.Layers[0]
	if d.Source != (Source{Monitor: 1}) || d.Camera != "" || d.W != 0.5 || d.X != 0.5 || d.H != 0.02 || d.Crop != (Crop{L: 0.45}) {
		t.Fatalf("display layer = %+v", d)
	}
	if out.Layers[1] != (Layer{Kind: LayerOverlay}) {
		t.Fatalf("overlay layer = %+v", out.Layers[1])
	}
	if c := out.Layers[2]; c.Source != (Source{}) || c.Audio || c.Camera != "cam" {
		t.Fatalf("camera layer = %+v", c)
	}
	many := Scene{}
	for range 40 {
		many.Layers = append(many.Layers, Layer{Kind: LayerCamera, Camera: "c"})
	}
	if n := len(clampScene(many, 0).Layers); n != MaxStudioLayers {
		t.Fatalf("layers = %d, want %d", n, MaxStudioLayers)
	}
}

func TestSceneSourcesDeduplicates(t *testing.T) {
	a := &Scene{Layers: []Layer{{Kind: LayerDisplay, Source: Source{Monitor: 0}}, {Kind: LayerCamera, Camera: "c1"}, {Kind: LayerOverlay}}}
	b := &Scene{Layers: []Layer{{Kind: LayerDisplay, Source: Source{Monitor: 0}}, {Kind: LayerDisplay, Source: Source{Window: 9}}, {Kind: LayerCamera, Camera: "c1"}, {Kind: LayerCamera}}}
	displays, cameras := sceneSources(a, nil, b)
	if len(displays) != 2 || displays[1] != (Source{Window: 9}) || len(cameras) != 1 || cameras[0] != "c1" {
		t.Fatalf("sceneSources = %v %v", displays, cameras)
	}
}

func TestBGRARowsFlipsBottomUpAndForcesAlpha(t *testing.T) {
	// 2 x 2, rows padded to 12 bytes, bottom-up.
	src := []byte{
		1, 1, 1, 0, 2, 2, 2, 0, 9, 9, 9, 9, // bottom row
		3, 3, 3, 0, 4, 4, 4, 0, 9, 9, 9, 9, // top row
	}
	want := []byte{3, 3, 3, 255, 4, 4, 4, 255, 1, 1, 1, 255, 2, 2, 2, 255}
	if got := bgraRows(src, 2, 2, -12); !bytes.Equal(got, want) {
		t.Fatalf("bottom-up = %v", got)
	}
	if got := bgraRows(src, 2, 2, 12); !bytes.Equal(got[:8], []byte{1, 1, 1, 255, 2, 2, 2, 255}) {
		t.Fatalf("top-down = %v", got)
	}
	if bgraRows(src[:19], 2, 2, 12) != nil || bgraRows(src, 4, 2, 12) != nil {
		t.Fatal("short buffers must be refused")
	}
}
