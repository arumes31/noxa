package webrtc

import (
	"bytes"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
)

func TestMediaCCRegistersPrimaryAndRTXAndCopiesPackets(t *testing.T) {
	i, err := (mediaCCFactory{}).NewInterceptor("test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = i.Close() })
	cc := i.(*mediaCCInterceptor)
	cc.pacer.SetTargetBitrate(0)
	written := make(chan rtp.Packet, 2)
	info := &interceptor.StreamInfo{ID: "sender", SSRC: 11, SSRCRetransmission: 22}
	writer := i.BindLocalStream(info, interceptor.RTPWriterFunc(func(h *rtp.Header, p []byte, _ interceptor.Attributes) (int, error) {
		written <- rtp.Packet{Header: h.Clone(), Payload: append([]byte(nil), p...)}
		return h.MarshalSize() + len(p), nil
	}))
	for _, ssrc := range []uint32{11, 22} {
		header := rtp.Header{Version: 2, SSRC: ssrc, SequenceNumber: 1, CSRC: []uint32{3, 4}}
		payload := bytes.Repeat([]byte{7}, 2000)
		if _, err := writer.Write(&header, payload, nil); err != nil {
			t.Fatal(err)
		}
		header.CSRC[0], payload[0] = 99, 99
	}
	cc.pacer.SetTargetBitrate(1_500_000)
	for _, ssrc := range []uint32{11, 22} {
		select {
		case packet := <-written:
			if packet.SSRC != ssrc || packet.CSRC[0] != 3 || len(packet.Payload) != 2000 || packet.Payload[0] != 7 {
				t.Fatalf("corrupt RTP: %+v", packet.Header)
			}
		case <-time.After(time.Second):
			t.Fatal("GCC did not register/deliver primary or RTX stream")
		}
	}
	i.UnbindLocalStream(info)
	cc.pacer.mu.Lock()
	defer cc.pacer.mu.Unlock()
	if len(cc.pacer.streams) != 0 || cc.pacer.count != 0 {
		t.Fatal("unbound stream retained")
	}
}

func TestMediaPacerRetainsPacketDebtAtLowBitrate(t *testing.T) {
	p := newMediaPacer()
	p.SetTargetBitrate(8000)
	var count atomic.Int32
	p.AddStream(1, interceptor.RTPWriterFunc(func(*rtp.Header, []byte, interceptor.Attributes) (int, error) { count.Add(1); return 1200, nil }))
	for range 20 {
		_, _ = p.Write(&rtp.Header{Version: 2, SSRC: 1}, make([]byte, 1188), nil)
	}
	time.Sleep(300 * time.Millisecond)
	_ = p.Close()
	if got := count.Load(); got > 1 {
		t.Fatalf("8kbps paced %d packets in 300ms; packet debt lost", got)
	}
}

func TestAudioDoesNotWaitBehindCongestedVideo(t *testing.T) {
	i, err := (mediaCCFactory{}).NewInterceptor("audio-priority")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = i.Close() })
	p := i.(*mediaCCInterceptor).pacer
	p.SetTargetBitrate(0)
	video := i.BindLocalStream(&interceptor.StreamInfo{ID: "video", SSRC: 11, MimeType: "video/VP8"}, interceptor.RTPWriterFunc(func(*rtp.Header, []byte, interceptor.Attributes) (int, error) {
		t.Error("zero-budget video emitted")
		return 0, nil
	}))
	written := make(chan struct{}, 1)
	audio := i.BindLocalStream(&interceptor.StreamInfo{ID: "audio", SSRC: 22, MimeType: "audio/opus"}, interceptor.RTPWriterFunc(func(*rtp.Header, []byte, interceptor.Attributes) (int, error) {
		written <- struct{}{}
		return 80, nil
	}))
	for range mediaPacerPackets {
		_, _ = video.Write(&rtp.Header{Version: 2, SSRC: 11}, make([]byte, 1200), nil)
	}
	_, _ = audio.Write(&rtp.Header{Version: 2, SSRC: 22}, make([]byte, 60), nil)
	select {
	case <-written:
	case <-time.After(time.Second):
		t.Fatal("voice was starved by the video congestion queue")
	}
}

func TestAudioQueueBoundsAndUnbind(t *testing.T) {
	p := newMediaPacer()
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	p.AddStream(1, interceptor.RTPWriterFunc(func(*rtp.Header, []byte, interceptor.Attributes) (int, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		return 1024, nil
	}))
	p.mu.Lock()
	p.streams[1].audio = true
	p.mu.Unlock()
	t.Cleanup(func() { close(release); _ = p.Close() })
	_, _ = p.Write(&rtp.Header{Version: 2, SSRC: 1}, []byte{1}, nil)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("worker did not enter write")
	}
	for range mediaAudioPackets * 2 {
		_, _ = p.Write(&rtp.Header{Version: 2, SSRC: 1}, make([]byte, 1024), nil)
	}
	p.mu.Lock()
	if p.audioCount == 0 || p.audioCount > mediaAudioPackets || p.audioBytes > mediaAudioBytes {
		t.Error("audio queue exceeded its independent bounds")
	}
	p.mu.Unlock()
	p.removeStream(1, 0)
	p.mu.Lock()
	if p.audioCount != 0 || p.audioBytes != 0 {
		t.Error("unbound audio retained queued packets")
	}
	p.mu.Unlock()
}

func TestMediaPacerQueueIsBoundedAndUnbindClearsIt(t *testing.T) {
	p := newMediaPacer()
	t.Cleanup(func() { _ = p.Close() })
	p.SetTargetBitrate(0)
	p.AddStream(1, interceptor.RTPWriterFunc(func(*rtp.Header, []byte, interceptor.Attributes) (int, error) {
		t.Error("zero-bitrate queue emitted")
		return 0, nil
	}))
	for range mediaPacerPackets * 2 {
		if _, err := p.Write(&rtp.Header{Version: 2, SSRC: 1}, make([]byte, 4000), nil); err != nil {
			t.Fatal(err)
		}
	}
	p.mu.Lock()
	if p.count == 0 || p.count > mediaPacerPackets || p.bytes > mediaPacerBytes {
		t.Error("queue limits violated")
	}
	p.mu.Unlock()
	p.removeStream(1, 0)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.count != 0 || p.bytes != 0 || len(p.streams) != 0 {
		t.Fatal("unbind retained buffered media")
	}
}
