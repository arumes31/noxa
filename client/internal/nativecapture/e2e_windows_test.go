//go:build windows

package nativecapture

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"

	"noxa/internal/framecrypt"
	"noxa/internal/nativesender"
	"noxa/internal/nativesender/sendertest"
)

// TestNativeCaptureStreamsToReceiver runs real capture + Media Foundation
// encode into the native sender and an in-process pion receiver that
// depacketizes, authenticates and decrypts every frame. NOXA_NATIVE_CAPTURE=1
// enables it; NOXA_NATIVE_TESTDATA=<file> saves real encrypted access units
// for the server inspector test (internal/webrtc/testdata).
func TestNativeCaptureStreamsToReceiver(t *testing.T) {
	if os.Getenv("NOXA_NATIVE_CAPTURE") == "" {
		t.Skip("set NOXA_NATIVE_CAPTURE=1 on an interactive Windows desktop")
	}
	server := sendertest.NewReceiver(t)
	material := make([]byte, 32)
	_, _ = rand.Read(material)
	keys := framecrypt.NewEncryptor()
	if err := keys.SetKey(1, 0, material); err != nil {
		t.Fatal(err)
	}
	if err := keys.Activate(1); err != nil {
		t.Fatal(err)
	}
	var session *Session
	var sessionMu sync.Mutex
	sender, err := nativesender.New(nativesender.Config{Signaling: server, Keys: keys, SettingEngine: sendertest.LoopbackSettings(), OnKeyframeRequest: func() {
		sessionMu.Lock()
		defer sessionMu.Unlock()
		if session != nil {
			session.RequestKeyframe()
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sender.Close() }()
	server.SetSender(sender)
	deadline := time.Now().Add(10 * time.Second)
	for sender.ConnectionState() != webrtc.PeerConnectionStateConnected {
		if time.Now().After(deadline) {
			t.Fatal("native peer did not connect")
		}
		time.Sleep(20 * time.Millisecond)
	}
	monitor := 0
	for i, m := range Monitors() {
		if m.Primary {
			monitor = i
		}
	}
	maxW, maxH := 1920, 1080
	if os.Getenv("NOXA_NATIVE_TESTDATA") != "" {
		maxW, maxH = 640, 360 // Small committed fixtures.
	}
	s, err := Start(Config{Source: Source{Monitor: monitor}, MaxWidth: maxW, MaxHeight: maxH, FPS: 30, Bitrate: 1_000_000,
		AllowSoftware: os.Getenv("NOXA_NATIVE_SOFTWARE") != ""}, func(f Frame) {
		if err := sender.WriteFrame(f.Data, f.Keyframe, f.SeqHeader, f.Captured); err != nil {
			t.Errorf("WriteFrame: %v", err)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	sessionMu.Lock()
	session = s
	sessionMu.Unlock()
	time.Sleep(3 * time.Second)
	s.Close()
	var saved []string
	received, keyframes := 0, 0
	for done := false; !done; {
		select {
		case frame := <-server.Frames:
			plain := sendertest.Decrypt(t, material, frame)
			nals := framecrypt.SplitAnnexB(plain)
			key := nals[len(nals)-1][0]&31 == 5
			if key && (len(nals) != 3 || nals[0][0]&31 != 7 || nals[1][0]&31 != 8) || !key && len(nals) != 1 {
				t.Fatalf("frame %d is not a canonical access unit: %d NALs", received, len(nals))
			}
			if !bytes.HasPrefix(frame, []byte{0, 0, 0, 1}) {
				t.Fatal("receiver frame lacks a 4-byte start code")
			}
			if key {
				keyframes++
			}
			if len(saved) < 8 {
				saved = append(saved, hex.EncodeToString(frame))
			}
			received++
		case <-time.After(time.Second):
			done = true
		}
	}
	st := sender.Stats()
	t.Logf("encoder=%q received=%d keyframes=%d sent=%d dropped=%d packets=%d bytes=%d", s.Info().Name, received, keyframes, st.FramesSent, st.FramesDropped, st.PacketsSent, st.BytesSent)
	if received < 10 || keyframes < 1 || uint64(received) != st.FramesSent {
		t.Fatalf("received %d of %d frames, %d keyframes", received, st.FramesSent, keyframes)
	}
	if path := os.Getenv("NOXA_NATIVE_TESTDATA"); path != "" {
		data, _ := json.MarshalIndent(map[string]any{"encoder": s.Info().Name, "frames": saved}, "", "  ")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// TestNativeAudioStreamsToReceiver captures a test tone with WASAPI process
// loopback, encodes it with the bundled libopus, encrypts and sends it on
// the native peer's screenaudio track; the in-process receiver authenticates,
// decrypts and decodes it back to the tone. NOXA_NATIVE_CAPTURE=1 enables it.
func TestNativeAudioStreamsToReceiver(t *testing.T) {
	if os.Getenv("NOXA_NATIVE_CAPTURE") == "" {
		t.Skip("set NOXA_NATIVE_CAPTURE=1 on an interactive Windows desktop with audio output")
	}
	requireOpus(t)
	requireLoopback(t)
	cmd := playTone(t, writeToneWAV(t, 30))
	time.Sleep(1500 * time.Millisecond)
	server := sendertest.NewReceiver(t)
	material := make([]byte, 32)
	_, _ = rand.Read(material)
	video, audio := framecrypt.NewEncryptor(), framecrypt.NewEncryptor()
	if err := audio.SetKey(1, 2, material); err != nil {
		t.Fatal(err)
	}
	if err := audio.Activate(1); err != nil {
		t.Fatal(err)
	}
	sender, err := nativesender.New(nativesender.Config{Signaling: server, Keys: video, AudioKeys: audio, SettingEngine: sendertest.LoopbackSettings()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sender.Close() }()
	server.SetSender(sender)
	deadline := time.Now().Add(10 * time.Second)
	for sender.ConnectionState() != webrtc.PeerConnectionStateConnected {
		if time.Now().After(deadline) {
			t.Fatal("native peer did not connect")
		}
		time.Sleep(20 * time.Millisecond)
	}
	s, err := startLoopback(AudioApplication, uint32(cmd.Process.Pid), processLoopbackInclude, func(p AudioPacket) { // #nosec G115
		if err := sender.WriteAudio(p.Data, p.Captured); err != nil {
			t.Errorf("WriteAudio: %v", err)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * time.Second)
	s.Close()
	dec := newTestDecoder(t)
	var energy float64
	received := 0
	var lastTS uint32
	for done := false; !done; {
		select {
		case p := <-server.Audio:
			pcm := dec.decode(t, sendertest.DecryptOpus(t, material, p.Payload))
			if received > 0 && p.Timestamp-lastTS != AudioFrame {
				t.Logf("timestamp step %d at packet %d", p.Timestamp-lastTS, received)
			}
			lastTS = p.Timestamp
			if received >= 25 {
				energy += rms(pcm)
			}
			received++
		case <-time.After(time.Second):
			done = true
		}
	}
	st := sender.Stats()
	avg := energy / float64(max(received-25, 1))
	t.Logf("audio received=%d sent=%d dropped=%d decoded rms %.0f", received, st.AudioFramesSent, st.AudioFramesDropped, avg)
	if received < 100 || uint64(received) != st.AudioFramesSent || avg < 1000 {
		t.Fatalf("received %d of %d packets, rms %.0f", received, st.AudioFramesSent, avg)
	}
}
