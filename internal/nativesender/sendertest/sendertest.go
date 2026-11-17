// Package sendertest is an in-process receiver for the native sender: it
// answers the native offer, exchanges ICE, depacketizes access units and
// decrypts them like a webview receiver (LiveKit decodeFunction).
package sendertest

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/sha256"
	"errors"
	"sync"
	"testing"

	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4"

	"noxa/internal/netproto"
)

// Receiver is an in-process SFU stand-in: it answers the native offer,
// exchanges ICE and collects depacketized access units.
type Receiver struct {
	t      testing.TB
	PC     *webrtc.PeerConnection
	Sender interface {
		AddCandidate(netproto.ICECandidate) error
	}
	mu     sync.Mutex
	Offers []netproto.WebRTCOffer
	Frames chan []byte
	SSRC   chan uint32
	// Audio receives the screenaudio track's RTP packets (encrypted Opus).
	Audio chan *rtp.Packet
}

func (f *Receiver) Offer(offer netproto.WebRTCOffer) (netproto.WebRTCAnswer, error) {
	f.mu.Lock()
	f.Offers = append(f.Offers, offer)
	f.mu.Unlock()
	if err := f.PC.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer.SDP}); err != nil {
		return netproto.WebRTCAnswer{}, err
	}
	answer, err := f.PC.CreateAnswer(nil)
	if err != nil {
		return netproto.WebRTCAnswer{}, err
	}
	if err := f.PC.SetLocalDescription(answer); err != nil {
		return netproto.WebRTCAnswer{}, err
	}
	return netproto.WebRTCAnswer{SDP: answer.SDP, Peer: netproto.PeerNative}, nil
}

func (f *Receiver) Candidate(c netproto.ICECandidate) error {
	if c.Peer != netproto.PeerNative {
		return errors.New("candidate without native peer tag")
	}
	mid, index := c.SDPMid, c.SDPMLineIndex
	return f.PC.AddICECandidate(webrtc.ICECandidateInit{Candidate: c.Candidate, SDPMid: &mid, SDPMLineIndex: &index})
}

func LoopbackSettings() *webrtc.SettingEngine {
	s := &webrtc.SettingEngine{}
	s.SetIncludeLoopbackCandidate(true)
	s.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	return s
}

func NewReceiver(t testing.TB) *Receiver {
	media := &webrtc.MediaEngine{}
	if err := media.RegisterDefaultCodecs(); err != nil {
		t.Fatal(err)
	}
	api := webrtc.NewAPI(webrtc.WithMediaEngine(media), webrtc.WithSettingEngine(*LoopbackSettings()))
	pc, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	f := &Receiver{t: t, PC: pc, Frames: make(chan []byte, 256), SSRC: make(chan uint32, 1), Audio: make(chan *rtp.Packet, 1024)}
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		f.mu.Lock()
		s := f.Sender
		f.mu.Unlock()
		if s == nil {
			return
		}
		init := c.ToJSON()
		_ = s.AddCandidate(netproto.ICECandidate{Candidate: init.Candidate, SDPMid: *init.SDPMid, SDPMLineIndex: *init.SDPMLineIndex, Peer: netproto.PeerNative})
	})
	pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		if track.Kind() == webrtc.RTPCodecTypeAudio {
			for {
				packet, _, err := track.ReadRTP()
				if err != nil {
					return
				}
				select {
				case f.Audio <- packet:
				default:
				}
			}
		}
		f.SSRC <- uint32(track.SSRC())
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
				f.Frames <- frame
				frame = nil
			}
		}
	})
	t.Cleanup(func() { _ = pc.Close() })
	return f
}

// decrypt is the receiver half of the frame cipher (LiveKit decodeFunction).
func Decrypt(t testing.TB, material, frame []byte) []byte {
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

// SetSender connects the receiver's ICE candidates to the sender.
func (f *Receiver) SetSender(s interface {
	AddCandidate(netproto.ICECandidate) error
}) {
	f.mu.Lock()
	f.Sender = s
	f.mu.Unlock()
}

// Offer0 returns the first native offer received.
func (f *Receiver) Offer0() netproto.WebRTCOffer {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Offers[0]
}

// DecryptOpus is the receiver half for audio (LiveKit decodeFunction): one
// clear TOC byte, no emulation prevention.
func DecryptOpus(t testing.TB, material, frame []byte) []byte {
	t.Helper()
	if len(frame) < 31 || frame[len(frame)-2] != 12 {
		t.Fatalf("no encrypted audio trailer: % x", frame)
	}
	iv := frame[len(frame)-14 : len(frame)-2]
	key, err := hkdf.Key(sha256.New, material, []byte("LKFrameEncryptionKey"), string(make([]byte, 128)), 16)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := aes.NewCipher(key)
	aead, _ := cipher.NewGCM(block)
	plain, err := aead.Open(nil, iv, frame[1:len(frame)-14], frame[:1])
	if err != nil {
		t.Fatalf("audio authentication failed: %v", err)
	}
	return append([]byte{frame[0]}, plain...)
}
