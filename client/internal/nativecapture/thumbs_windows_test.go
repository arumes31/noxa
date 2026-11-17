//go:build windows

package nativecapture

import (
	"bytes"
	"image/jpeg"
	"os"
	"testing"
	"time"
)

// TestThumbnails renders the primary monitor and a test window as JPEG
// thumbnails and times a full picker refresh (every listed window and
// monitor). NOXA_NATIVE_CAPTURE=1 on an interactive desktop enables it.
func TestThumbnails(t *testing.T) {
	if os.Getenv("NOXA_NATIVE_CAPTURE") == "" {
		t.Skip("set NOXA_NATIVE_CAPTURE=1 on an interactive Windows desktop")
	}
	win := startAnimatedWindow(t, false, 640, 360)
	time.Sleep(300 * time.Millisecond)
	for _, src := range []Source{{Monitor: 0}, {Window: uint64(win.hwnd.Load())}} {
		start := time.Now()
		jpg, err := Thumbnail(src, 320, 180)
		if err != nil {
			t.Fatalf("thumbnail %+v: %v", src, err)
		}
		img, err := jpeg.Decode(bytes.NewReader(jpg))
		if err != nil {
			t.Fatal(err)
		}
		b := img.Bounds()
		if b.Dx() > 320 || b.Dy() > 180 || b.Dx() < 100 {
			t.Fatalf("thumbnail %+v size %v", src, b)
		}
		// Not a blank image: the pattern/desktop has varied pixels.
		r0, g0, b0, _ := img.At(b.Dx()/4, b.Dy()/2).RGBA()
		r1, g1, b1, _ := img.At(3*b.Dx()/4, b.Dy()/3).RGBA()
		t.Logf("%+v: %dx%d, %d bytes, %v, samples %v/%v", src, b.Dx(), b.Dy(), len(jpg), time.Since(start).Round(time.Millisecond), []uint32{r0 >> 8, g0 >> 8, b0 >> 8}, []uint32{r1 >> 8, g1 >> 8, b1 >> 8})
	}
	var sources []Source
	for _, m := range Monitors() {
		sources = append(sources, Source{Monitor: m.Index})
	}
	windows := Windows()
	for _, w := range windows {
		sources = append(sources, Source{Window: w.Handle})
	}
	start := time.Now()
	thumbs := Thumbnails(sources, 320, 180, 5*time.Second)
	t.Logf("refresh: %d of %d sources (%d windows) in %v", len(thumbs), len(sources), len(windows), time.Since(start).Round(time.Millisecond))
	for _, w := range windows {
		t.Logf("window %q exe %q fullscreen %v thumb %v", w.Title, w.Exe, w.Fullscreen, len(thumbs[Source{Window: w.Handle}]) > 0)
	}
}
