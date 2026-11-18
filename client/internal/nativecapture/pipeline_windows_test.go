//go:build windows

package nativecapture

import (
	"fmt"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"noxa/internal/framecrypt"
)

// TestMonitorCaptureEncodes runs the full pipeline on the primary monitor.
// NOXA_NATIVE_CAPTURE=1 enables it (needs an interactive desktop).
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
	s, err := Start(Config{Source: Source{Monitor: monitor}, MaxWidth: 1920, MaxHeight: 1080, FPS: fps, Bitrate: 8_000_000}, func(f Frame) {
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
	s, err := Start(Config{Source: src, MaxWidth: 1920, MaxHeight: 1080, FPS: fps, Bitrate: 8_000_000}, func(Frame) {})
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

// TestLadderControlsMidStream exercises the quality-ladder controls on the
// running pipeline: resolution step (encoder reopen), fps step, bitrate
// target, pause/resume. It logs the produced bitrate against the target.
func TestLadderControlsMidStream(t *testing.T) {
	if os.Getenv("NOXA_NATIVE_CAPTURE") == "" {
		t.Skip("set NOXA_NATIVE_CAPTURE=1 on an interactive Windows desktop")
	}
	win := startAnimatedWindow(t, false, 1280, 800)
	defer win.close()
	var mu sync.Mutex
	var bytes, frames int
	var lastW, lastH int
	s, err := Start(Config{Source: Source{Window: uint64(win.hwnd.Load())}, MaxWidth: 1920, MaxHeight: 1080, FPS: 30, Bitrate: 4_000_000}, func(f Frame) {
		mu.Lock()
		bytes += len(f.Data)
		frames++
		lastW, lastH = f.Width, f.Height
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	measure := func(name string, target int) {
		mu.Lock()
		bytes, frames = 0, 0
		mu.Unlock()
		time.Sleep(2 * time.Second)
		mu.Lock()
		defer mu.Unlock()
		t.Logf("%s: %dx%d %.1f fps %.2f Mbps (target %.2f)", name, lastW, lastH, float64(frames)/2, float64(bytes)*8/2/1e6, float64(target)/1e6)
		if frames < 10 {
			t.Fatalf("%s: only %d frames", name, frames)
		}
		if mbps := float64(bytes) * 8 / 2; mbps > float64(target)*1.5 {
			t.Errorf("%s: %.2f Mbps exceeds target %.2f by over 50%%", name, mbps/1e6, float64(target)/1e6)
		}
	}
	measure("start 1080p30", 4_000_000)
	s.SetMaxSize(640, 400)
	time.Sleep(300 * time.Millisecond)
	measure("after resize", 4_000_000)
	if st := s.Stats(); st.Width > 640 || st.Height > 400 {
		t.Fatalf("resize not applied: %+v", st)
	}
	s.SetBitrate(1_000_000)
	time.Sleep(1500 * time.Millisecond) // encoder reopen is rate limited
	measure("1 Mbps", 1_000_000)
	s.SetFPS(60)
	measure("1 Mbps 60 fps", 1_000_000)
	s.SetPaused(true)
	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	frames = 0
	mu.Unlock()
	time.Sleep(time.Second)
	mu.Lock()
	paused := frames
	mu.Unlock()
	if paused > 2 {
		t.Fatalf("paused pipeline still encoded %d frames", paused)
	}
	s.SetPaused(false)
	measure("resumed", 1_000_000)
	select {
	case err := <-s.Fatal():
		t.Fatal(err)
	default:
	}
}

// TestWindowResizeRebuildsAfterItSettles resizes a window that then stops
// repainting: the debounced rebuild must still reach the new size.
func TestWindowResizeRebuildsAfterItSettles(t *testing.T) {
	if os.Getenv("NOXA_NATIVE_CAPTURE") == "" {
		t.Skip("set NOXA_NATIVE_CAPTURE=1 on an interactive Windows desktop")
	}
	win := startAnimatedWindow(t, false, 640, 480)
	defer win.close()
	var mu sync.Mutex
	var keysAfter int
	var resized bool
	s, err := Start(Config{Source: Source{Window: uint64(win.hwnd.Load())}, MaxWidth: 1920, MaxHeight: 1080, FPS: 30, Bitrate: 2_000_000}, func(f Frame) {
		mu.Lock()
		if resized && f.Keyframe {
			keysAfter++
		}
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	time.Sleep(500 * time.Millisecond)
	before := s.Stats()
	win.still.Store(true)
	mu.Lock()
	resized = true
	mu.Unlock()
	_, _, _ = procSetWindowPos.Call(win.hwnd.Load(), 0, 0, 0, 900, 600, 0x16) // SWP_NOMOVE|SWP_NOZORDER|SWP_NOACTIVATE
	time.Sleep(150 * time.Millisecond)
	if st := s.Stats(); st.Width != before.Width || st.Height != before.Height {
		t.Fatalf("rebuilt before the size settled: %dx%d -> %dx%d", before.Width, before.Height, st.Width, st.Height)
	}
	time.Sleep(time.Second)
	after := s.Stats()
	mu.Lock()
	keys := keysAfter
	mu.Unlock()
	t.Logf("%dx%d -> %dx%d, keyframes after resize=%d", before.Width, before.Height, after.Width, after.Height, keys)
	if after.Width <= before.Width || after.Height <= before.Height || keys == 0 {
		t.Fatalf("static window not rebuilt at its new size: %+v keyframes=%d", after, keys)
	}
	select {
	case err := <-s.Fatal():
		t.Fatal(err)
	default:
	}
}

// TestRegionCaptureEncodesTheCrop shares the middle quarter of the primary
// monitor: the encoded size is the even-aligned crop.
func TestRegionCaptureEncodesTheCrop(t *testing.T) {
	if os.Getenv("NOXA_NATIVE_CAPTURE") == "" {
		t.Skip("set NOXA_NATIVE_CAPTURE=1 on an interactive Windows desktop")
	}
	monitor, size := 0, Monitor{}
	for i, m := range Monitors() {
		if m.Primary {
			monitor, size = i, m
		}
	}
	// An animated window keeps the desktop changing, so WGC delivers frames
	// (a locked or sleeping display delivers only the first one).
	win := startAnimatedWindow(t, false, 900, 600)
	defer win.close()
	region := &Region{X: 0.25, Y: 0.25, Width: 0.5, Height: 0.5}
	st, _, s := runCapture(t, Source{Monitor: monitor, Region: region}, 30, 2*time.Second)
	s.Close()
	// WGC delivers the monitor at its pixel size; the crop is fitted into 1920x1080.
	want := cropRect(size.Width, size.Height, region)
	ww, wh := outputSize(want.W, want.H, 1920, 1080)
	if st.Encoded == 0 || st.Width != ww || st.Height != wh {
		t.Fatalf("region %dx%d encoded=%d, want %dx%d (monitor %dx%d)", st.Width, st.Height, st.Encoded, ww, wh, size.Width, size.Height)
	}
	t.Logf("region %dx%d captured=%d encoded=%d", st.Width, st.Height, st.Captured, st.Encoded)
}

// TestStudioSessionEncodes runs the Studio compositor through the encoder:
// a monitor beside a test window, then a crossfade to another Take.
func TestStudioSessionEncodes(t *testing.T) {
	if os.Getenv("NOXA_NATIVE_CAPTURE") == "" {
		t.Skip("set NOXA_NATIVE_CAPTURE=1 on an interactive Windows desktop")
	}
	win := startAnimatedWindow(t, false, 640, 360)
	defer win.close()
	scene := Scene{Take: 1, Transition: Transition{Kind: "cut"}, Layers: []Layer{
		{Kind: LayerDisplay, Source: Source{Monitor: 0}, W: 0.5, H: 1},
		{Kind: LayerDisplay, Source: Source{Window: uint64(win.hwnd.Load())}, X: 0.5, W: 0.5, H: 1},
	}}
	var mu sync.Mutex
	keys, frames := 0, 0
	s, err := Start(Config{Source: Source{Studio: true}, Scene: scene, MaxWidth: 1280, MaxHeight: 720, FPS: 30, Bitrate: 4_000_000}, func(f Frame) {
		mu.Lock()
		frames++
		if f.Keyframe {
			keys++
		}
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
	s.SetScene(Scene{Take: 2, Transition: Transition{Kind: "fade", MS: 500}, Layers: scene.Layers[1:]}, nil)
	time.Sleep(1500 * time.Millisecond)
	st := s.Stats()
	s.Close()
	select {
	case err := <-s.Fatal():
		t.Fatal(err)
	default:
	}
	mu.Lock()
	defer mu.Unlock()
	t.Logf("studio %dx%d captured=%d encoded=%d keyframes=%d avg latency=%v", st.Width, st.Height, st.Captured, st.Encoded, keys,
		st.EncodeTime/time.Duration(max(st.Encoded, 1)))
	if st.Width != 1280 || st.Height != 720 || frames < 45 || keys < 1 {
		t.Fatalf("studio %dx%d frames=%d keyframes=%d", st.Width, st.Height, frames, keys)
	}
}

func TestSupportedOnThisDesktop(t *testing.T) {
	if os.Getenv("NOXA_NATIVE_CAPTURE") == "" {
		t.Skip("set NOXA_NATIVE_CAPTURE=1 on an interactive Windows desktop")
	}
	if !Supported() {
		t.Fatal("Windows Graphics Capture or Media Foundation reported unsupported on a capture-capable desktop")
	}
}

// The software H.264 MFT, the last step of the encoder chain: with every
// hardware encoder rejected (as after multi-slice keyframes) a real session
// opens it, encodes the NV12 ring textures and writes single-slice
// keyframes the sender admits.
func TestSoftwareEncoderIsTheLastResort(t *testing.T) {
	if os.Getenv("NOXA_NATIVE_CAPTURE") == "" {
		t.Skip("set NOXA_NATIVE_CAPTURE=1 on an interactive Windows desktop")
	}
	var report []string
	var hardware []string
	func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		initThread()
		d, err := newDevice(0)
		if err != nil {
			t.Fatal(err)
		}
		defer d.close()
		if err := mfStartup(); err != nil {
			t.Fatal(err)
		}
		_, infos, free := enumEncoders(d, &report)
		defer free()
		order := rankEncoders(infos, func(encoderInfo) bool { return false })
		if len(order) == 0 || infos[order[len(order)-1]].hardware {
			t.Fatalf("no software encoder last: %v %v", infos, report)
		}
		for _, info := range infos {
			if info.hardware {
				hardware = append(hardware, info.name)
			}
		}
		t.Logf("adapter %s; chain %v; enumeration notes %v", d.adapter, infos, report)
	}()
	for _, name := range hardware {
		multiSliceEncoders.Store(name, true)
	}
	t.Cleanup(func() {
		for _, name := range hardware {
			multiSliceEncoders.Delete(name)
		}
	})
	var mu sync.Mutex
	var encoded, keyframes int
	var keyErr error
	s, err := Start(Config{Source: Source{Monitor: 0}, MaxWidth: 1280, MaxHeight: 720, FPS: 30, Bitrate: 2_000_000}, func(f Frame) {
		mu.Lock()
		defer mu.Unlock()
		encoded++
		if !f.Keyframe {
			return
		}
		keyframes++
		var sps, pps []byte
		for _, nal := range framecrypt.SplitAnnexB(f.SeqHeader) {
			switch nal[0] & 31 {
			case 7:
				sps = nal
			case 8:
				pps = nal
			}
		}
		if _, key, err := framecrypt.CanonicalH264(f.Data, sps, pps); err != nil || !key {
			keyErr = fmt.Errorf("keyframe rejected (key=%v): %w", key, err)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
	s.Close()
	info := s.Info()
	mu.Lock()
	defer mu.Unlock()
	t.Logf("encoder %q hardware=%v encoded=%d keyframes=%d", info.Name, info.Hardware, encoded, keyframes)
	if info.Hardware || encoded == 0 || keyframes == 0 || keyErr != nil {
		t.Fatalf("software encoder: hardware=%v encoded=%d keyframes=%d err=%v", info.Hardware, encoded, keyframes, keyErr)
	}
}

// MFTEnum2 really filters by adapter: the device's LUID lists its hardware
// MFTs, a LUID no adapter has lists none (an ignored attribute would list
// them all); the vendor fallback then decides instead.
func TestAdapterEnumerationFiltersByLUID(t *testing.T) {
	if os.Getenv("NOXA_NATIVE_CAPTURE") == "" {
		t.Skip("set NOXA_NATIVE_CAPTURE=1 on an interactive Windows desktop")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	initThread()
	d, err := newDevice(0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.close()
	if err := mfStartup(); err != nil {
		t.Fatal(err)
	}
	onAdapter := func(luid uint64) (n int, report []string) {
		probe := *d // only the adapter identity is read
		probe.luid = luid
		_, infos, free := enumEncoders(&probe, &report)
		defer free()
		for _, info := range infos {
			if info.onAdapter {
				n++
			}
		}
		return n, report
	}
	own, report := onAdapter(d.luid)
	foreign, foreignReport := onAdapter(0x7FFF_FFFF_0000_0001)
	t.Logf("own adapter: %d on-adapter MFTs %v; foreign LUID: %d %v", own, report, foreign, foreignReport)
	failed := func(report []string) bool {
		return slices.ContainsFunc(report, func(line string) bool { return strings.Contains(line, "(MFTEnum2): 0 encoders") })
	}
	if own == 0 || failed(report) || !failed(foreignReport) {
		t.Fatalf("MFTEnum2 adapter filter: own %d %v, foreign %d %v", own, report, foreign, foreignReport)
	}
}

// A hardware encoder found to write multi-slice keyframes mid-stream is
// replaced at once by the next candidate (here the software MFT), keeping
// the converter, ring and last picture: frames resume well inside the
// 10 s stall watchdog even from a static source.
func TestMultiSliceEncoderIsReplacedMidStream(t *testing.T) {
	if os.Getenv("NOXA_NATIVE_CAPTURE") == "" {
		t.Skip("set NOXA_NATIVE_CAPTURE=1 on an interactive Windows desktop")
	}
	var mu sync.Mutex
	var rejected []string
	keyframeSlices = func([]byte) int { return 2 } // every hardware keyframe "has two slices"
	t.Cleanup(func() {
		keyframeSlices = sliceCount
		mu.Lock()
		defer mu.Unlock()
		for _, name := range rejected {
			multiSliceEncoders.Delete(name)
		}
	})
	var software, hardware int
	var swapped time.Time
	var s *Session
	start := time.Now()
	sess, err := Start(Config{Source: Source{Monitor: 0}, MaxWidth: 1280, MaxHeight: 720, FPS: 30, Bitrate: 2_000_000}, func(f Frame) {
		mu.Lock()
		defer mu.Unlock()
		if s == nil {
			return
		}
		if info := s.Info(); info.Hardware {
			hardware++
		} else {
			if software == 0 {
				swapped = time.Now()
			}
			software++
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	s = sess
	mu.Unlock()
	time.Sleep(3 * time.Second)
	s.Close()
	multiSliceEncoders.Range(func(key, _ any) bool {
		mu.Lock()
		rejected = append(rejected, key.(string))
		mu.Unlock()
		return true
	})
	info := s.Info()
	mu.Lock()
	defer mu.Unlock()
	t.Logf("final encoder %q hardware=%v; frames: %d hardware (dropped by the check), %d software, first software after %v; rejected %v",
		info.Name, info.Hardware, hardware, software, swapped.Sub(start), rejected)
	if info.Hardware || software == 0 || len(rejected) == 0 || swapped.Sub(start) > 2*time.Second {
		t.Fatalf("no mid-stream swap: hardware=%v software frames=%d rejected=%v after %v", info.Hardware, software, rejected, swapped.Sub(start))
	}
}
