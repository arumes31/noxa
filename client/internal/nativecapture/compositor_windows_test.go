//go:build windows

package nativecapture

import (
	"image"
	"image/color"
	"os"
	"runtime"
	"testing"
	"time"
	"unsafe"
)

type fakeCamera struct {
	pix  []byte
	w, h int
	err  error
}

func (f *fakeCamera) latest(seq uint64) ([]byte, int, int, uint64, error) {
	if f.err != nil {
		return nil, 0, 0, seq, f.err
	}
	if seq == 1 {
		return nil, 0, 0, seq, nil
	}
	return f.pix, f.w, f.h, 1, nil
}
func (f *fakeCamera) close() {}

// readBack copies a BGRA texture to the CPU.
func readBack(t *testing.T, d *device, tex uintptr, w, h int) []byte {
	t.Helper()
	desc := texture2DDesc{Width: uint32(w), Height: uint32(h), MipLevels: 1, ArraySize: 1, Format: dxgiFormatB8G8R8A8, SampleCount: 1, Usage: 3, CPUAccessFlags: 0x20000}
	var staging uintptr
	if hr := vcall(d.dev, 5, uintptr(unsafe.Pointer(&desc)), 0, uintptr(unsafe.Pointer(&staging))); hr.failed() {
		t.Fatal(hr.err("CreateTexture2D staging"))
	}
	defer release(staging)
	vcall(d.ctx, 47, staging, tex) // CopyResource
	var mapped struct {
		data              uintptr
		rowPitch, depthPx uint32
	}
	if hr := vcall(d.ctx, 14, staging, 0, 1, 0, uintptr(unsafe.Pointer(&mapped))); hr.failed() {
		t.Fatal(hr.err("Map"))
	}
	defer vcall(d.ctx, 15, staging, 0)
	out := make([]byte, w*h*4)
	src := unsafe.Slice((*byte)(unsafe.Pointer(mapped.data)), int(mapped.rowPitch)*h) //nolint:govet
	for y := range h {
		copy(out[y*w*4:(y+1)*w*4], src[y*int(mapped.rowPitch):])
	}
	return out
}

// pixel returns R, G, B at (x, y) of a BGRA buffer w wide.
func pixel(buf []byte, w, x, y int) [3]int {
	i := (y*w + x) * 4
	return [3]int{int(buf[i+2]), int(buf[i+1]), int(buf[i])}
}

func near(got [3]int, want [3]int, tol int) bool {
	for i := range got {
		if d := got[i] - want[i]; d > tol || d < -tol {
			return false
		}
	}
	return true
}

// TestCompositorComposesSceneOverlayAndFade composes a solid green test
// window, a synthetic blue camera, a missing camera and a half-transparent
// red overlay square, checks the pixels, then fades to an empty scene.
// NOXA_NATIVE_CAPTURE=1 on an interactive Windows desktop enables it.
func TestCompositorComposesSceneOverlayAndFade(t *testing.T) {
	if os.Getenv("NOXA_NATIVE_CAPTURE") == "" {
		t.Skip("set NOXA_NATIVE_CAPTURE=1 on an interactive Windows desktop")
	}
	win := startAnimatedWindow(t, false, 400, 300)
	defer win.close()
	win.solid.Store(0x00FF00 | 1<<24)
	time.Sleep(300 * time.Millisecond)

	blue := make([]byte, 64*64*4)
	for i := 0; i < len(blue); i += 4 {
		blue[i], blue[i+3] = 255, 255
	}
	saved := openCamera
	openCamera = func(id string) cameraFrames {
		if id == "busy" {
			return &fakeCamera{err: ErrCameraBusy}
		}
		return &fakeCamera{pix: blue, w: 64, h: 64}
	}
	defer func() { openCamera = saved }()

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	initThread()
	d, err := newDevice(0)
	if err != nil {
		t.Skipf("no D3D11 device: %v", err)
	}
	defer d.close()
	const W, H = 640, 360
	overlay := image.NewNRGBA(image.Rect(0, 0, W, H))
	for y := 100; y < 180; y++ {
		for x := 480; x < 560; x++ {
			overlay.SetNRGBA(x, y, color.NRGBA{255, 0, 0, 128})
		}
	}
	inbox := &studioInbox{}
	scene := Scene{Take: 1, Transition: Transition{Kind: "cut"}, Layers: []Layer{
		{Kind: LayerDisplay, Source: Source{Window: uint64(win.hwnd.Load())}, X: 0, Y: 0, W: 0.5, H: 1},
		{Kind: LayerCamera, Camera: "cam", X: 0.5, Y: 0.6, W: 0.25, H: 0.4},
		{Kind: LayerCamera, Camera: "busy", X: 0.75, Y: 0.6, W: 0.25, H: 0.4},
		{Kind: LayerOverlay, Overlay: 0},
	}}
	inbox.next.Store(&studioUpdate{scene: clampScene(scene, 1), images: []*image.NRGBA{overlay}})
	c, err := newCompositor(d, W, H, inbox)
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	var buf []byte
	render := func() {
		got, err := c.latest(func(tex uintptr, w, h uint32) { buf = readBack(t, d, tex, int(w), int(h)) })
		if err != nil || !got {
			t.Fatalf("latest: %v %v", got, err)
		}
	}
	// WGC needs a moment for the first window frame.
	deadline := time.Now().Add(3 * time.Second)
	for render(); time.Now().Before(deadline) && pixel(buf, W, 160, 180)[1] < 200; render() {
		time.Sleep(30 * time.Millisecond)
	}
	if got := pixel(buf, W, 160, 180); got[1] < 200 || got[0] > 60 || got[2] > 60 {
		t.Fatalf("window pixel = %v, want green", got)
	}
	if got := pixel(buf, W, 400, 290); !near(got, [3]int{0, 0, 255}, 8) {
		t.Fatalf("camera pixel = %v, want blue", got)
	}
	if got := pixel(buf, W, 560, 290); !near(got, [3]int{0x11, 0x16, 0x1c}, 4) {
		t.Fatalf("busy camera pixel = %v, want neutral", got)
	}
	if got := pixel(buf, W, 520, 140); !near(got, [3]int{128, 0, 0}, 8) {
		t.Fatalf("overlay pixel = %v, want half red over black", got)
	}
	if got := pixel(buf, W, 600, 40); !near(got, [3]int{0, 0, 0}, 2) {
		t.Fatalf("background pixel = %v, want black", got)
	}
	if inbox.cameraError() != ErrCameraBusy.Error() {
		t.Fatal("busy camera not reported")
	}

	// Fade to an empty scene: half-way the window is half as bright.
	inbox.next.Store(&studioUpdate{scene: Scene{Take: 2, Transition: Transition{Kind: "fade", MS: 1000}}})
	render()
	c.fadeStart = time.Now().Add(-500 * time.Millisecond)
	render()
	if got := pixel(buf, W, 160, 180); got[1] < 100 || got[1] > 160 {
		t.Fatalf("mid-fade window pixel = %v, want about half green", got)
	}
	c.fadeStart = time.Now().Add(-2 * time.Second)
	render()
	if got := pixel(buf, W, 160, 180); !near(got, [3]int{0, 0, 0}, 2) || c.outgoing != nil {
		t.Fatalf("after fade pixel = %v (outgoing %v), want black", got, c.outgoing != nil)
	}
	if len(c.displays) != 0 || len(c.cameras) != 0 {
		t.Fatalf("sources left open after the fade: %d displays, %d cameras", len(c.displays), len(c.cameras))
	}
}

// TestRegionConvertCopiesTheCroppedArea feeds a four-colour texture through
// the pipeline's convert with a bottom-right Region: the converter input
// holds only that quadrant. NOXA_NATIVE_CAPTURE=1 enables it.
func TestRegionConvertCopiesTheCroppedArea(t *testing.T) {
	if os.Getenv("NOXA_NATIVE_CAPTURE") == "" {
		t.Skip("set NOXA_NATIVE_CAPTURE=1 on an interactive Windows desktop")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	initThread()
	d, err := newDevice(0)
	if err != nil {
		t.Skipf("no D3D11 device: %v", err)
	}
	defer d.close()
	const n = 128
	pix := make([]byte, n*n*4)
	for y := range n {
		for x := range n {
			i := (y*n + x) * 4
			// BGRA quadrants: red, green / blue, white.
			switch {
			case x < n/2 && y < n/2:
				pix[i+2] = 255
			case y < n/2:
				pix[i+1] = 255
			case x < n/2:
				pix[i] = 255
			default:
				pix[i], pix[i+1], pix[i+2] = 255, 255, 255
			}
			pix[i+3] = 255
		}
	}
	src, err := d.uploadTexture(pix, n, n, n*4, dxgiFormatB8G8R8A8)
	if err != nil {
		t.Fatal(err)
	}
	defer src.close()
	s := &Session{cfg: Config{MaxWidth: n, MaxHeight: n, FPS: 30, Bitrate: 1_000_000, Source: Source{Region: &Region{X: 0.5, Y: 0.5, Width: 0.5, Height: 0.5}}}}
	p := &pipeline{d: d, last: -1}
	defer p.closeSized()
	r := cropRect(n, n, s.cfg.Source.Region)
	if err := s.ensureSized(p, r.W, r.H); err != nil {
		t.Fatal(err)
	}
	s.convert(p, src.tex, r)
	got := readBack(t, d, p.conv.input, r.W, r.H)
	for _, xy := range [][2]int{{0, 0}, {r.W - 1, 0}, {0, r.H - 1}, {r.W - 1, r.H - 1}, {r.W / 2, r.H / 2}} {
		if c := pixel(got, r.W, xy[0], xy[1]); c != [3]int{255, 255, 255} {
			t.Fatalf("pixel %v = %v, want the white bottom-right quadrant", xy, c)
		}
	}
}
