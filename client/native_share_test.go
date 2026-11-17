package main

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"noxa/client/internal/nativecapture"
	"noxa/internal/framecrypt"
	"noxa/internal/nativesender"
	"noxa/internal/netproto"
)

func TestNativeShareFallbackDecisions(t *testing.T) {
	if got := nativeShareDecision(nativecapture.ErrSourceClosed, 0); got != "ended" {
		t.Fatalf("closed window: %s", got)
	}
	failure := errors.New("device removed")
	if got := nativeShareDecision(failure, 0); got != "restart" {
		t.Fatalf("first failure: %s", got)
	}
	if got := nativeShareDecision(failure, 1); got != "fallback" {
		t.Fatalf("second failure: %s", got)
	}
	for codec, want := range map[string]bool{"": false, "h264": false, "vp8": true, "h265": true} {
		if nativeShareCodecFallback(codec) != want {
			t.Fatalf("codec %q fallback != %v", codec, want)
		}
	}
}

func TestNativeKeyOpsMirrorTheEngine(t *testing.T) {
	keys := framecrypt.NewEncryptor()
	material := base64.StdEncoding.EncodeToString(make([]byte, 32))
	if err := applyNativeKeyOp(keys, NativeKeyOp{Op: "key", Epoch: 2, Kid: 1, Key: material}); err != nil {
		t.Fatal(err)
	}
	if _, ok := keys.Active(); ok {
		t.Fatal("key active before activate")
	}
	if err := applyNativeKeyOp(keys, NativeKeyOp{Op: "activate", Epoch: 2}); err != nil {
		t.Fatal(err)
	}
	if epoch, ok := keys.Active(); !ok || epoch != 2 {
		t.Fatalf("active = %d %v", epoch, ok)
	}
	if err := applyNativeKeyOp(keys, NativeKeyOp{Op: "retire", Epoch: 2}); err != nil {
		t.Fatal(err)
	}
	if _, ok := keys.Active(); ok {
		t.Fatal("retired epoch still active")
	}
	if applyNativeKeyOp(keys, NativeKeyOp{Op: "key", Epoch: 3, Key: "short"}) == nil || applyNativeKeyOp(keys, NativeKeyOp{Op: "bogus"}) == nil {
		t.Fatal("invalid operations accepted")
	}
}

func TestNativeDiagnosticsRowIsValid(t *testing.T) {
	start := time.Unix(100, 0)
	prev := nativeSample{at: start}
	cur := nativeSample{at: start.Add(time.Second), captured: 60, encoded: 59, sent: 58, keyframes: 1, bytes: 1_000_000, retransmitBytes: 10_000, encodeTime: 590 * time.Millisecond}
	row := nativeDiagnosticsRow(prev, cur, nativecapture.Stats{Encoded: 59, Keyframes: 1, Width: 1920, Height: 1080},
		nativesender.Stats{SSRC: 7, FramesSent: 58, PacketsSent: 900, BytesSent: 1_000_000, RetransmittedPackets: 9, RetransmittedBytes: 10_000, EstimateBPS: 9_000_000},
		nativecapture.EncoderInfo{Vendor: "nvidia", Hardware: true}, nativesender.Level{MaxWidth: 1920, MaxHeight: 1080, FPS: 60, Bitrate: 8_000_000}, 42)
	if !row.Valid() {
		t.Fatalf("invalid row: %+v", row)
	}
	if row.CaptureBackend != "native" || row.EncoderImplementation != "MediaFoundationVideoEncoder" || !*row.PowerEfficient ||
		*row.CaptureFPS != 60 || *row.EncodedFPS != 59 || *row.SentFPS != 58 || *row.BitrateBPS != 8_000_000 || *row.EncodeMS != 10 || *row.KeyFramesDelta != 1 {
		t.Fatalf("row values: %+v", row)
	}
	// A reset (counters went backwards) yields no rates, never negative ones.
	if r := nativeDiagnosticsRow(cur, prev, nativecapture.Stats{}, nativesender.Stats{SSRC: 7}, nativecapture.EncoderInfo{}, nativesender.Level{FPS: 30}, 42); r.EncodedFPS != nil || !r.Valid() {
		t.Fatalf("reset row: %+v", r)
	}
}

func TestNativeShareGenerationUpdates(t *testing.T) {
	n := &nativeShare{stop: make(chan struct{})}
	if row := n.diagnostics(time.Now()); row != nil {
		t.Fatal("diagnostics without a session/generation")
	}
	if err := n.applyUpdate(NativeShareUpdate{Generation: 9007199254740993, UploadActive: true, Codec: "h264"}); err != nil {
		t.Fatal(err)
	}
	if n.generation != 9007199254740993 || !n.upload {
		t.Fatalf("update not applied: gen=%d upload=%v", n.generation, n.upload)
	}
	// A late update of an older publication never wins.
	if err := n.applyUpdate(NativeShareUpdate{Generation: 5, UploadActive: false}); err == nil || n.generation != 9007199254740993 || !n.upload {
		t.Fatalf("stale update applied: gen=%d upload=%v err=%v", n.generation, n.upload, err)
	}
	// Demand withdrawn for the same generation pauses upload.
	if err := n.applyUpdate(NativeShareUpdate{Generation: 9007199254740993, UploadActive: false, Codec: "h264"}); err != nil || n.upload {
		t.Fatalf("pause not applied: upload=%v err=%v", n.upload, err)
	}
}

func TestNativeShareStaleCodecNeverFallsBack(t *testing.T) {
	n := &nativeShare{stop: make(chan struct{}), generation: 20}
	// n.cm is nil: a fallback would panic. A late generation-19 VP8 update
	// must be rejected as stale before the codec can end generation 20.
	if err := n.applyUpdate(NativeShareUpdate{Generation: 19, Codec: "vp8"}); err == nil {
		t.Fatal("stale codec update accepted")
	}
	if n.stoppedLocked() {
		t.Fatal("stale codec update stopped the share")
	}
}

func TestNativeBitrateCapFollowsServerLimit(t *testing.T) {
	for _, c := range []struct{ requested, limit, want int }{
		{8_000_000, 0, 8_000_000},
		{8_000_000, 1_000_000, 850_000},
		{2_000_000, 10_000_000, 2_000_000},
		{100_000, 0, 500_000},
	} {
		if got := nativeBitrateCap(c.requested, netproto.MediaLimits{VideoMaxBitrate: c.limit}); got != c.want {
			t.Errorf("nativeBitrateCap(%d, %d) = %d, want %d", c.requested, c.limit, got, c.want)
		}
	}
	n := &nativeShare{stop: make(chan struct{}), req: NativeShareRequest{Bitrate: 8_000_000}}
	n.ladder = nativesender.NewLadder(nativesender.Level{MaxWidth: 1920, MaxHeight: 1080, FPS: 60, Bitrate: 8_000_000}, 8_000_000)
	n.level = n.ladder.Current()
	n.applyLimits(netproto.MediaLimits{VideoMaxBitrate: 2_000_000})
	if n.ladder.Top.Bitrate != 1_700_000 || n.level.Bitrate != 1_700_000 {
		t.Fatalf("limit not applied: top=%d level=%d", n.ladder.Top.Bitrate, n.level.Bitrate)
	}
}

func TestStopNativeShareIgnoresAnOlderShare(t *testing.T) {
	cm := &connManager{}
	current := &nativeShare{cm: cm, stop: make(chan struct{}), req: NativeShareRequest{ID: "2"}}
	cm.nativeShare = current
	app := &App{activeID: "a", tabs: map[string]*tabState{"a": {cm: cm}}}
	app.cmStore(cm)
	if err := app.StopNativeScreenShareForTab("a", "1"); err != nil {
		t.Fatal(err)
	}
	if cm.nativeShare != current || current.stoppedLocked() {
		t.Fatal("a stale stop ended the replacement share")
	}
	if err := app.UpdateNativeScreenShareForTab("a", NativeShareUpdate{ID: "1", Generation: 1, UploadActive: true}); err == nil || current.upload {
		t.Fatal("a stale update reached the replacement share")
	}
	if err := app.StopNativeScreenShareForTab("a", "2"); err != nil {
		t.Fatal(err)
	}
	if cm.nativeShare != nil || !current.stoppedLocked() {
		t.Fatal("the current share was not stopped")
	}
}

func TestDetachClosesEveryDetachedNativeShare(t *testing.T) {
	m := &connManager{}
	a := &nativeShare{cm: m, stop: make(chan struct{})}
	b := &nativeShare{cm: m, stop: make(chan struct{})}
	m.nativeShare = a
	m.mu.Lock()
	m.detachLocked()
	m.nativeShare = b // a second detach before the first finishDetach
	m.detachLocked()
	m.mu.Unlock()
	m.finishDetach(nil, nil, nil, false)
	if !a.stoppedLocked() || !b.stoppedLocked() || m.nativeShare != nil {
		t.Fatal("a detached native share was not closed")
	}
}

func TestNativeReconnectBudgetResetsOnConnect(t *testing.T) {
	current, stale := &nativesender.Sender{}, &nativesender.Sender{}
	n := &nativeShare{stop: make(chan struct{}), sender: current, reconnects: 2}
	n.peerConnected(stale)
	if n.reconnects != 2 {
		t.Fatal("a replaced peer reset the reconnect budget")
	}
	n.peerConnected(current)
	if n.reconnects != 0 {
		t.Fatalf("reconnects = %d after connect, want 0", n.reconnects)
	}
}

// TestNativeShareAudioFallbacks: application audio falls back to system
// audio (with a notice), a missing libopus leaves the share silent without
// trying other sources, and "none" never opens a loopback.
func TestNativeShareAudioFallbacks(t *testing.T) {
	appFails := errors.New("activation failed")
	cases := []struct {
		name, audio string
		window      uint64
		fail        map[nativecapture.AudioMode]error
		wantTried   []nativecapture.AudioMode
		wantNotice  string
		wantSession bool
	}{
		{"none", "none", 7, nil, nil, "", false},
		{"application", "application", 7, nil, []nativecapture.AudioMode{"application"}, "", true},
		{"application falls back to system", "application", 7, map[nativecapture.AudioMode]error{"application": appFails},
			[]nativecapture.AudioMode{"application", "system"}, "system", true},
		{"monitor application means system", "application", 0, nil, []nativecapture.AudioMode{"system"}, "system", true},
		{"system", "system", 0, nil, []nativecapture.AudioMode{"system"}, "", true},
		{"old windows", "application", 7, map[nativecapture.AudioMode]error{"application": nativecapture.ErrAudioUnsupported, "system": nativecapture.ErrAudioUnsupported},
			[]nativecapture.AudioMode{"application", "system"}, "none", false},
		{"no libopus", "application", 7, map[nativecapture.AudioMode]error{"application": nativecapture.ErrOpusUnavailable},
			[]nativecapture.AudioMode{"application"}, "none", false},
	}
	defer func(orig func(nativecapture.AudioMode, uint64, func(nativecapture.AudioPacket)) (*nativecapture.AudioSession, error)) {
		startNativeAudio = orig
	}(startNativeAudio)
	for _, c := range cases {
		var tried []nativecapture.AudioMode
		startNativeAudio = func(mode nativecapture.AudioMode, window uint64, _ func(nativecapture.AudioPacket)) (*nativecapture.AudioSession, error) {
			tried = append(tried, mode)
			if window != c.window {
				t.Errorf("%s: window %d", c.name, window)
			}
			if err := c.fail[mode]; err != nil {
				return nil, err
			}
			return &nativecapture.AudioSession{Mode: mode}, nil
		}
		n := &nativeShare{req: NativeShareRequest{Audio: c.audio, Source: nativecapture.Source{Window: c.window}}}
		notice := n.openAudio()
		if notice != c.wantNotice || (n.audio != nil) != c.wantSession || len(tried) != len(c.wantTried) {
			t.Errorf("%s: notice %q session %v tried %v", c.name, notice, n.audio != nil, tried)
			continue
		}
		for i := range tried {
			if tried[i] != c.wantTried[i] {
				t.Errorf("%s: tried %v, want %v", c.name, tried, c.wantTried)
			}
		}
	}
}

func TestNativeKeySlotsAreSeparate(t *testing.T) {
	cm := &connManager{}
	if cm.nativeKeys("") != cm.nativeKeys("screen") || cm.nativeKeys("screen") == cm.nativeKeys("screenaudio") {
		t.Fatal("screen and screenaudio keys must be separate keyrings")
	}
	app := &App{activeID: "a", tabs: map[string]*tabState{"a": {cm: cm}}}
	app.cmStore(cm)
	material := base64.StdEncoding.EncodeToString(make([]byte, 32))
	if err := app.NativeScreenKeyForTab("a", NativeKeyOp{Slot: "screenaudio", Op: "key", Epoch: 1, Key: material}); err != nil {
		t.Fatal(err)
	}
	if err := app.NativeScreenKeyForTab("a", NativeKeyOp{Slot: "screenaudio", Op: "activate", Epoch: 1}); err != nil {
		t.Fatal(err)
	}
	if _, ok := cm.nativeKeys("screenaudio").Active(); !ok {
		t.Fatal("screenaudio key not active")
	}
	if _, ok := cm.nativeKeys("screen").Active(); ok {
		t.Fatal("a screenaudio key reached the screen keyring")
	}
	if app.NativeScreenKeyForTab("a", NativeKeyOp{Slot: "mic", Op: "forget"}) == nil {
		t.Fatal("a mic key operation was accepted")
	}
}

func TestNativePickerSource(t *testing.T) {
	for value, want := range map[string]nativecapture.Source{"m:0": {Monitor: 0}, "m:2": {Monitor: 2}, "w:123456": {Window: 123456}} {
		if got, ok := nativePickerSource(value); !ok || got != want {
			t.Errorf("%q -> %+v %v", value, got, ok)
		}
	}
	for _, value := range []string{"", "m", "w:0", "w:-1", "m:x", "x:1", "m:99", "w:1:2"} {
		if _, ok := nativePickerSource(value); ok {
			t.Errorf("%q accepted", value)
		}
	}
	if got := (&App{}).NativeScreenThumbnails([]string{"bogus"}); len(got) != 0 {
		t.Fatalf("thumbnails for an invalid value: %v", got)
	}
}

// TestNativeShareAudioFailureEndsAudio: a capture that broke for good drops
// the share's audio and tells the webview (notice "none"); a failure of an
// already replaced session does nothing.
func TestNativeShareAudioFailureEndsAudio(t *testing.T) {
	events := &eventRecorder{}
	session := &nativecapture.AudioSession{Mode: nativecapture.AudioSystem}
	n := &nativeShare{cm: &connManager{sink: events, tabID: "a"}, stop: make(chan struct{}), audio: session, req: NativeShareRequest{ID: "s1"}}
	failed := make(chan error, 1)
	failed <- errors.New("audio service restarted")
	n.watchAudio(session, failed)
	if n.audio != nil {
		t.Fatal("a failed audio session stayed installed")
	}
	payload := events.waitFor(t, nativeShareEvent, nil, time.Second, "audio notice")
	if !strings.Contains(payload, "audio") || !strings.Contains(payload, "none") {
		t.Fatalf("audio notice payload %s", payload)
	}
	other := &nativecapture.AudioSession{}
	n.audio = other
	failed <- errors.New("stale")
	n.watchAudio(session, failed)
	if n.audio != other {
		t.Fatal("a stale failure removed the current audio session")
	}
}
