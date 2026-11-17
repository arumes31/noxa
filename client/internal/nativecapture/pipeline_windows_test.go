//go:build windows

package nativecapture

import (
	"os"
	"strconv"
	"sync"
	"testing"
	"time"
)

// TestMonitorCaptureEncodes runs the full pipeline on the primary monitor.
// NOXA_NATIVE_CAPTURE=1 enables it (needs an interactive desktop);
// NOXA_NATIVE_SOFTWARE=1 admits the software MFT (GPU-less CI runners).
func TestMonitorCaptureEncodes(t *testing.T) {
	if os.Getenv("NOXA_NATIVE_CAPTURE") == "" {
		t.Skip("set NOXA_NATIVE_CAPTURE=1 on an interactive Windows desktop")
	}
	fps, _ := strconv.Atoi(os.Getenv("NOXA_NATIVE_FPS"))
	if fps == 0 {
		fps = 60
	}
	seconds := 5
	monitor := 0
	for i, m := range Monitors() {
		if m.Primary {
			monitor = i
		}
	}
	var mu sync.Mutex
	var frames []Frame
	var dump *os.File
	if path := os.Getenv("NOXA_NATIVE_DUMP"); path != "" {
		var err error
		if dump, err = os.Create(path); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = dump.Close() }()
	}
	s, err := Start(Config{Source: Source{Monitor: monitor}, MaxWidth: 1920, MaxHeight: 1080, FPS: fps, Bitrate: 8_000_000,
		AllowSoftware: os.Getenv("NOXA_NATIVE_SOFTWARE") != ""}, func(f Frame) {
		mu.Lock()
		frames = append(frames, Frame{Keyframe: f.Keyframe, Width: f.Width, Height: f.Height, SeqHeader: f.SeqHeader, Data: f.Data[:min(len(f.Data), 64)]})
		mu.Unlock()
		if dump != nil {
			_, _ = dump.Write(f.Data)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	info := s.Info()
	t.Logf("encoder %q vendor=%q hardware=%v", info.Name, info.Vendor, info.Hardware)
	time.Sleep(time.Duration(seconds) * time.Second)
	s.RequestKeyframe()
	time.Sleep(200 * time.Millisecond)
	st := s.Stats()
	s.Close()
	select {
	case err := <-s.Fatal():
		t.Fatal(err)
	default:
	}
	mu.Lock()
	defer mu.Unlock()
	keys := 0
	for _, f := range frames {
		if f.Keyframe {
			keys++
		}
	}
	elapsed := float64(seconds) + 0.2
	t.Logf("%dx%d captured=%d (%.1f fps) encoded=%d (%.1f fps) keyframes=%d avg encode latency=%v",
		st.Width, st.Height, st.Captured, float64(st.Captured)/elapsed, st.Encoded, float64(st.Encoded)/elapsed, keys,
		st.EncodeTime/time.Duration(max(st.Encoded, 1)))
	if len(frames) == 0 || !frames[0].Keyframe || keys < 2 {
		t.Fatalf("frames=%d keyframes=%d first=%+v", len(frames), keys, frames[:min(1, len(frames))])
	}
	t.Logf("first AU prefix % x seq=% x", frames[0].Data, frames[0].SeqHeader)
}

func runCapture(t *testing.T, src Source, fps int, d time.Duration) (Stats, EncoderInfo, *Session) {
	t.Helper()
	s, err := Start(Config{Source: src, MaxWidth: 1920, MaxHeight: 1080, FPS: fps, Bitrate: 8_000_000,
		AllowSoftware: os.Getenv("NOXA_NATIVE_SOFTWARE") != ""}, func(Frame) {})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(d)
	return s.Stats(), s.Info(), s
}

// TestWindowCaptureEndsWhenWindowCloses captures an animated test window and
// expects ErrSourceClosed once it is destroyed.
func TestWindowCaptureEndsWhenWindowCloses(t *testing.T) {
	if os.Getenv("NOXA_NATIVE_CAPTURE") == "" {
		t.Skip("set NOXA_NATIVE_CAPTURE=1 on an interactive Windows desktop")
	}
	win := startAnimatedWindow(t, false, 640, 480)
	st, info, s := runCapture(t, Source{Window: uint64(win.hwnd.Load())}, 30, 2*time.Second)
	defer s.Close()
	t.Logf("window %dx%d captured=%d encoded=%d encoder=%q", st.Width, st.Height, st.Captured, st.Encoded, info.Name)
	if st.Captured < 20 || st.Encoded < 20 {
		t.Fatalf("too few frames: %+v", st)
	}
	win.close()
	select {
	case err := <-s.Fatal():
		if err != ErrSourceClosed {
			t.Fatalf("fatal = %v, want ErrSourceClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("closing the window did not end the capture")
	}
}

// TestFocusedFullscreenFPS measures capture/encode rates at 1080p60 while a
// topmost fullscreen window holds the focus and repaints continuously.
func TestFocusedFullscreenFPS(t *testing.T) {
	if os.Getenv("NOXA_NATIVE_CAPTURE") == "" {
		t.Skip("set NOXA_NATIVE_CAPTURE=1 on an interactive Windows desktop")
	}
	monitor := 0
	var mw, mh int
	for i, m := range Monitors() {
		if m.Primary {
			monitor, mw, mh = i, m.Width, m.Height
		}
	}
	win := startAnimatedWindow(t, true, mw, mh)
	defer win.close()
	before := win.paints.Load()
	const seconds = 6
	st, info, s := runCapture(t, Source{Monitor: monitor}, 60, seconds*time.Second)
	s.Close()
	paints := float64(win.paints.Load()-before) / seconds
	t.Logf("encoder=%q hardware=%v out=%dx%d app=%.1f fps capture=%.1f fps encoded=%.1f fps avg latency=%v",
		info.Name, info.Hardware, st.Width, st.Height, paints, float64(st.Captured)/seconds, float64(st.Encoded)/seconds,
		st.EncodeTime/time.Duration(max(st.Encoded, 1)))
	if float64(st.Encoded)/seconds < 20 {
		t.Fatalf("encoded fps too low: %+v", st)
	}
}
