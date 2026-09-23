package webrtc

import (
	"encoding/binary"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/cc"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/sdp/v3"
	"noxa/internal/mediacc/gcc"
)

func TestVideoFeedbackAccountsRTXAndStillReducesForCongestion(t *testing.T) {
	pacer := newMediaPacer()
	bwe, err := gcc.NewSendSideBWE(gcc.SendSideBWEInitialBitrate(1_500_000), gcc.SendSideBWEPacer(pacer))
	if err != nil {
		t.Fatal(err)
	}
	estimator := &mediaBandwidthEstimator{BandwidthEstimator: bwe, pacer: pacer}
	factory, err := cc.NewInterceptor(func() (cc.BandwidthEstimator, error) { return estimator, nil })
	if err != nil {
		t.Fatal(err)
	}
	inner, err := factory.NewInterceptor("feedback")
	if err != nil {
		t.Fatal(err)
	}
	i := &mediaCCInterceptor{Interceptor: inner, pacer: pacer}
	t.Cleanup(func() { _ = i.Close() })
	written := make(chan rtp.Header, 1)
	sink := interceptor.RTPWriterFunc(func(h *rtp.Header, payload []byte, _ interceptor.Attributes) (int, error) {
		written <- h.Clone()
		return h.MarshalSize() + len(payload), nil
	})
	extensions := []interceptor.RTPHeaderExtension{{ID: 3, URI: sdp.TransportCCURI}}
	audio := i.BindLocalStream(&interceptor.StreamInfo{SSRC: 1, MimeType: "audio/opus", RTPHeaderExtensions: extensions}, sink)
	video := i.BindLocalStream(&interceptor.StreamInfo{SSRC: 2, SSRCRetransmission: 3, MimeType: "video/VP8", RTPHeaderExtensions: extensions}, sink)
	send := func(writer interceptor.RTPWriter, ssrc uint32) rtp.Header {
		t.Helper()
		if _, err := writer.Write(&rtp.Header{Version: 2, SSRC: ssrc}, make([]byte, 200), nil); err != nil {
			t.Fatal(err)
		}
		select {
		case h := <-written:
			return h
		case <-time.After(time.Second):
			t.Fatal("media not delivered")
			return rtp.Header{}
		}
	}
	for batch := range 2 {
		feedback := &rtcp.TransportLayerCC{BaseSequenceNumber: uint16(batch * 30), PacketStatusCount: 30, ReferenceTime: uint32(1000 + batch*10),
			PacketChunks: []rtcp.PacketStatusChunk{&rtcp.RunLengthChunk{RunLength: 30, PacketStatusSymbol: rtcp.TypeTCCPacketReceivedSmallDelta}}}
		for n := range 30 {
			if h := send(audio, 1); len(h.GetExtension(3)) != 0 {
				t.Fatal("voice acquired video feedback")
			}
			ssrc := uint32(2)
			if n%3 == 0 {
				ssrc = 3
			}
			h := send(video, ssrc)
			ext := h.GetExtension(3)
			if len(ext) != 2 || binary.BigEndian.Uint16(ext) != uint16(batch*30+n) {
				t.Fatalf("primary/RTX feedback gap: %v", ext)
			}
			feedback.RecvDeltas = append(feedback.RecvDeltas, &rtcp.RecvDelta{Type: rtcp.TypeTCCPacketReceivedSmallDelta, Delta: 50_000})
		}
		if err := estimator.WriteRTCP([]rtcp.Packet{feedback}, nil); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(time.Second)
	for bwe.GetTargetBitrate() >= 1_500_000 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if bwe.GetTargetBitrate() >= 1_500_000 {
		t.Fatal("growing video delay did not reduce the pacing target")
	}
	if got := bwe.GetStats()["averageLoss"].(float64); got != 0 {
		t.Fatalf("interleaved audio/RTX manufactured loss: %v", got)
	}
}

func TestVideoFeedbackDoesNotUseUnpacedAudio(t *testing.T) {
	_, factories, err := newEngineMedia(false, VideoBounds{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	chain, err := factories.Build("separate-media-feedback")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = chain.Close() })
	written := make(chan *rtp.Header, 3)
	sink := interceptor.RTPWriterFunc(func(h *rtp.Header, payload []byte, _ interceptor.Attributes) (int, error) {
		copy := h.Clone()
		written <- &copy
		return h.MarshalSize() + len(payload), nil
	})
	for i, mime := range []string{"audio/opus", "video/VP8", "video/VP8"} {
		ssrc := uint32(i + 1)
		writer := chain.BindLocalStream(&interceptor.StreamInfo{SSRC: ssrc, MimeType: mime,
			RTPHeaderExtensions: []interceptor.RTPHeaderExtension{{ID: 3, URI: sdp.TransportCCURI}}}, sink)
		if _, err := writer.Write(&rtp.Header{Version: 2, SSRC: ssrc}, []byte{1}, nil); err != nil {
			t.Fatal(err)
		}
		select {
		case packet := <-written:
			ext := packet.GetExtension(3)
			if i == 0 {
				if len(ext) != 0 {
					t.Fatal("unpaced audio entered video bandwidth feedback")
				}
			} else if len(ext) != 2 || binary.BigEndian.Uint16(ext) != uint16(i-1) {
				t.Fatalf("video feedback sequence has a gap: %v", ext)
			}
		case <-time.After(time.Second):
			t.Fatal("media did not reach network writer")
		}
	}
}

func TestMediaTransportSequenceExcludesDeniedAndExpiredPackets(t *testing.T) {
	registry := &mediaEgressRegistry{}
	stream := &mediaEgressStream{active: true, registry: registry}
	registry.streams = map[string]*mediaEgressStream{"test": stream}
	_, factories, err := newEngineMedia(false, VideoBounds{}, registry)
	if err != nil {
		t.Fatal(err)
	}
	chain, err := factories.Build("test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stream.stop(); _ = chain.Close() })
	written := make(chan uint16, 4)
	info := &interceptor.StreamInfo{ID: "test", SSRC: 11, RTPHeaderExtensions: []interceptor.RTPHeaderExtension{{ID: 3, URI: sdp.TransportCCURI}}}
	writer := chain.BindLocalStream(info, interceptor.RTPWriterFunc(func(h *rtp.Header, p []byte, _ interceptor.Attributes) (int, error) {
		ext := h.GetExtension(3)
		if len(ext) != 2 {
			t.Error("missing transport sequence")
			return 0, nil
		}
		written <- binary.BigEndian.Uint16(ext)
		return h.MarshalSize() + len(p), nil
	}))
	var deny atomic.Bool
	checked := make(chan struct{}, 4)
	guard := func(_ MediaDelivery, write func() error) error {
		defer func() { checked <- struct{}{} }()
		if deny.Load() {
			return nil
		}
		return write()
	}
	send := func() {
		t.Helper()
		packet, _ := stream.prepare(&rtp.Packet{Header: rtp.Header{Version: 2, SSRC: 11}, Payload: []byte{1}}, mediaTicket{guard: guard})
		if _, err := writer.Write(&packet.Header, packet.Payload, nil); err != nil {
			t.Fatal(err)
		}
	}
	wait := func() {
		t.Helper()
		select {
		case <-checked:
		case <-time.After(time.Second):
			t.Fatal("terminal guard not reached")
		}
	}
	send()
	wait()
	first := <-written
	deny.Store(true)
	send()
	wait()
	deny.Store(false)
	send()
	wait()
	second := <-written
	if second != first+1 {
		t.Fatalf("local denial created a network-loss gap: %d -> %d", first, second)
	}
}

type feedbackRecorder struct {
	cc.BandwidthEstimator
	packets chan int
}

func (r *feedbackRecorder) WriteRTCP(packets []rtcp.Packet, _ interceptor.Attributes) error {
	r.packets <- len(packets)
	return nil
}

func TestFeedbackWaitsForInFlightHistoryInsertion(t *testing.T) {
	p := newMediaPacer()
	t.Cleanup(func() { _ = p.Close() })
	p.sent, p.transportSequence = 250, 250
	entered, release, sent := make(chan struct{}), make(chan struct{}), make(chan struct{})
	writer := interceptor.RTPWriterFunc(func(*rtp.Header, []byte, interceptor.Attributes) (int, error) {
		close(entered)
		<-release
		return 1, nil
	})
	go func() {
		p.writePacket(pacedPacket{header: rtp.Header{Version: 2}, payload: []byte{1}, stream: &pacedStream{writer: writer, twccID: 3}})
		close(sent)
	}()
	<-entered
	recorder := &feedbackRecorder{packets: make(chan int, 1)}
	estimator := &mediaBandwidthEstimator{BandwidthEstimator: recorder, pacer: p}
	done := make(chan struct{})
	go func() {
		_ = estimator.WriteRTCP([]rtcp.Packet{&rtcp.TransportLayerCC{BaseSequenceNumber: 0, PacketStatusCount: 1,
			PacketChunks: []rtcp.PacketStatusChunk{&rtcp.RunLengthChunk{RunLength: 1}}}}, nil)
		close(done)
	}()
	close(release)
	<-sent
	<-done
	if <-recorder.packets != 0 {
		t.Fatal("feedback used history evicted by the in-flight send")
	}
}

func TestTransportFeedbackIgnoresWirePaddingAndEvictedHistory(t *testing.T) {
	feedback := &rtcp.TransportLayerCC{
		Header:             rtcp.Header{Count: rtcp.FormatTCC, Type: rtcp.TypeTransportSpecificFeedback},
		BaseSequenceNumber: 0, PacketStatusCount: 1,
		PacketChunks: []rtcp.PacketStatusChunk{&rtcp.StatusVectorChunk{Type: rtcp.TypeTCCStatusVectorChunk, SymbolSize: rtcp.TypeTCCSymbolSizeTwoBit, SymbolList: []uint16{rtcp.TypeTCCPacketReceivedSmallDelta}}},
		RecvDeltas:   []*rtcp.RecvDelta{{Type: rtcp.TypeTCCPacketReceivedSmallDelta, Delta: 250}},
	}
	feedback.Header.Length = uint16(feedback.MarshalSize()/4 - 1)
	raw, err := feedback.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var decoded rtcp.TransportLayerCC
	if err := decoded.Unmarshal(raw); err != nil {
		t.Fatal(err)
	}
	original := decoded.PacketChunks[0].(*rtcp.StatusVectorChunk)
	if len(original.SymbolList) <= 1 {
		t.Fatal("fixture did not reproduce wire padding")
	}
	normalized := boundedTransportFeedback(&decoded, 1)
	if normalized == nil || len(normalized.PacketChunks[0].(*rtcp.StatusVectorChunk).SymbolList) != 1 {
		t.Fatal("padding would be interpreted as loss")
	}
	if len(original.SymbolList) <= 1 {
		t.Fatal("shared RTCP feedback was mutated")
	}
	if boundedTransportFeedback(&decoded, 251) != nil || boundedTransportFeedback(&decoded, 0) != nil {
		t.Fatal("unknown send history reached estimator")
	}
	decoded.BaseSequenceNumber = 65535
	if boundedTransportFeedback(&decoded, 65536) == nil {
		t.Fatal("wire sequence wrap rejected")
	}
}
