package webrtc

import (
	"testing"

	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
)

func TestSubscriberPLIRepairsCurrentLayerWhileSwitchWaitsForKeyframe(t *testing.T) {
	e, err := New(testLogger(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	r := NewRouter(nil)
	attachFakePeer(t, e, r, "viewer")
	r.JoinChannel(1, "viewer")
	r.JoinChannel(1, "pub")
	testVideoPublication(t, r, "pub", "viewer", SlotCam)
	registerVideoSource(r, "pub", SlotCam, "h", 10)
	registerVideoSource(r, "pub", SlotCam, "f", 20)
	output := pubTrackFor(r, "viewer", "pub").video[SlotCam]
	capture := &extensionCapture{}
	_, err = output.track.Bind(extensionTrackContext{codec: webrtc.RTPCodecParameters{RTPCodecCapability: output.track.Codec(), PayloadType: 96}, writer: capture})
	if err != nil {
		t.Fatal(err)
	}
	if r.ForwardVideo("pub", SlotCam, "h", continuityPacket(10, 100, 90000, 10, true)) != 1 {
		t.Fatal("initial current layer was not forwarded")
	}
	feedback := &fakeRTCPWriter{}
	r.rtcpWriters["pub"] = feedback
	if err := r.SetVideoQuality("viewer", "high"); err != nil {
		t.Fatal(err)
	}
	if feedback.pliCount() != 1 || feedback.pkts[0][0].(*rtcp.PictureLossIndication).MediaSSRC != 20 {
		t.Fatal("quality change did not request the desired layer's keyframe")
	}
	if r.ForwardVideo("pub", SlotCam, "f", continuityPacket(20, 1000, 900000, 100, false)) != 0 {
		t.Fatal("desired layer switched before supplying a keyframe")
	}
	if r.ForwardVideo("pub", SlotCam, "h", continuityPacket(10, 101, 93000, 11, false)) != 1 || output.videoSource.Load() != 10 {
		t.Fatal("current layer did not remain active while the switch was pending")
	}

	// A receiver missing a reference frame on the still-active layer sends
	// PLI for this binding. Repeating the desired layer request cannot repair
	// its current dependency chain and is also coalesced with the switch PLI.
	r.relayKeyframeRequest("viewer", output.sender)
	currentRequested := false
	for _, batch := range feedback.pkts {
		for _, packet := range batch {
			if pli, ok := packet.(*rtcp.PictureLossIndication); ok && pli.MediaSSRC == 10 {
				currentRequested = true
			}
		}
	}
	if !currentRequested {
		t.Fatalf("receiver PLI never requested a keyframe from active SSRC 10 while desired SSRC 20 was pending: %+v", feedback.pkts)
	}
	r.relayKeyframeRequest("viewer", output.sender)
	if feedback.pliCount() != 2 {
		t.Fatal("repeated receiver feedback bypassed per-source keyframe coalescing")
	}

	if r.ForwardVideo("pub", SlotCam, "f", continuityPacket(20, 1001, 903000, 101, true)) != 1 || output.videoSource.Load() != 20 {
		t.Fatal("desired keyframe did not complete the source switch")
	}
	// Open a new request window without sleeping. Once the desired source is
	// current, feedback must request it once and never wake the retired layer.
	r.mu.Lock()
	clear(r.keyframeLast)
	r.mu.Unlock()
	feedback.pkts = nil
	r.relayKeyframeRequest("viewer", output.sender)
	if feedback.pliCount() != 1 || feedback.pkts[0][0].(*rtcp.PictureLossIndication).MediaSSRC != 20 {
		t.Fatalf("completed switch requested the wrong or duplicate source: %+v", feedback.pkts)
	}
}
