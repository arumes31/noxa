package webrtc

import (
	"encoding/binary"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/cc"
	"github.com/pion/rtp"
	"github.com/pion/sdp/v3"

	"noxa/internal/mediacc/gcc"
)

const mediaPacerPackets = 1024
const mediaPacerBytes = 2 * 1024 * 1024
const mediaPacerLifetime = 500 * time.Millisecond
const mediaAudioPackets = 256
const mediaAudioBytes = 128 * 1024
const mediaAudioLifetime = 100 * time.Millisecond

type pacedStream struct {
	writer    interceptor.RTPWriter
	active    bool // protected by mediaPacer.mu
	bindingID string
	twccID    uint8
	audio     bool
}
type pacedPacket struct {
	header  rtp.Header
	payload []byte
	attrs   interceptor.Attributes
	stream  *pacedStream
	expires time.Time
	size    int
}

// mediaPacer always queues, even with no backlog: StaticRTP holds its track
// lock during Write, so entering the authority there would invert teardown.
// The worker owns final writes. Close cancels queued work and joins it.
type mediaPacer struct {
	mu                                sync.Mutex
	streams                           map[uint32]*pacedStream
	queue                             [mediaPacerPackets]pacedPacket
	head, count, bytes                int
	bitrate                           int
	closed                            bool
	stop, done                        chan struct{}
	egress                            *mediaEgressRegistry
	transportSequence                 uint16 // owned by the pacing worker
	historyMu                         sync.Mutex
	sent                              uint64 // historyMu covers insertion and feedback delivery
	audioQueue                        [mediaAudioPackets]pacedPacket
	audioHead, audioCount, audioBytes int
}

func newMediaPacer() *mediaPacer {
	p := &mediaPacer{streams: make(map[uint32]*pacedStream), bitrate: 1_500_000, stop: make(chan struct{}), done: make(chan struct{})}
	go p.run()
	return p
}

func (p *mediaPacer) AddStream(ssrc uint32, writer interceptor.RTPWriter) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	if old := p.streams[ssrc]; old != nil {
		old.active = false
	}
	p.streams[ssrc] = &pacedStream{writer: writer, active: true}
}

func (p *mediaPacer) addRTX(ssrc, rtx uint32) {
	if rtx == 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if stream := p.streams[ssrc]; stream != nil {
		p.streams[rtx] = stream
	}
}

func (p *mediaPacer) removeStream(ssrc, rtx uint32) {
	p.mu.Lock()
	defer p.mu.Unlock()
	stream := p.streams[ssrc]
	if stream == nil {
		return
	}
	stream.active = false
	delete(p.streams, ssrc)
	if p.streams[rtx] == stream {
		delete(p.streams, rtx)
	}
	count := p.count
	for range count {
		packet := p.popLocked()
		if packet.stream != stream {
			p.pushLocked(packet)
		}
	}
	count = p.audioCount
	for range count {
		packet := p.popAudioLocked()
		if packet.stream != stream {
			p.pushAudioLocked(packet)
		}
	}
}

func (p *mediaPacer) SetTargetBitrate(rate int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.bitrate = min(max(rate, 0), 1_000_000_000)
}

func (p *mediaPacer) Write(header *rtp.Header, payload []byte, attrs interceptor.Attributes) (int, error) {
	size := header.MarshalSize() + len(payload) + int(header.PaddingSize)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return 0, io.ErrClosedPipe
	}
	stream := p.streams[header.SSRC]
	if stream == nil || !stream.active || size > 65535 {
		return size, nil
	}
	if stream.audio {
		if p.audioCount == mediaAudioPackets || p.audioBytes+size > mediaAudioBytes {
			return size, nil
		}
	} else if p.count == mediaPacerPackets || p.bytes+size > mediaPacerBytes {
		return size, nil
	}
	attributes := make(interceptor.Attributes, len(attrs))
	for key, value := range attrs {
		attributes[key] = value
	}
	packet := pacedPacket{header: header.Clone(), payload: append([]byte(nil), payload...), attrs: attributes, stream: stream, expires: time.Now().Add(mediaPacerLifetime), size: size}
	if stream.audio {
		packet.expires = time.Now().Add(mediaAudioLifetime)
		p.pushAudioLocked(packet)
	} else {
		p.pushLocked(packet)
	}
	return size, nil
}

func (p *mediaPacer) pushAudioLocked(packet pacedPacket) {
	p.audioQueue[(p.audioHead+p.audioCount)%mediaAudioPackets] = packet
	p.audioCount++
	p.audioBytes += packet.size
}

func (p *mediaPacer) popAudioLocked() pacedPacket {
	packet := p.audioQueue[p.audioHead]
	p.audioQueue[p.audioHead] = pacedPacket{}
	p.audioHead = (p.audioHead + 1) % mediaAudioPackets
	p.audioCount--
	p.audioBytes -= packet.size
	return packet
}

func (p *mediaPacer) pushLocked(packet pacedPacket) {
	p.queue[(p.head+p.count)%mediaPacerPackets] = packet
	p.count++
	p.bytes += packet.size
}
func (p *mediaPacer) popLocked() pacedPacket {
	packet := p.queue[p.head]
	p.queue[p.head] = pacedPacket{}
	p.head = (p.head + 1) % mediaPacerPackets
	p.count--
	p.bytes -= packet.size
	return packet
}

func (p *mediaPacer) run() {
	defer close(p.done)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	last := time.Now()
	budget := float64(0)
	for {
		select {
		case <-p.stop:
			return
		case now := <-ticker.C:
			p.mu.Lock()
			// Voice has its own bounded queue and does not spend the video
			// pacing budget. A video retransmission backlog cannot starve it.
			// Both queues still cross the same final authorization boundary.
			for count := p.audioCount; count > 0 && p.audioCount > 0 && !p.closed; count-- {
				packet := p.popAudioLocked()
				if !packet.stream.active || !time.Now().Before(packet.expires) {
					continue
				}
				p.mu.Unlock()
				p.writePacket(packet)
				p.mu.Lock()
			}
			// Bound catch-up after a stalled socket; never release a huge burst.
			bytesPerSecond := float64(p.bitrate) * 1.5 / 8
			budget = min(budget+min(now.Sub(last), 50*time.Millisecond).Seconds()*bytesPerSecond, 0.05*bytesPerSecond)
			last = now
			for work := p.count; work > 0 && p.count > 0 && !p.closed; work-- {
				if p.audioCount > 0 {
					break
				}
				front := &p.queue[p.head]
				if !front.stream.active || !time.Now().Before(front.expires) {
					p.popLocked()
					continue
				}
				if budget <= 0 {
					break
				}
				packet := p.popLocked()
				p.mu.Unlock()
				p.writePacket(packet)
				budget -= float64(packet.size)
				p.mu.Lock()
			}
			p.mu.Unlock()
		}
	}
}

func (p *mediaPacer) writePacket(packet pacedPacket) {
	write := interceptor.RTPWriterFunc(func(header *rtp.Header, payload []byte, attrs interceptor.Attributes) (int, error) {
		if packet.stream.twccID != 0 {
			p.historyMu.Lock()
			defer p.historyMu.Unlock()
			var extension [2]byte
			binary.BigEndian.PutUint16(extension[:], p.transportSequence)
			if err := header.SetExtension(packet.stream.twccID, extension[:]); err != nil {
				return 0, err
			}
			p.transportSequence++
		}
		// GCC accounts the send here, after queue expiry and final permission
		// checks. Locally discarded packets cannot manufacture network loss.
		n, err := packet.stream.writer.Write(header, payload, attrs)
		if packet.stream.twccID != 0 {
			p.sent++
		}
		return n, err
	})
	if p.egress == nil { // Standalone pacing tests have no router bindings.
		_, _ = write.Write(&packet.header, packet.payload, packet.attrs)
		return
	}
	p.egress.mu.Lock()
	stream := p.egress.streams[packet.stream.bindingID]
	p.egress.mu.Unlock()
	if stream != nil {
		_, _ = stream.write(&packet.header, packet.payload, packet.attrs, write)
	}
}

func (p *mediaPacer) Close() error {
	p.mu.Lock()
	if !p.closed {
		p.closed = true
		for _, stream := range p.streams {
			stream.active = false
		}
		clear(p.streams)
		clear(p.queue[:])
		p.count, p.bytes = 0, 0
		clear(p.audioQueue[:])
		p.audioCount, p.audioBytes = 0, 0
		close(p.stop)
	}
	p.mu.Unlock()
	<-p.done
	return nil
}

// Each peer owns a paced GCC estimator; unbind removes both primary and RTX.
type mediaCCFactory struct{ registry *mediaEgressRegistry }

func (f mediaCCFactory) NewInterceptor(id string) (interceptor.Interceptor, error) {
	pacer := newMediaPacer()
	pacer.egress = f.registry
	factory, err := cc.NewInterceptor(func() (cc.BandwidthEstimator, error) {
		estimator, err := gcc.NewSendSideBWE(gcc.SendSideBWEInitialBitrate(1_500_000), gcc.SendSideBWEPacer(pacer))
		if err != nil {
			return nil, err
		}
		return &mediaBandwidthEstimator{BandwidthEstimator: estimator, pacer: pacer}, nil
	})
	if err != nil {
		_ = pacer.Close()
		return nil, err
	}
	inner, err := factory.NewInterceptor(id)
	if err != nil {
		_ = pacer.Close()
		return nil, err
	}
	return &mediaCCInterceptor{Interceptor: inner, pacer: pacer}, nil
}

type mediaCCInterceptor struct {
	interceptor.Interceptor
	pacer *mediaPacer
}

func (i *mediaCCInterceptor) BindLocalStream(info *interceptor.StreamInfo, writer interceptor.RTPWriter) interceptor.RTPWriter {
	// GCC's AddStream registers the primary SSRC with the injected pacer.
	out := i.Interceptor.BindLocalStream(info, writer)
	i.pacer.mu.Lock()
	if stream := i.pacer.streams[info.SSRC]; stream != nil {
		stream.bindingID = info.ID
		stream.audio = strings.EqualFold(info.MimeType, "audio/opus")
		for _, extension := range info.RTPHeaderExtensions {
			if !stream.audio && extension.URI == sdp.TransportCCURI && extension.ID > 0 && extension.ID < 256 {
				stream.twccID = uint8(extension.ID)
			}
		}
	}
	i.pacer.mu.Unlock()
	i.pacer.addRTX(info.SSRC, info.SSRCRetransmission)
	return out
}
func (i *mediaCCInterceptor) UnbindLocalStream(info *interceptor.StreamInfo) {
	i.pacer.removeStream(info.SSRC, info.SSRCRetransmission)
	i.Interceptor.UnbindLocalStream(info)
}
