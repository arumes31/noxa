package webrtc

import (
	"github.com/pion/rtcp"
	"testing"
)

func TestMediaDeliveryGuardCoversAudioVideoAndRecorder(t *testing.T) {
	for _, video := range []bool{false, true} {
		r := NewRouter(nil)
		writer := &fakeTrackWriter{}
		r.JoinChannel(7, "publisher")
		r.JoinChannel(7, "recorder")
		slot := SlotMic
		if video {
			slot = SlotCam
			r.addVideoOutput("recorder", writer)
		} else {
			r.AddOutput("recorder", writer)
		}
		allow := true
		calls := 0
		r.SetMediaGuard(func(d MediaDelivery, write func() error) error {
			calls++
			if d.SenderID != "publisher" || d.RecipientID != "recorder" || d.ChannelID != 7 || d.RecipientChannelID != 7 || d.Slot != slot || !d.Tap || d.Whisper {
				t.Fatalf("incorrect routing scope: %+v", d)
			}
			// A guard must be able to inspect router state without deadlocking.
			if !r.mu.TryLock() {
				t.Fatal("guard ran while router lock was held")
			}
			r.mu.Unlock()
			if !allow {
				return nil
			}
			return write()
		})
		forward := func() int {
			if video {
				return r.ForwardVideo("publisher", slot, "", makeAudioPacket(t, 1, -1))
			}
			return r.ForwardRTP("publisher", slot, makeAudioPacket(t, 1, -1))
		}
		if got := forward(); got != 1 {
			t.Fatalf("allowed writes = %d", got)
		}
		allow = false
		if got := forward(); got != 0 || writer.count() != 1 || calls != 2 {
			t.Fatalf("revoked packet reached recorder: sent=%d count=%d calls=%d", got, writer.count(), calls)
		}
	}
}

func TestRecorderSelectsScreenInsteadOfConcurrentCamera(t *testing.T) {
	r := NewRouter(nil)
	writer := &fakeTrackWriter{}
	r.JoinChannel(7, "publisher")
	r.JoinChannel(7, "recorder")
	r.addVideoOutput("recorder", writer)
	rtcpWriter := &fakeRTCPWriter{}
	r.rtcpWriters["publisher"] = rtcpWriter
	registerVideoSource(r, "publisher", SlotCam, "f", 1001)
	registerVideoSource(r, "publisher", SlotCam, "h", 1002)
	registerVideoSource(r, "publisher", SlotCam, "q", 1003)
	r.SetTrackSlots("publisher", map[string]string{"camera": SlotCam, "display": SlotScreen})
	if sent := r.ForwardVideo("publisher", SlotScreen, "", makeAudioPacket(t, 1, -1)); sent != 1 {
		t.Fatal("screen disappeared from recording")
	}
	if sent := r.ForwardVideo("publisher", SlotCam, "", makeAudioPacket(t, 2, -1)); sent != 0 {
		t.Fatal("camera was interleaved into the screen recording")
	}
	r.SetTrackSlots("publisher", map[string]string{"camera": SlotCam})
	if sent := r.ForwardVideo("publisher", SlotCam, "h", makeAudioPacket(t, 3, -1)); sent != 1 {
		t.Fatal("camera did not resume after screen stopped")
	}
	if len(rtcpWriter.pkts) != 1 {
		t.Fatalf("missing recorder keyframe: %+v", rtcpWriter.pkts)
	}
	if pli, ok := rtcpWriter.pkts[0][0].(*rtcp.PictureLossIndication); !ok || pli.MediaSSRC != 1002 {
		t.Fatalf("keyframe was not requested for the selected layer: %+v", rtcpWriter.pkts)
	}
}
