package nativesender

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4"

	"noxa/internal/framecrypt"
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

// fakeServer is an in-process SFU stand-in: it answers the native offer,
// exchanges ICE and collects depacketized access units.
type fakeServer struct {
	t      *testing.T
	pc     *webrtc.PeerConnection
	sender *Sender
	mu     sync.Mutex
	offers []netproto.WebRTCOffer
	frames chan []byte
	ssrc   chan uint32
}

func (f *fakeServer) Offer(offer netproto.WebRTCOffer) (netproto.WebRTCAnswer, error) {
	f.mu.Lock()
	f.offers = append(f.offers, offer)
	f.mu.Unlock()
	if err := f.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer.SDP}); err != nil {
		return netproto.WebRTCAnswer{}, err
	}
	answer, err := f.pc.CreateAnswer(nil)
	if err != nil {
		return netproto.WebRTCAnswer{}, err
	}
	if err := f.pc.SetLocalDescription(answer); err != nil {
		return netproto.WebRTCAnswer{}, err
	}
	return netproto.WebRTCAnswer{SDP: answer.SDP, Peer: netproto.PeerNative}, nil
}

func (f *fakeServer) Candidate(c netproto.ICECandidate) error {
	if c.Peer != netproto.PeerNative {
		return errors.New("candidate without native peer tag")
	}
	mid, index := c.SDPMid, c.SDPMLineIndex
	return f.pc.AddICECandidate(webrtc.ICECandidateInit{Candidate: c.Candidate, SDPMid: &mid, SDPMLineIndex: &index})
}

func loopbackSettings() *webrtc.SettingEngine {
	s := &webrtc.SettingEngine{}
	s.SetIncludeLoopbackCandidate(true)
	s.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	return s
}

func newFakeServer(t *testing.T) *fakeServer {
	media := &webrtc.MediaEngine{}
	if err := media.RegisterDefaultCodecs(); err != nil {
		t.Fatal(err)
	}
	api := webrtc.NewAPI(webrtc.WithMediaEngine(media), webrtc.WithSettingEngine(*loopbackSettings()))
	pc, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeServer{t: t, pc: pc, frames: make(chan []byte, 256), ssrc: make(chan uint32, 1)}
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		f.mu.Lock()
		s := f.sender
		f.mu.Unlock()
		if s == nil {
			return
		}
		init := c.ToJSON()
		_ = s.AddCandidate(netproto.ICECandidate{Candidate: init.Candidate, SDPMid: *init.SDPMid, SDPMLineIndex: *init.SDPMLineIndex, Peer: netproto.PeerNative})
	})
	pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		f.ssrc <- uint32(track.SSRC())
		depacketizer := &codecs.H264Packet{}
		var frame []byte
		for {
			packet, _, err := track.ReadRTP()
			if err != nil {
				return
			}
			data, err := depacketizer.Unmarshal(packet.Payload)
			if err != nil {
				t.Errorf("depacketize: %v", err)
				return
			}
			frame = append(frame, data...)
			if packet.Marker {
				f.frames <- frame
				frame = nil
			}
		}
	})
	t.Cleanup(func() { _ = pc.Close() })
	return f
}

// decrypt is the receiver half of the frame cipher (LiveKit decodeFunction).
func decrypt(t *testing.T, material, frame []byte) []byte {
	t.Helper()
	clear := -1
	for i := 0; i+4 < len(frame) && clear < 0; i++ {
		if frame[i] == 0 && frame[i+1] == 0 && frame[i+2] == 0 && frame[i+3] == 1 {
			if nal := frame[i+4] & 31; nal == 1 || nal == 5 {
				clear = i + 6
			}
		}
	}
	if clear < 0 {
		t.Fatal("no slice NAL")
	}
	header, tail := frame[:clear], frame[clear:]
	var raw []byte // undo emulation prevention of the encrypted tail
	for i := 0; i < len(tail); i++ {
		if i >= 2 && tail[i] == 3 && tail[i-1] == 0 && tail[i-2] == 0 && i+1 < len(tail) && tail[i+1] <= 3 {
			continue
		}
		raw = append(raw, tail[i])
	}
	if len(raw) < 30 || raw[len(raw)-2] != 12 {
		t.Fatalf("no encrypted trailer: % x", raw[max(0, len(raw)-16):])
	}
	iv := raw[len(raw)-14 : len(raw)-2]
	key, err := hkdf.Key(sha256.New, material, []byte("LKFrameEncryptionKey"), string(make([]byte, 128)), 16)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := aes.NewCipher(key)
	aead, _ := cipher.NewGCM(block)
	plain, err := aead.Open(nil, iv, raw[:len(raw)-14], header)
	if err != nil {
		t.Fatalf("authentication failed: %v", err)
	}
	return append(append([]byte{}, header...), plain...)
}

func TestSenderStreamsEncryptedFramesToReceiver(t *testing.T) {
	server := newFakeServer(t)
	material := make([]byte, 32)
	_, _ = rand.Read(material)
	keys := framecrypt.NewEncryptor()
	var keyframeRequests sync.WaitGroup
	keyframeRequests.Add(1)
	var once sync.Once
	sender, err := New(Config{Signaling: server, Keys: keys, SettingEngine: loopbackSettings(),
		OnKeyframeRequest: func() { once.Do(keyframeRequests.Done) }})
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()
	server.mu.Lock()
	server.sender = sender
	offer := server.offers[0]
	server.mu.Unlock()
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
	case ssrc = <-server.ssrc:
	case <-time.After(5 * time.Second):
		t.Fatal("no track at receiver")
	}
	if ssrc != sender.Stats().SSRC {
		t.Fatalf("ssrc %d != %d", ssrc, sender.Stats().SSRC)
	}
	for i := range want {
		select {
		case frame := <-server.frames:
			got := decrypt(t, material, frame)
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
	if err := server.pc.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: ssrc}}); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { keyframeRequests.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("PLI did not request a keyframe")
	}
	if st := sender.Stats(); st.FramesSent != 30 || st.KeyframesSent != 3 || st.FramesDropped != 2 {
		t.Fatalf("stats: %+v", st)
	}
}
