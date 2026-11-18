//go:build windows

package main

import (
	"crypto/rand"
	"os"
	"testing"
	"time"

	"noxa/client/internal/nativecapture"
	"noxa/internal/nativesender/sendertest"
)

// TestNativeCallShareRealCapture runs real monitor capture and Media
// Foundation encode through the call share path (nativeShare with a
// nativeCall) to an in-process viewer that decrypts every frame with the
// call's key. NOXA_NATIVE_CAPTURE=1 enables it on an interactive desktop.
func TestNativeCallShareRealCapture(t *testing.T) {
	if os.Getenv("NOXA_NATIVE_CAPTURE") == "" {
		t.Skip("set NOXA_NATIVE_CAPTURE=1 on an interactive Windows desktop")
	}
	n, viewer := testCallShare(t)
	n.req = NativeShareRequest{ID: "share", Source: nativecapture.Source{Monitor: 0}, MaxWidth: 1920, MaxHeight: 1080, FPS: 60, Bitrate: 6_000_000, Audio: "none"}
	n.audioMode = "none"
	material := make([]byte, 32)
	_, _ = rand.Read(material)
	if err := n.keys.SetKey(3, 1, material); err != nil {
		t.Fatal(err)
	}
	if err := n.keys.Activate(3); err != nil {
		t.Fatal(err)
	}
	if err := n.call.setTargets([]string{"viewer"}); err != nil {
		t.Fatal(err)
	}
	if err := n.start(); err != nil {
		t.Fatal(err)
	}
	waitConnected(t, n.call)
	r := viewer.current()
	frames, start := 0, time.Now()
	for time.Since(start) < 4*time.Second {
		select {
		case frame := <-r.Frames:
			sendertest.Decrypt(t, material, frame)
			frames++
		case <-time.After(2 * time.Second):
			t.Fatalf("no frame after %d", frames)
		}
	}
	stats := n.session.Stats()
	t.Logf("viewer decrypted %d frames in 4 s (%.1f fps); captured %d encoded %d %dx%d", frames, float64(frames)/4, stats.Captured, stats.Encoded, stats.Width, stats.Height)
	if frames < 8 {
		t.Fatalf("only %d frames reached the viewer", frames)
	}
}
