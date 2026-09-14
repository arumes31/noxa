package webrtc

import (
	"testing"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
)

// authorizationTrack changes server permissions between received packets.
// All media remains in memory; no peer connection or external service is used.
type authorizationTrack struct {
	fakeVideoTrack
	beforeRead func(int)
}

func (f *authorizationTrack) ReadRTP() (*rtp.Packet, interceptor.Attributes, error) {
	f.beforeRead(f.reads())
	return f.fakeTrackReader.ReadRTP()
}

func TestAudioAuthorizationDoesNotTrustLevelMetadata(t *testing.T) {
	for _, tc := range []struct {
		name  string
		extID uint8
		level int
	}{
		{"extension_not_negotiated", 0, -1},
		{"extension_omitted", 1, -1},
		{"forged_silence", 1, 127},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRouter(nil)
			r.SetHandlers(func(string) bool { return false }, nil)
			r.JoinChannel(1, "publisher")
			tap := &fakeTrackWriter{}
			r.AddOutput("recorder", tap)
			r.JoinChannel(1, "recorder")
			track := &fakeTrackReader{packets: []*rtp.Packet{makeAudioPacket(t, 1, tc.level)}}
			r.ReadLoop("publisher", SlotMic, track, tc.extID)
			if got := tap.count(); got != 0 {
				t.Fatalf("denied audio reached channel recorder: %d packets", got)
			}
		})
	}
}

func TestAudioAuthorizationRevokedDuringContinuousSpeech(t *testing.T) {
	r := NewRouter(nil)
	allowed := true
	r.SetHandlers(func(string) bool { return allowed }, nil)
	r.JoinChannel(1, "publisher")
	tap := &fakeTrackWriter{}
	r.AddOutput("recorder", tap)
	r.JoinChannel(1, "recorder")
	track := &authorizationTrack{
		fakeVideoTrack: fakeVideoTrack{fakeTrackReader: fakeTrackReader{packets: []*rtp.Packet{
			makeAudioPacket(t, 1, 10), makeAudioPacket(t, 2, 10), makeAudioPacket(t, 3, 10),
		}}},
		beforeRead: func(n int) { allowed = n == 0 },
	}
	r.ReadLoop("publisher", SlotMic, track, 1)
	if got := tap.count(); got != 1 {
		t.Fatalf("forwarded %d packets, want only the packet before revocation", got)
	}
}

func TestVideoAuthorizationRevokedDuringLiveTrack(t *testing.T) {
	r := NewRouter(nil)
	allowed := true
	r.SetVideoHandlers(func(string) bool { return allowed })
	r.JoinChannel(1, "publisher")
	tap := &fakeTrackWriter{}
	r.addVideoOutput("recorder", tap)
	r.JoinChannel(1, "recorder")
	track := &authorizationTrack{
		fakeVideoTrack: fakeVideoTrack{ssrc: 42, fakeTrackReader: fakeTrackReader{packets: []*rtp.Packet{
			makeAudioPacket(t, 1, -1), makeAudioPacket(t, 2, -1), makeAudioPacket(t, 3, -1),
		}}},
		beforeRead: func(n int) { allowed = n == 0 },
	}
	r.ReadVideoLoop("publisher", SlotCam, track)
	if got := tap.count(); got != 1 {
		t.Fatalf("forwarded %d packets, want only the packet before revocation", got)
	}
}
