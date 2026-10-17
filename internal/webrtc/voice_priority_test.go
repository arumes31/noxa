package webrtc

import (
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
)

// Voice and video must never share an MSID stream: browsers lip-sync tracks
// of one MediaStream, which would delay voice behind a jittery video buffer.
func TestMicNeverSharesStreamWithVideoOrScreenAudio(t *testing.T) {
	for _, publisher := range []string{"a", "noxa", "user|x"} {
		mic := slotStreamID(publisher, SlotMic)
		for slot := range slotKinds {
			if slot != SlotMic && slotStreamID(publisher, slot) == mic {
				t.Fatalf("publisher %q mic shares MSID stream %q with slot %q", publisher, mic, slot)
			}
		}
	}
}

// The engine registers Opus at mediaOpusPayloadType (engine.go), the payload
// type the socket layer uses to give voice priority and DSCP EF.
func TestSocketClassifiesOnlyOpusRTPAsVoice(t *testing.T) {
	header := rtp.Header{Version: 2, PayloadType: mediaOpusPayloadType, SSRC: 1}
	raw, _ := header.Marshal()
	if !isVoiceRTP(append(raw, 1, 2, 3)) {
		t.Fatal("Opus RTP not classified as voice")
	}
	for _, pt := range []uint8{96, 98, 102, 116, 45} {
		header.PayloadType = pt
		raw, _ = header.Marshal()
		if isVoiceRTP(append(raw, make([]byte, 1000)...)) {
			t.Fatalf("video PT %d classified as voice", pt)
		}
	}
	for _, other := range [][]byte{
		{0x00, 0x01, 0x00, 0x00, 0x21, 0x12, 0xa4, 0x42, 0, 0, 0, 0}, // STUN
		{0x16, 0xfe, 0xfd, 0, 0, 0, 0, 0, 0, 0, 0, 0},                // DTLS
		{0x80, 200, 0, 6, 0, 0, 0, 1, 0, 0, 0, 0},                    // RTCP SR
		{0x81, 205, 0, 4, 0, 0, 0, 1, 0, 0, 0, 0},                    // RTCP TWCC
		{0x80, 0xef}, // short
	} {
		if isVoiceRTP(other) {
			t.Fatalf("non-voice datagram %x classified as voice", other[:2])
		}
	}
}

// The pacer's voice lane must not wait for the video worker, even while a
// video write is stuck in the socket.
func TestVoiceIsNotQueuedBehindBlockedVideoWrite(t *testing.T) {
	i, err := (mediaCCFactory{}).NewInterceptor("voice-behind-video")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = i.Close() })
	p := i.(*mediaCCInterceptor).pacer
	p.SetTargetBitrate(50_000_000)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	var videoCalls atomic.Int32
	video := i.BindLocalStream(&interceptor.StreamInfo{ID: "video", SSRC: 11, MimeType: "video/H264"}, interceptor.RTPWriterFunc(func(*rtp.Header, []byte, interceptor.Attributes) (int, error) {
		if videoCalls.Add(1) == 1 {
			close(entered)
			<-release // e.g. a socket write stalled behind a full send queue
		}
		return 1200, nil
	}))
	voice := make(chan time.Time, 64)
	audio := i.BindLocalStream(&interceptor.StreamInfo{ID: "voice", SSRC: 22, MimeType: "audio/opus"}, interceptor.RTPWriterFunc(func(*rtp.Header, []byte, interceptor.Attributes) (int, error) {
		voice <- time.Now()
		return 80, nil
	}))
	_, _ = video.Write(&rtp.Header{Version: 2, SSRC: 11}, make([]byte, 1188), nil)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("video worker did not start writing")
	}
	for n := range 10 {
		sent := time.Now()
		_, _ = audio.Write(&rtp.Header{Version: 2, SSRC: 22, SequenceNumber: uint16(n)}, make([]byte, 60), nil)
		select {
		case at := <-voice:
			if delay := at.Sub(sent); delay > 20*time.Millisecond {
				t.Fatalf("voice packet %d delayed %s behind a stalled video write", n, delay)
			}
		case <-time.After(time.Second):
			t.Fatalf("voice packet %d waited behind a stalled video write", n)
		}
	}
	once.Do(func() { close(release) })
}

// Voice is written as soon as it is queued: no pacing tick, no added delay.
func TestVoiceBypassesPacingTick(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		i, err := (mediaCCFactory{}).NewInterceptor("voice-no-tick")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = i.Close() }()
		var at time.Time
		audio := i.BindLocalStream(&interceptor.StreamInfo{ID: "voice", SSRC: 22, MimeType: "audio/opus"}, interceptor.RTPWriterFunc(func(*rtp.Header, []byte, interceptor.Attributes) (int, error) {
			at = time.Now()
			return 80, nil
		}))
		time.Sleep(2 * time.Millisecond) // Off the 5 ms video tick.
		sent := time.Now()
		_, _ = audio.Write(&rtp.Header{Version: 2, SSRC: 22}, make([]byte, 60), nil)
		synctest.Wait()
		if at.IsZero() || !at.Equal(sent) {
			t.Fatalf("voice waited for the pacing tick: sent=%v written=%v", sent, at)
		}
	})
}

// The pacing budget is the estimate minus voice: voice is reserved out of the
// video share, never the reverse.
func TestVideoPacingBudgetReservesVoice(t *testing.T) {
	measure := func(withVoice bool) (videoBytes, voiceBytes int) {
		synctest.Test(t, func(t *testing.T) {
			i, err := (mediaCCFactory{}).NewInterceptor("voice-reserve")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = i.Close() }()
			p := i.(*mediaCCInterceptor).pacer
			video := i.BindLocalStream(&interceptor.StreamInfo{ID: "video", SSRC: 11, MimeType: "video/H264"}, interceptor.RTPWriterFunc(func(_ *rtp.Header, payload []byte, _ interceptor.Attributes) (int, error) {
				videoBytes += len(payload) + 12
				return len(payload) + 12, nil
			}))
			audio := i.BindLocalStream(&interceptor.StreamInfo{ID: "voice", SSRC: 22, MimeType: "audio/opus"}, interceptor.RTPWriterFunc(func(_ *rtp.Header, payload []byte, _ interceptor.Attributes) (int, error) {
				voiceBytes += len(payload) + 12
				return len(payload) + 12, nil
			}))
			p.SetTargetBitrate(800_000) // 150 kB/s video pacing budget at 1.5x
			for range 200 {             // 1 s of 20 ms frames
				for range 30 {
					_, _ = video.Write(&rtp.Header{Version: 2, SSRC: 11}, make([]byte, 1188), nil)
				}
				if withVoice {
					for range 4 { // 4 voice streams at ~50 kbit/s each
						_, _ = audio.Write(&rtp.Header{Version: 2, SSRC: 22}, make([]byte, 300), nil)
					}
				}
				time.Sleep(20 * time.Millisecond)
				synctest.Wait()
			}
		})
		return videoBytes, voiceBytes
	}
	videoAlone, _ := measure(false)
	videoShared, voice := measure(true)
	if voice < 200*4*300 {
		t.Fatalf("voice was throttled: %d bytes", voice)
	}
	if got, want := videoAlone-videoShared, voice*8/10; got < want {
		t.Fatalf("video did not yield its budget to voice: video alone=%d with voice=%d voice=%d", videoAlone, videoShared, voice)
	}
}

// Shared-socket ownership goes to waiting voice before any waiting video.
func TestSocketGateHandsVoiceAheadOfQueuedVideo(t *testing.T) {
	var gate socketWriteGate
	var queued atomic.Int32
	gate.acquireHook = func() { queued.Add(1) }
	set := func(time.Time) error { return nil }
	entered, release := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	var order []string
	record := func(name string) func() (int, error) {
		return func() (int, error) { mu.Lock(); order = append(order, name); mu.Unlock(); return 1, nil }
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = gate.write(set, func() (int, error) { close(entered); <-release; return 1, nil })
	}()
	<-entered
	for n, name := range []string{"video1", "video2", "video3"} {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = gate.write(set, record(name)) }()
		for queued.Load() != int32(n+1) {
			time.Sleep(time.Millisecond)
		}
	}
	wg.Add(1)
	go func() { defer wg.Done(); _, _ = gate.writePriority(true, set, record("voice")) }()
	for queued.Load() != 4 {
		time.Sleep(time.Millisecond)
	}
	close(release)
	wg.Wait()
	if len(order) != 4 || order[0] != "voice" || order[1] != "video1" || order[3] != "video3" {
		t.Fatalf("socket ownership order = %v; voice must go first, video FIFO", order)
	}
}

// A writer that times out while queued must not leak ownership.
func TestSocketGateTimeoutDoesNotLeakOwnership(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var gate socketWriteGate
		set := func(time.Time) error { return nil }
		release := make(chan struct{})
		go func() { _, _ = gate.write(set, func() (int, error) { <-release; return 1, nil }) }()
		synctest.Wait()
		if _, err := gate.writePriority(true, set, func() (int, error) { return 1, nil }); err != os.ErrDeadlineExceeded {
			t.Fatalf("queued voice writer: %v", err)
		}
		close(release)
		synctest.Wait()
		if n, err := gate.writePriority(true, set, func() (int, error) { return 7, nil }); n != 7 || err != nil {
			t.Fatalf("gate leaked ownership after timeout: n=%d err=%v", n, err)
		}
	})
}

// One stalled voice write must not put the voice output into the 250 ms
// cooldown used for video backlogs; the next packet goes out immediately.
func TestVoiceSocketTimeoutHasNoCooldown(t *testing.T) {
	for _, slot := range []string{SlotMic, SlotScreenAudio, SlotScreen} {
		t.Run(slot, func(t *testing.T) {
			registry := &mediaEgressRegistry{}
			stream := &mediaEgressStream{active: true, registry: registry}
			delivery := MediaDelivery{SenderID: "s", RecipientID: "r", Slot: slot}
			first, ok := stream.prepare(&rtp.Packet{Header: rtp.Header{Version: 2, SSRC: 1}}, mediaTicket{delivery: delivery})
			if !ok {
				t.Fatal("first admission failed")
			}
			timeout := interceptor.RTPWriterFunc(func(*rtp.Header, []byte, interceptor.Attributes) (int, error) {
				return 0, os.ErrDeadlineExceeded
			})
			if _, err := stream.write(&first.Header, first.Payload, nil, timeout); err == nil {
				t.Fatal("timeout not reported")
			}
			_, admitted := stream.prepare(&rtp.Packet{Header: rtp.Header{Version: 2, SSRC: 1}}, mediaTicket{delivery: delivery})
			voice := slot != SlotScreen
			if admitted != voice {
				t.Fatalf("after a socket timeout admitted=%v, want %v (voice exempt from cooldown, video not)", admitted, voice)
			}
			stream.mu.RLock()
			active := stream.active
			stream.mu.RUnlock()
			if !active {
				t.Fatal("socket timeout retired the output")
			}
		})
	}
}
