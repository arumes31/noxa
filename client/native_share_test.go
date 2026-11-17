package main

import (
	"encoding/base64"
	"errors"
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
