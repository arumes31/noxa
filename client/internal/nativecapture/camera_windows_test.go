//go:build windows

package nativecapture

import (
	"image"
	"image/png"
	"os"
	"testing"
	"time"
)

// TestCameraDeliversFrames opens the first camera, waits for a frame and,
// with NOXA_NATIVE_DUMP=<file.png>, saves it for a look at its orientation.
// It also opens the same camera a second time (as the webview's camera
// publication would) and logs whether Windows shares it.
// NOXA_NATIVE_CAPTURE=1 on a machine with a camera enables it.
func TestCameraDeliversFrames(t *testing.T) {
	if os.Getenv("NOXA_NATIVE_CAPTURE") == "" {
		t.Skip("set NOXA_NATIVE_CAPTURE=1 on a Windows machine with a camera")
	}
	cams := Cameras()
	if len(cams) == 0 {
		t.Skip("no camera")
	}
	t.Logf("cameras: %+v", cams)
	first := startMFCamera(cams[0].ID)
	defer first.close()
	pix, w, h := waitCamera(t, first)
	if pix == nil {
		return
	}
	// The first frames are dark while exposure settles.
	time.Sleep(2 * time.Second)
	if latest, lw, lh, _, err := first.latest(0); err == nil && latest != nil {
		pix, w, h = latest, lw, lh
	}
	t.Logf("frame %dx%d", w, h)
	if path := os.Getenv("NOXA_NATIVE_DUMP"); path != "" {
		img := image.NewRGBA(image.Rect(0, 0, w, h))
		for i := 0; i < len(pix); i += 4 {
			img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = pix[i+2], pix[i+1], pix[i], 255
		}
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		_ = png.Encode(f, img)
		_ = f.Close()
	}
	second := startMFCamera(cams[0].ID)
	defer second.close()
	if pix, _, _ := waitCamera(t, second); pix != nil {
		t.Log("a second reader shares the camera")
	}
}

func waitCamera(t *testing.T, c *mfCamera) ([]byte, int, int) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		pix, w, h, _, err := c.latest(0)
		if err != nil {
			t.Logf("camera error: %v", err)
			return nil, 0, 0
		}
		if pix != nil {
			return pix, w, h
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Log("camera delivered no frame")
	return nil, 0, 0
}
