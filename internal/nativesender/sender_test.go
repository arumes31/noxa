package nativesender

import (
	"bytes"
	"crypto/rand"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"

	"noxa/internal/framecrypt"
	"noxa/internal/nativesender/sendertest"
	"noxa/internal/netproto"
)

// Baseline SPS/PPS from the webview hardware probe (hardware-probe-frame.js).
var (
	testSPS = []byte{0x67, 0x42, 0xc0, 0x1f, 0x0f, 0xd9, 0x1f, 0x88, 0x88, 0x84, 0, 0, 3, 0, 4, 0, 0, 3, 0, 0xc8, 0x3c, 0x60, 0xc9, 0x20}
	testPPS = []byte{0x68, 0x87, 0xcb, 0x83, 0xcb, 0x20}
)

// encoderAU builds an access unit the way Media Foundation writes one: AUD,
// 3-byte start codes, SEI, in-band SPS/PPS on keyframes.
func encoderAU(key bool, payload []byte) []byte {
	au := []byte{0, 0, 0, 1, 0x09, 0x10, 0, 0, 1, 0x06, 0x05, 0x01, 0x80}
	slice := []byte{0x41, 0x9a}
	if key {
		au = append(au, 0, 0, 0, 1)
		au = append(au, testSPS...)
		au = append(au, 0, 0, 1)
		au = append(au, testPPS...)
		slice = []byte{0x65, 0x88}
	}
	au = append(au, 0, 0, 1)
	au = append(au, slice...)
	return append(au, payload...)
}

func TestSenderStreamsEncryptedFramesToReceiver(t *testing.T) {
	server := sendertest.NewReceiver(t)
	material := make([]byte, 32)
	_, _ = rand.Read(material)
	keys := framecrypt.NewEncryptor()
	var keyframeRequests sync.WaitGroup
	keyframeRequests.Add(1)
	var once sync.Once
	sender, err := New(Config{Signaling: server, Keys: keys, SettingEngine: sendertest.LoopbackSettings(),
		OnKeyframeRequest: func() { once.Do(keyframeRequests.Done) }})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sender.Close() }()
	server.SetSender(sender)
	offer := server.Offer0()
	if offer.Peer != netproto.PeerNative || offer.MediaE2EE != netproto.MediaE2EEVersion || len(offer.Tracks) != 1 || offer.Tracks[0] != (netproto.TrackSlot{TrackID: TrackID, Slot: "screen"}) {
		t.Fatalf("offer metadata: %+v", offer)
	}
	connected := make(chan struct{})
	var connectedOnce sync.Once
	sender.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		if s == webrtc.PeerConnectionStateConnected {
			connectedOnce.Do(func() { close(connected) })
		}
	})
	if sender.ConnectionState() == webrtc.PeerConnectionStateConnected {
		connectedOnce.Do(func() { close(connected) })
	}
	select {
	case <-connected:
	case <-time.After(10 * time.Second):
		t.Fatal("native peer did not connect")
	}
	start := time.Now()
	// Fail closed: without an active key nothing is sent.
	if err := sender.WriteFrame(encoderAU(true, []byte{1, 2, 3}), true, nil, start); !errors.Is(err, ErrDropped) || !errors.Is(err, framecrypt.ErrNoKey) {
		t.Fatalf("keyless frame: %v", err)
	}
	if err := keys.SetKey(1, 3, material); err != nil {
		t.Fatal(err)
	}
	if err := keys.Activate(1); err != nil {
		t.Fatal(err)
	}
	// After a key gap a delta frame is withheld until a keyframe.
	if err := sender.WriteFrame(encoderAU(false, []byte{9}), false, nil, start.Add(time.Millisecond)); !errors.Is(err, ErrDropped) {
		t.Fatalf("delta after key gap: %v", err)
	}
	var want [][]byte
	for i := range 30 {
		key := i%10 == 0
		payload := make([]byte, 100+i*300) // large frames cross FU-A fragmentation
		_, _ = rand.Read(payload)
		au := encoderAU(key, payload)
		canonical, _, err := framecrypt.CanonicalH264(au, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, canonical)
		if err := sender.WriteFrame(au, key, nil, start.Add(time.Duration(i+2)*16*time.Millisecond)); err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
	}
	var ssrc uint32
	select {
	case ssrc = <-server.SSRC:
	case <-time.After(5 * time.Second):
		t.Fatal("no track at receiver")
	}
	if ssrc != sender.Stats().SSRC {
		t.Fatalf("ssrc %d != %d", ssrc, sender.Stats().SSRC)
	}
	for i := range want {
		select {
		case frame := <-server.Frames:
			got := sendertest.Decrypt(t, material, frame)
			if !bytes.Equal(got, want[i]) {
				t.Fatalf("frame %d differs after decrypt (len %d vs %d)", i, len(got), len(want[i]))
			}
			if frame[len(frame)-1] != 3 {
				t.Fatalf("frame %d key index %d", i, frame[len(frame)-1])
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("frame %d not received", i)
		}
	}
	// PLI from the SFU reaches the encoder.
	if err := server.PC.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: ssrc}}); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { keyframeRequests.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("PLI did not request a keyframe")
	}
	if st := sender.Stats(); st.FramesSent != 30 || st.KeyframesSent != 3 || st.FramesDropped != 2 || st.PacketsSent < 30 || st.BytesSent == 0 {
		t.Fatalf("stats: %+v", st)
	}
}
