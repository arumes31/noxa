package webrtc

import (
	"github.com/pion/rtp"
	"testing"
)

type capturedMedia struct {
	delivery MediaDelivery
	commit   MediaCommit
}
type scopedCapture struct{ packets []capturedMedia }

func (*scopedCapture) WriteRTP(*rtp.Packet) error { panic("scoped tap lost delivery identity") }
func (s *scopedCapture) WriteMedia(_ *rtp.Packet, d MediaDelivery, commit MediaCommit) error {
	s.packets = append(s.packets, capturedMedia{d, commit})
	return nil
}

func TestScopedTapPreservesSourcesAndRechecksBufferedPermissions(t *testing.T) {
	r := NewRouter(nil)
	capture := &scopedCapture{}
	r.AddOutput("tap", capture)
	r.addVideoOutput("tap", capture)
	r.JoinChannel(1, "tap")
	r.JoinChannel(1, "alice")
	r.JoinChannel(1, "bob")
	allowed := true
	r.SetMediaGuard(func(_ MediaDelivery, write func() error) error {
		if allowed {
			return write()
		}
		return nil
	})
	for _, sender := range []string{"alice", "bob"} {
		for _, slot := range []string{SlotMic, SlotScreenAudio} {
			if r.ForwardRTP(sender, slot, makeAudioPacket(t, 1, -1)) != 1 {
				t.Fatal("missing scoped audio")
			}
		}
		for _, slot := range []string{SlotCam, SlotScreen} {
			if r.ForwardVideo(sender, slot, "", makeAudioPacket(t, 1, -1)) != 1 {
				t.Fatal("missing scoped video")
			}
		}
	}
	if len(capture.packets) != 8 || capture.packets[0].delivery.SenderID == capture.packets[4].delivery.SenderID {
		t.Fatal("publisher/slot identity collapsed")
	}
	allowed = false
	for _, packet := range capture.packets {
		if err := packet.commit(func() error { t.Fatal("revoked buffered packet delivered"); return nil }); err != nil {
			t.Fatal(err)
		}
	}
	allowed = true
	if err := r.SetVideoLimits(100000, VideoBounds{}); err != nil {
		t.Fatal(err)
	}
	if err := capture.packets[2].commit(func() error { t.Fatal("old video policy delivered"); return nil }); err != nil {
		t.Fatal(err)
	}
}
