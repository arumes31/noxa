package main

import (
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"noxa/client/internal/nativecapture"
	"noxa/internal/nativesender"
	"noxa/internal/framecrypt"
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
