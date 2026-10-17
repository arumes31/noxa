package webrtc

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	pion "github.com/pion/webrtc/v4"
	"go.uber.org/goleak"
)

// In-process chaos test of the production video fan-out over real Pion
// ICE/DTLS/SRTP: one screen publisher, several viewers that join, leave and
// rejoin, injected loss/reorder/duplication/jitter on publisher ingress and on
// individual viewer egress, a stalled viewer, publisher restarts and the
// H.265 <-> H.264 switch triggered by a viewer without HEVC.
//
// The default run takes roughly half a minute. NOXA_VIDEO_CHAOS_SOAK=<duration>
// (for example 10m) repeats the churn cycle until the duration elapses and
// additionally compares heap usage between cycles.
// NOXA_VIDEO_CHAOS_VIEWERS=6..10 selects the steady viewer count (default 6).

const (
	chaosH264Fmtp    = "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42001f"
	chaosH265Fmtp    = "profile-id=1;tier-flag=0;level-id=93"
	chaosHEVCLevel   = 93
	chaosFPS         = 60
	chaosFUBytes     = 900
	chaosKeyFUs      = 18
	chaosDeltaFUs    = 3
	chaosTagMagic    = 0xa5
	chaosTagLength   = 9
	chaosPublisherID = "pub"
	chaosTrackID     = "screen-track"
	chaosKeyTimeout  = 4 * time.Second
)

// chaosLink injects impairment into one direction of one RTP flow. A zero
// value forwards everything unchanged.
type chaosLink struct {
	mu                  sync.Mutex
	rng                 *rand.Rand
	loss, dup, reorder  float64
	jitter              time.Duration
	blackout            bool
	dropped, duplicated atomic.Int64
	delayed             atomic.Int64
}

func newChaosLink(seed int64) *chaosLink { return &chaosLink{rng: rand.New(rand.NewSource(seed))} }

func (l *chaosLink) set(loss, dup, reorder float64, jitter time.Duration) {
	l.mu.Lock()
	l.loss, l.dup, l.reorder, l.jitter = loss, dup, reorder, jitter
	l.mu.Unlock()
}

func (l *chaosLink) setBlackout(on bool) {
	l.mu.Lock()
	l.blackout = on
	l.mu.Unlock()
}

func (l *chaosLink) decide() (drop, dup bool, delay time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.blackout || (l.loss > 0 && l.rng.Float64() < l.loss) {
		l.dropped.Add(1)
		return true, false, 0
	}
	if l.dup > 0 && l.rng.Float64() < l.dup {
		dup = true
		l.duplicated.Add(1)
	}
	if l.reorder > 0 && l.rng.Float64() < l.reorder {
		// A reordered packet is overtaken by its successors.
		delay = time.Duration(5+l.rng.Intn(25)) * time.Millisecond
	} else if l.jitter > 0 {
		delay = time.Duration(l.rng.Int63n(int64(l.jitter)))
	}
	if delay > 0 {
		l.delayed.Add(1)
	}
	return false, dup, delay
}

// chaosInterceptor is registered before Pion's defaults, so it sits next to
// the network: retransmissions and feedback cross the impaired link too.
type chaosInterceptor struct {
	interceptor.NoOp
	link    *chaosLink
	pending *sync.WaitGroup
}

type chaosInterceptorFactory struct {
	link    *chaosLink
	pending *sync.WaitGroup
}

func (f chaosInterceptorFactory) NewInterceptor(string) (interceptor.Interceptor, error) {
	return &chaosInterceptor{link: f.link, pending: f.pending}, nil
}

func chaosVideo(info *interceptor.StreamInfo) bool {
	return strings.HasPrefix(strings.ToLower(info.MimeType), "video/")
}

func (i *chaosInterceptor) BindLocalStream(info *interceptor.StreamInfo, writer interceptor.RTPWriter) interceptor.RTPWriter {
	if !chaosVideo(info) {
		return writer
	}
	return interceptor.RTPWriterFunc(func(header *rtp.Header, payload []byte, attrs interceptor.Attributes) (int, error) {
		drop, dup, delay := i.link.decide()
		size := header.MarshalSize() + len(payload)
		if drop {
			return size, nil
		}
		copies := 1
		if dup {
			copies = 2
		}
		for range copies {
			if delay <= 0 {
				if _, err := writer.Write(header, payload, attrs); err != nil {
					return 0, err
				}
				continue
			}
			clone := header.Clone()
			data := append([]byte(nil), payload...)
			i.pending.Add(1)
			time.AfterFunc(delay, func() {
				defer i.pending.Done()
				_, _ = writer.Write(&clone, data, interceptor.Attributes{})
			})
		}
		return size, nil
	})
}

func (i *chaosInterceptor) BindRemoteStream(info *interceptor.StreamInfo, reader interceptor.RTPReader) interceptor.RTPReader {
	if !chaosVideo(info) {
		return reader
	}
	var queued [][]byte
	var held []byte
	return interceptor.RTPReaderFunc(func(b []byte, attrs interceptor.Attributes) (int, interceptor.Attributes, error) {
		for {
			if len(queued) > 0 {
				n := copy(b, queued[0])
				queued = queued[1:]
				return n, interceptor.Attributes{}, nil
			}
			n, a, err := reader.Read(b, attrs)
			if err != nil {
				return n, a, err
			}
			drop, dup, delay := i.link.decide()
			if drop {
				continue
			}
			if dup {
				queued = append(queued, slices.Clone(b[:n]))
			}
			if delay > 0 && held == nil {
				held = slices.Clone(b[:n])
				continue
			}
			if held != nil {
				queued = append(queued, held)
				held = nil
			}
			return n, a, nil
		}
	})
}

func chaosAPI(t *testing.T, link *chaosLink, pending *sync.WaitGroup, hevc bool) *pion.API {
	t.Helper()
	media := &pion.MediaEngine{}
	register := func(capability pion.RTPCodecCapability, pt pion.PayloadType) {
		if err := media.RegisterCodec(pion.RTPCodecParameters{RTPCodecCapability: capability, PayloadType: pt}, pion.RTPCodecTypeVideo); err != nil {
			t.Fatal(err)
		}
		if err := media.RegisterCodec(pion.RTPCodecParameters{RTPCodecCapability: pion.RTPCodecCapability{MimeType: pion.MimeTypeRTX, ClockRate: 90000, SDPFmtpLine: fmt.Sprintf("apt=%d", pt)}, PayloadType: pt + 1}, pion.RTPCodecTypeVideo); err != nil {
			t.Fatal(err)
		}
	}
	if hevc {
		register(pion.RTPCodecCapability{MimeType: pion.MimeTypeH265, ClockRate: 90000, SDPFmtpLine: chaosH265Fmtp}, 116)
	}
	register(pion.RTPCodecCapability{MimeType: pion.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: chaosH264Fmtp}, 102)
	register(pion.RTPCodecCapability{MimeType: pion.MimeTypeVP8, ClockRate: 90000}, 96)
	if err := media.RegisterCodec(pion.RTPCodecParameters{RTPCodecCapability: pion.RTPCodecCapability{MimeType: pion.MimeTypeOpus, ClockRate: 48000, Channels: 2}, PayloadType: 111}, pion.RTPCodecTypeAudio); err != nil {
		t.Fatal(err)
	}
	registry := &interceptor.Registry{}
	registry.Add(chaosInterceptorFactory{link: link, pending: pending})
	if err := pion.RegisterDefaultInterceptors(media, registry); err != nil {
		t.Fatal(err)
	}
	settings := pion.SettingEngine{}
	settings.SetIncludeLoopbackCandidate(true)
	settings.SetIPFilter(func(ip net.IP) bool { return ip.IsLoopback() })
	settings.SetNetworkTypes([]pion.NetworkType{pion.NetworkTypeUDP4})
	return pion.NewAPI(pion.WithMediaEngine(media), pion.WithInterceptorRegistry(registry), pion.WithSettingEngine(settings))
}

// chaosTrackLocal sends one encoded stream on one SSRC and switches its
// payload type in place, like a browser changing codec without simulcast.
type chaosTrackLocal struct {
	mu     sync.Mutex
	writer pion.TrackLocalWriter
	codecs []pion.RTPCodecParameters
	ssrc   uint32
}

func (c *chaosTrackLocal) Bind(ctx pion.TrackLocalContext) (pion.RTPCodecParameters, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writer, c.codecs = ctx.WriteStream(), slices.Clone(ctx.CodecParameters())
	c.ssrc = uint32(ctx.SSRC())
	for _, codec := range c.codecs {
		if strings.EqualFold(codec.MimeType, pion.MimeTypeH265) || strings.EqualFold(codec.MimeType, pion.MimeTypeH264) {
			return codec, nil
		}
	}
	return pion.RTPCodecParameters{}, errors.New("chaos publisher: no H.264/H.265 negotiated")
}

func (c *chaosTrackLocal) Unbind(pion.TrackLocalContext) error {
	c.mu.Lock()
	c.writer = nil
	c.mu.Unlock()
	return nil
}

func (c *chaosTrackLocal) ID() string              { return chaosTrackID }
func (c *chaosTrackLocal) RID() string             { return "" }
func (c *chaosTrackLocal) StreamID() string        { return "pub-screen" }
func (c *chaosTrackLocal) Kind() pion.RTPCodecType { return pion.RTPCodecTypeVideo }

func (c *chaosTrackLocal) write(mime string, packets []*rtp.Packet) bool {
	c.mu.Lock()
	writer, ssrc := c.writer, c.ssrc
	var pt pion.PayloadType
	found := false
	for _, codec := range c.codecs {
		if strings.EqualFold(codec.MimeType, mime) {
			pt, found = codec.PayloadType, true
			break
		}
	}
	c.mu.Unlock()
	if writer == nil || !found {
		return false
	}
	for _, packet := range packets {
		packet.PayloadType, packet.SSRC = uint8(pt), ssrc
		_, err := writer.WriteRTP(&packet.Header, packet.Payload)
		if err != nil {
			return false
		}
	}
	return true
}

type chaosEnv struct {
	t        *testing.T
	engine   *Engine
	router   *Router
	voice    *Voice
	pending  sync.WaitGroup
	mu       sync.Mutex
	clients  map[string]*fanoutChaosClient
	ingress  *chaosLink
	seed     atomic.Int64
	pub      *chaosPublisher
	failures atomic.Int64
	tornDown sync.Once
	reported atomic.Bool
}

type fanoutChaosClient struct {
	id    string
	pc    *pion.PeerConnection
	sigMu sync.Mutex
	done  chan struct{}
	// The server peer this client negotiated with; offers of a replaced
	// server peer are never applied to a reconnected client.
	server atomic.Pointer[PeerConnectionWrapper]
	// Viewer-only state.
	viewer *chaosViewer
}

func (e *chaosEnv) errorf(format string, args ...any) {
	e.failures.Add(1)
	e.t.Errorf(format, args...)
}

func newChaosEnv(t *testing.T) *chaosEnv {
	t.Helper()
	engine, err := NewWithNetwork(testLogger(), nil, false, NetworkConfig{UDPAddr: "127.0.0.1:0", ExternalIPs: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	engine.iceServers = nil
	router := NewRouter(nil)
	voice := NewVoice(engine, router, nil)
	voice.renegDebounce, voice.renegRateLimit = 10*time.Millisecond, 50*time.Millisecond
	env := &chaosEnv{t: t, engine: engine, router: router, voice: voice, clients: make(map[string]*fanoutChaosClient), ingress: newChaosLink(1)}
	env.seed.Store(time.Now().UnixNano())
	voice.SetOfferSender(func(clientID, _ string) error {
		env.mu.Lock()
		client := env.clients[clientID]
		env.mu.Unlock()
		wrapper := engine.PeerConnection(clientID)
		if client == nil || wrapper == nil {
			return ErrNoPeer
		}
		go env.answer(client, wrapper)
		return nil
	})
	return env
}

func gatherChaos(pc *pion.PeerConnection) error {
	select {
	case <-pion.GatheringCompletePromise(pc):
		return nil
	case <-time.After(5 * time.Second):
		return errors.New("ICE gathering timeout")
	}
}

// answer applies one server-initiated offer. The candidate-complete local
// description replaces trickle signaling in this single-host test.
func (e *chaosEnv) answer(client *fanoutChaosClient, wrapper *PeerConnectionWrapper) {
	client.sigMu.Lock()
	defer client.sigMu.Unlock()
	select {
	case <-client.done:
		return
	default:
	}
	if client.server.Load() != wrapper {
		return
	}
	if err := gatherChaos(wrapper.pc); err != nil {
		return
	}
	offer := wrapper.pc.LocalDescription()
	if offer == nil || offer.Type != pion.SDPTypeOffer {
		return
	}
	if err := client.pc.SetRemoteDescription(*offer); err != nil {
		return
	}
	answer, err := client.pc.CreateAnswer(nil)
	if err != nil {
		return
	}
	if err := client.pc.SetLocalDescription(answer); err != nil {
		return
	}
	if err := gatherChaos(client.pc); err != nil {
		return
	}
	started := time.Now()
	_ = e.voice.HandleAnswer(client.id, client.pc.LocalDescription().SDP)
	if os.Getenv("NOXA_VIDEO_CHAOS_TIMING") != "" {
		e.t.Logf("timing: %s answered at %s (server took %v)", client.id, started.Format("15:04:05.000"), time.Since(started))
	}
}

func (e *chaosEnv) register(client *fanoutChaosClient) {
	e.mu.Lock()
	e.clients[client.id] = client
	e.mu.Unlock()
}

func (e *chaosEnv) unregister(id string) *fanoutChaosClient {
	e.mu.Lock()
	defer e.mu.Unlock()
	client := e.clients[id]
	delete(e.clients, id)
	return client
}

// --- publisher -------------------------------------------------------------

type chaosSwitch struct {
	at         time.Time
	generation uint32
	mime       string
}

type chaosPublisher struct {
	env           *chaosEnv
	client        *fanoutChaosClient
	track         *chaosTrackLocal
	generation    atomic.Uint32
	keyframe      atomic.Bool
	plis          atomic.Int64
	keyframes     atomic.Int64
	mu            sync.Mutex
	mime          string
	switches      []chaosSwitch
	pubGen        uint64
	stop          chan struct{}
	wg            sync.WaitGroup
	plisAt        []time.Time
	sent          atomic.Int64 // frames written
	writeFailures atomic.Int64
	maxGap        atomic.Int64 // longest interval between written frames, ns
}

func (p *chaosPublisher) currentMime() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.mime
}

func (p *chaosPublisher) lastSwitch() chaosSwitch {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.switches[len(p.switches)-1]
}

func (p *chaosPublisher) plisSince(since time.Time) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, at := range p.plisAt {
		if !at.Before(since) {
			n++
		}
	}
	return n
}

// offerInitial runs the client-initiated first offer/answer, as browsers do:
// the server never offers to a peer that has not negotiated yet (Voice
// suspends renegotiation until the client's initial answer is set).
func (e *chaosEnv) offerInitial(client *fanoutChaosClient) {
	t := e.t
	t.Helper()
	pc := client.pc
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := pc.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	if err := gatherChaos(pc); err != nil {
		t.Fatal(err)
	}
	client.sigMu.Lock()
	answer, err := e.voice.HandleOffer(client.id, pc.LocalDescription().SDP, nil)
	if err != nil {
		client.sigMu.Unlock()
		t.Fatal(err)
	}
	wrapper := e.engine.PeerConnection(client.id)
	client.server.Store(wrapper)
	if err := pc.SetRemoteDescription(pion.SessionDescription{Type: pion.SDPTypeAnswer, SDP: answer}); err != nil {
		client.sigMu.Unlock()
		t.Fatal(err)
	}
	// The answer carries no candidates. The server may already have queued a
	// renegotiation offer, so take its candidates from whichever description
	// it holds once gathering is complete.
	if err := gatherChaos(wrapper.pc); err != nil {
		client.sigMu.Unlock()
		t.Fatal(err)
	}
	for _, line := range strings.Split(wrapper.pc.LocalDescription().SDP, "\r\n") {
		if candidate, ok := strings.CutPrefix(line, "a=candidate:"); ok {
			mid := "0"
			if err := pc.AddICECandidate(pion.ICECandidateInit{Candidate: "candidate:" + candidate, SDPMid: &mid}); err != nil {
				client.sigMu.Unlock()
				t.Fatal(err)
			}
		}
	}
	client.sigMu.Unlock()
}

// startPublisher connects (or reconnects with a new DTLS identity) and
// publishes an HEVC-capable screen source.
func (e *chaosEnv) startPublisher(generation uint32) *chaosPublisher {
	t := e.t
	t.Helper()
	api := chaosAPI(t, e.ingress, &e.pending, true)
	pc, err := api.NewPeerConnection(pion.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	track := &chaosTrackLocal{}
	transceiver, err := pc.AddTransceiverFromTrack(track, pion.RTPTransceiverInit{Direction: pion.RTPTransceiverDirectionSendonly})
	if err != nil {
		t.Fatal(err)
	}
	client := &fanoutChaosClient{id: chaosPublisherID, pc: pc, done: make(chan struct{})}
	p := &chaosPublisher{env: e, client: client, track: track, mime: pion.MimeTypeH265, stop: make(chan struct{})}
	p.generation.Store(generation)
	p.keyframe.Store(true)
	p.switches = []chaosSwitch{{time.Now(), generation, pion.MimeTypeH265}}
	e.pub = p // teardown closes it even if negotiation below fails
	pc.OnTrack(func(remote *pion.TrackRemote, _ *pion.RTPReceiver) {
		go func() {
			for {
				if _, _, err := remote.ReadRTP(); err != nil {
					return
				}
			}
		}()
	})
	e.voice.JoinChannel(chaosPublisherID, 1)
	e.voice.DeclareTrackSlots(chaosPublisherID, map[string]string{chaosTrackID: SlotScreen})
	e.register(client)
	e.offerInitial(client)
	p.pubGen, err = e.router.PublishVideoWithFormats(chaosPublisherID, SlotScreen, 0, true, "source", []string{"h265", "h264", "vp8"}, chaosHEVCLevel)
	if err != nil {
		t.Fatal(err)
	}
	sender := transceiver.Sender()
	p.wg.Add(3)
	go func() {
		defer p.wg.Done()
		for {
			packets, _, err := sender.ReadRTCP()
			if err != nil {
				return
			}
			for _, packet := range packets {
				switch packet.(type) {
				case *rtcp.PictureLossIndication, *rtcp.FullIntraRequest:
					p.plis.Add(1)
					p.mu.Lock()
					p.plisAt = append(p.plisAt, time.Now())
					p.mu.Unlock()
					p.keyframe.Store(true)
				}
			}
		}
	}()
	go p.encode()
	go p.followCodec()
	e.pub = p
	return p
}

// followCodec mirrors the client: it polls the publication's aggregate codec
// choice and switches its single upload in place with a fresh keyframe.
func (p *chaosPublisher) followCodec() {
	defer p.wg.Done()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-tick.C:
		}
		for _, publication := range p.env.router.VideoPublications(chaosPublisherID) {
			if publication.PublisherID != chaosPublisherID || publication.Slot != SlotScreen || publication.Generation != p.pubGen {
				continue
			}
			want := ""
			switch publication.Codec {
			case "h265":
				want = pion.MimeTypeH265
			case "h264":
				want = pion.MimeTypeH264
			}
			p.mu.Lock()
			if want != "" && want != p.mime {
				p.mime = want
				generation := p.generation.Add(1)
				p.switches = append(p.switches, chaosSwitch{time.Now(), generation, want})
				p.env.t.Logf("publisher switch to %s generation %d at %s", want, generation, time.Now().Format("15:04:05.000"))
				p.keyframe.Store(true)
			}
			p.mu.Unlock()
		}
	}
}

func chaosTag(payload []byte, generation, frame uint32) []byte {
	payload = append(payload, chaosTagMagic)
	payload = binary.BigEndian.AppendUint32(payload, generation)
	return binary.BigEndian.AppendUint32(payload, frame)
}

func chaosReadTag(payload []byte) (generation, frame uint32, ok bool) {
	if len(payload) < chaosTagLength+3 || payload[len(payload)-chaosTagLength] != chaosTagMagic {
		return 0, 0, false
	}
	tag := payload[len(payload)-chaosTagLength+1:]
	return binary.BigEndian.Uint32(tag), binary.BigEndian.Uint32(tag[4:]), true
}

func chaosFrame(mime string, keyframe bool, generation, frame uint32, sequence *uint16, timestamp uint32) []*rtp.Packet {
	var packets []*rtp.Packet
	add := func(payload []byte) {
		packets = append(packets, &rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: *sequence, Timestamp: timestamp}, Payload: payload})
		*sequence++
	}
	filler := make([]byte, chaosFUBytes)
	fragments := chaosDeltaFUs
	if keyframe {
		fragments = chaosKeyFUs
	}
	if mime == pion.MimeTypeH265 {
		kind := byte(1)
		if keyframe {
			kind = 19
			add(hevcAP(hevcNAL(32, false), hevcNAL(33, false), hevcNAL(34, false)))
		}
		for i := range fragments {
			header := kind
			if i == 0 {
				header |= 0x80
			}
			if i == fragments-1 {
				header |= 0x40
			}
			payload := []byte{49 << 1, 1, header}
			if i == 0 {
				payload = append(payload, 0x80)
			}
			add(chaosTag(append(payload, filler...), generation, frame))
		}
	} else {
		indicator, kind := byte(0x5c), byte(1)
		if keyframe {
			indicator, kind = 0x7c, 5
			add([]byte{24, 0, 2, 0x67, 1, 0, 2, 0x68, 2})
		}
		for i := range fragments {
			header := kind
			if i == 0 {
				header |= 0x80
			}
			if i == fragments-1 {
				header |= 0x40
			}
			add(chaosTag(append([]byte{indicator, header}, filler...), generation, frame))
		}
	}
	packets[len(packets)-1].Marker = true
	return packets
}

func (p *chaosPublisher) encode() {
	defer p.wg.Done()
	tick := time.NewTicker(time.Second / chaosFPS)
	defer tick.Stop()
	sequence := uint16(rand.Intn(65536))
	var frame uint32
	var last time.Time
	for {
		select {
		case <-p.stop:
			return
		case <-tick.C:
		}
		if now := time.Now(); !last.IsZero() {
			if gap := int64(now.Sub(last)); gap > p.maxGap.Load() {
				p.maxGap.Store(gap)
			}
			last = now
		} else {
			last = now
		}
		frame++
		p.mu.Lock()
		mime := p.mime
		generation := p.generation.Load()
		p.mu.Unlock()
		keyframe := p.keyframe.Swap(false)
		if keyframe {
			p.keyframes.Add(1)
		}
		packets := chaosFrame(mime, keyframe, generation, frame, &sequence, frame*(90000/chaosFPS))
		if p.track.write(mime, packets) {
			p.sent.Add(1)
		} else {
			p.writeFailures.Add(1)
			if keyframe {
				p.keyframe.Store(true)
			}
		}
	}
}

func (p *chaosPublisher) close() {
	select {
	case <-p.stop:
		return
	default:
		close(p.stop)
	}
	p.client.sigMu.Lock()
	close(p.client.done)
	p.client.sigMu.Unlock()
	_ = p.client.pc.Close()
	p.wg.Wait()
}

// --- viewers ----------------------------------------------------------------

type chaosTrackStats struct {
	ssrc        uint32
	mime        string
	packets     int
	firstAt     time.Time
	firstKeyAt  time.Time
	lowest      int64
	lowestKey   bool
	lowestInfo  string
	highest     int64
	seen        map[int64]struct{}
	arrived     map[int64]time.Time
	missing     map[int64]time.Time
	bigJumps    int
	generations map[uint32]int
	lastFrameAt time.Time
	frames      int
	maxFrameGap time.Duration
	foreign     int
}

type chaosViewer struct {
	env         *chaosEnv
	id          string
	hevc        bool
	link        *chaosLink
	pc          *pion.PeerConnection
	stalled     atomic.Bool
	mu          sync.Mutex
	tracks      map[*pion.TrackRemote]*chaosTrackStats
	all         []*chaosTrackStats
	watchAt     time.Time
	keyAt       time.Time
	plis        int
	lastPLI     time.Time
	readers     sync.WaitGroup
	measureFrom time.Time
	genSeenAt   map[uint32]time.Time
	lateOld     int
	outage      bool // holes are repaired by keyframes, not retransmission
}

func chaosKeyStart(mime string, payload []byte) bool {
	if len(payload) == 0 {
		return false
	}
	if strings.EqualFold(mime, pion.MimeTypeH265) {
		return len(payload) > 4 && payload[0]>>1 == 48 && payload[4]>>1 == 32
	}
	return payload[0]&31 == 24 && len(payload) > 3 && payload[3]&31 == 7
}

func (v *chaosViewer) read(remote *pion.TrackRemote) {
	defer v.readers.Done()
	stats := &chaosTrackStats{ssrc: uint32(remote.SSRC()), seen: make(map[int64]struct{}), missing: make(map[int64]time.Time), generations: make(map[uint32]int)}
	v.mu.Lock()
	v.tracks[remote] = stats
	v.all = append(v.all, stats)
	v.mu.Unlock()
	var unwrap int64 = -1
	for {
		for v.stalled.Load() {
			time.Sleep(5 * time.Millisecond)
		}
		packet, _, err := remote.ReadRTP()
		if err != nil {
			return
		}
		now := time.Now()
		mime := remote.Codec().MimeType
		v.mu.Lock()
		if stats.mime == "" {
			stats.mime = mime
		} else if !strings.EqualFold(stats.mime, mime) {
			stats.foreign++
		}
		if isVideoPadding(packet) || len(packet.Payload) == 0 {
			v.mu.Unlock()
			continue
		}
		// Unwrap relative to the highest sequence seen on this track.
		var seq int64
		if unwrap < 0 {
			seq = int64(packet.SequenceNumber) + 1<<20
		} else {
			seq = unwrap + int64(rtpSequenceDelta(packet.SequenceNumber, uint16(unwrap)))
		}
		if stats.packets == 0 || seq > stats.highest {
			if stats.packets > 0 {
				if seq-stats.highest > 2000 {
					stats.bigJumps++
				} else {
					for missing := stats.highest + 1; missing < seq; missing++ {
						if _, ok := stats.seen[missing]; !ok {
							stats.missing[missing] = now
						}
					}
				}
			}
			stats.highest, unwrap = seq, seq
		}
		delete(stats.missing, seq)
		if stats.packets == 0 || seq < stats.lowest {
			stats.lowest = seq
			stats.lowestKey = chaosKeyStart(mime, packet.Payload)
			generation, frame, _ := chaosReadTag(packet.Payload)
			stats.lowestInfo = fmt.Sprintf("seq=%d arrival=%d at=%s gen=%d frame=%d head=% x", packet.SequenceNumber, stats.packets, now.Format("15:04:05.000"), generation, frame, packet.Payload[:min(6, len(packet.Payload))])
		}
		if stats.packets == 0 {
			stats.firstAt = now
		}
		stats.packets++
		stats.seen[seq] = struct{}{}
		if stats.arrived == nil {
			stats.arrived = make(map[int64]time.Time)
		}
		stats.arrived[seq] = now
		if chaosKeyStart(mime, packet.Payload) {
			if stats.firstKeyAt.IsZero() {
				stats.firstKeyAt = now
			}
			v.keyAt = now
		}
		if generation, _, ok := chaosReadTag(packet.Payload); ok {
			stats.generations[generation]++
			if v.genSeenAt == nil {
				v.genSeenAt = make(map[uint32]time.Time)
			}
			if _, ok := v.genSeenAt[generation]; !ok {
				v.genSeenAt[generation] = now
			}
			for newer, at := range v.genSeenAt {
				// A new source generation reached this viewer. Older encoded
				// media must not follow it on any track after a short in-flight
				// allowance for packets already on the wire.
				if newer > generation && now.Sub(at) > 300*time.Millisecond {
					v.lateOld++
					break
				}
			}
		}
		if packet.Marker {
			if !stats.lastFrameAt.IsZero() && !v.measureFrom.IsZero() && now.After(v.measureFrom) {
				stats.maxFrameGap = max(stats.maxFrameGap, now.Sub(stats.lastFrameAt))
			}
			stats.lastFrameAt = now
			stats.frames++
		}
		// Emulate a decoder: an unrepaired hole older than one second asks for
		// a keyframe, at most once per second per viewer, like Chromium.
		pli := false
		for seq, at := range stats.missing {
			if now.Sub(at) > time.Second {
				delete(stats.missing, seq)
				pli = true
			}
		}
		if len(stats.missing) > 4096 {
			clear(stats.missing)
		}
		if pli && now.Sub(v.lastPLI) > time.Second {
			v.lastPLI = now
			v.plis++
			v.mu.Unlock()
			_ = v.pc.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: uint32(remote.SSRC())}})
			continue
		}
		v.mu.Unlock()
	}
}

func (e *chaosEnv) clientPC(id string) *fanoutChaosClient {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.clients[id]
}

// joinViewer connects a viewer and explicitly watches the screen.
func (e *chaosEnv) joinViewer(id string, hevc bool) *chaosViewer {
	t := e.t
	t.Helper()
	link := newChaosLink(e.seed.Add(1))
	pc, err := chaosAPI(t, link, &e.pending, hevc).NewPeerConnection(pion.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	viewer := &chaosViewer{env: e, id: id, hevc: hevc, link: link, pc: pc, tracks: make(map[*pion.TrackRemote]*chaosTrackStats)}
	client := &fanoutChaosClient{id: id, pc: pc, done: make(chan struct{}), viewer: viewer}
	pc.OnTrack(func(remote *pion.TrackRemote, _ *pion.RTPReceiver) {
		if remote.Kind() != pion.RTPCodecTypeVideo {
			go func() {
				for {
					if _, _, err := remote.ReadRTP(); err != nil {
						return
					}
				}
			}()
			return
		}
		viewer.readers.Add(1)
		go viewer.read(remote)
	})
	// A browser viewer always has its microphone m-line.
	if _, err := pc.AddTransceiverFromKind(pion.RTPCodecTypeAudio, pion.RTPTransceiverInit{Direction: pion.RTPTransceiverDirectionRecvonly}); err != nil {
		t.Fatal(err)
	}
	e.register(client)
	e.voice.JoinChannel(id, 1)
	started := time.Now()
	e.offerInitial(client)
	if os.Getenv("NOXA_VIDEO_CHAOS_TIMING") != "" {
		e.t.Logf("timing: join %s at %s took %v", id, started.Format("15:04:05.000"), time.Since(started))
	}
	e.watch(viewer)
	e.voice.RefreshSubscriber(id)
	return viewer
}

func (e *chaosEnv) watch(viewer *chaosViewer) {
	codecs, level := []string{"h264", "vp8"}, uint8(0)
	if viewer.hevc {
		codecs, level = []string{"h265", "h264", "vp8"}, chaosHEVCLevel
	}
	for _, publication := range e.router.VideoPublications(viewer.id) {
		if publication.PublisherID != chaosPublisherID || publication.Slot != SlotScreen {
			continue
		}
		viewer.mu.Lock()
		viewer.watchAt = time.Now()
		viewer.mu.Unlock()
		if _, err := e.router.WatchVideoWithFormats(viewer.id, chaosPublisherID, SlotScreen, publication.Generation, publication.WatchRevision+1, e.router.VideoWatchSession(viewer.id), true, codecs, level); err != nil {
			e.errorf("%s watch: %v", viewer.id, err)
		}
	}
}

func (e *chaosEnv) leaveViewer(id string) *chaosViewer {
	client := e.unregister(id)
	if client == nil {
		return nil
	}
	client.sigMu.Lock()
	close(client.done)
	client.sigMu.Unlock()
	started := time.Now()
	_ = e.voice.ClosePeer(id)
	if os.Getenv("NOXA_VIDEO_CHAOS_TIMING") != "" {
		e.t.Logf("timing: ClosePeer(%s) at %s took %v", id, started.Format("15:04:05.000"), time.Since(started))
	}
	_ = client.pc.Close()
	client.viewer.readers.Wait()
	return client.viewer
}

func (v *chaosViewer) keyframeSince(since time.Time) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return !v.keyAt.IsZero() && !v.keyAt.Before(since)
}

// decodableSince reports frames arriving after since with either every hole
// repaired or a keyframe received after the viewer last asked for one.
func (v *chaosViewer) decodableSince(since time.Time) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	flowing, holes := false, false
	for _, stats := range v.all {
		if stats.lastFrameAt.After(since.Add(200 * time.Millisecond)) {
			flowing = true
		}
		if len(stats.missing) > 0 {
			holes = true
		}
	}
	repaired := !holes && (v.lastPLI.IsZero() || v.keyAt.After(v.lastPLI))
	return flowing && (repaired || v.keyAt.After(since))
}

func (v *chaosViewer) generationSeen(generation uint32) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	_, ok := v.genSeenAt[generation]
	return ok
}

func (e *chaosEnv) waitFor(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}

func (e *chaosEnv) viewers() []*chaosViewer {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []*chaosViewer
	for _, client := range e.clients {
		if client.viewer != nil {
			out = append(out, client.viewer)
		}
	}
	return out
}

// expectGeneration waits until every current viewer decodes the publisher's
// current generation from a keyframe.
func (e *chaosEnv) expectGeneration(label string, timeout time.Duration) {
	generation := e.pub.lastSwitch().generation
	for _, viewer := range e.viewers() {
		if !e.waitFor(timeout, func() bool { return viewer.generationSeen(generation) }) {
			e.errorf("%s: viewer %s did not receive generation %d within %v", label, viewer.id, generation, timeout)
		}
	}
}

// checkViewer validates one viewer's complete history.
func (e *chaosEnv) checkViewer(viewer *chaosViewer, clean bool) {
	viewer.mu.Lock()
	defer viewer.mu.Unlock()
	if viewer.lateOld > 0 {
		e.errorf("viewer %s received %d packets of an older source generation after a newer one", viewer.id, viewer.lateOld)
	}
	for _, stats := range viewer.all {
		if stats.packets == 0 {
			continue
		}
		if stats.foreign > 0 {
			e.errorf("viewer %s track ssrc=%d changed codec mid-track (%d packets)", viewer.id, stats.ssrc, stats.foreign)
		}
		if !stats.lowestKey && clean {
			// A lossy downlink can lose a keyframe's leading parameter sets
			// before the receiver knows the stream; that needs a PLI, as in
			// a browser, and is not an SFU ordering error.
			e.errorf("viewer %s track ssrc=%d (%s) did not start with a decodable keyframe: %s", viewer.id, stats.ssrc, stats.mime, stats.lowestInfo)
			for _, publication := range e.router.VideoOperatorDiagnostics().Publications {
				for _, diagnostic := range publication.Viewers {
					if diagnostic.ClientID == viewer.id {
						encoded, _ := json.Marshal(diagnostic)
						e.t.Logf("viewer %s diagnostics: %s", viewer.id, encoded)
					}
				}
			}
		}
		if stats.bigJumps > 0 {
			e.errorf("viewer %s track ssrc=%d: %d sequence discontinuities", viewer.id, stats.ssrc, stats.bigJumps)
		}
		if len(stats.generations) > 1 {
			e.errorf("viewer %s track ssrc=%d carried %d source generations", viewer.id, stats.ssrc, len(stats.generations))
		}
		if viewer.outage {
			continue
		}
		// Retransmissions cannot complete once a track is retired, so the
		// final half second of a track is not judged.
		top := stats.highest - 120
		span, holes := top-stats.lowest+1, int64(0)
		for seq := stats.lowest; seq <= top; seq++ {
			if _, ok := stats.seen[seq]; !ok {
				holes++
			}
		}
		allowed := span / 200 // ingress loss beyond the bounded repair window
		if !clean {
			allowed = span / 20
		}
		if span > 0 && holes > allowed+2 && !chaosRace {
			var ranges []string
			for seq := stats.lowest; seq <= top && len(ranges) < 6; seq++ {
				if _, ok := stats.seen[seq]; ok {
					continue
				}
				end := seq
				for end+1 <= top {
					if _, ok := stats.seen[end+1]; ok {
						break
					}
					end++
				}
				ranges = append(ranges, fmt.Sprintf("%d..%d(next at %s)", uint16(seq), uint16(end), stats.arrived[end+1].Format("15:04:05.000")))
				seq = end
			}
			e.errorf("viewer %s track ssrc=%d: %d/%d sequence numbers never delivered: %v", viewer.id, stats.ssrc, holes, span, ranges)
			e.ingressReport()
		}
	}
}

func chaosViewerCount(t *testing.T) int {
	count := 6
	if raw := os.Getenv("NOXA_VIDEO_CHAOS_VIEWERS"); raw != "" {
		if _, err := fmt.Sscanf(raw, "%d", &count); err != nil || count < 3 || count > 10 {
			t.Fatalf("NOXA_VIDEO_CHAOS_VIEWERS=%q: want 3..10", raw)
		}
	}
	return count
}

func TestVideoFanoutChaos(t *testing.T) {
	if testing.Short() {
		t.Skip("real-transport chaos run")
	}
	soak := time.Duration(0)
	if raw := os.Getenv("NOXA_VIDEO_CHAOS_SOAK"); raw != "" {
		var err error
		if soak, err = time.ParseDuration(raw); err != nil {
			t.Fatalf("NOXA_VIDEO_CHAOS_SOAK: %v", err)
		}
	}
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	env := newChaosEnv(t)
	defer env.teardown()
	viewerCount := chaosViewerCount(t)
	if os.Getenv("NOXA_VIDEO_CHAOS_CLEAN_INGRESS") == "" {
		env.ingress.set(0.02, 0.01, 0.03, 4*time.Millisecond)
	}
	pub := env.startPublisher(1)

	steady := make([]string, viewerCount)
	for i := range steady {
		steady[i] = fmt.Sprintf("v%d", i)
		env.joinViewer(steady[i], true)
	}
	env.expectGeneration("initial join", chaosKeyTimeout)

	// One viewer has a lossy, duplicating, reordering downlink for the whole run.
	lossy := env.clientPC(steady[1]).viewer
	lossy.link.set(0.03, 0.02, 0.03, 0)
	healthy := []*chaosViewer{env.clientPC(steady[0]).viewer, env.clientPC(steady[2]).viewer}

	cycles := 0
	start := time.Now()
	var heapAfterFirst uint64
	for {
		cycles++
		env.chaosCycle(steady, healthy, cycles)
		if t.Failed() {
			break
		}
		if cycles == 1 {
			heapAfterFirst = chaosHeap()
		}
		if soak == 0 || time.Since(start) > soak {
			break
		}
		if growth := int64(chaosHeap()) - int64(heapAfterFirst); growth > 64<<20 {
			t.Errorf("heap grew %d MiB after %d churn cycles", growth>>20, cycles)
			break
		}
	}
	t.Logf("cycles=%d publisher PLIs=%d keyframes=%d ingress dropped=%d dup=%d delayed=%d", cycles, pub.plis.Load(), env.pub.keyframes.Load(),
		env.ingress.dropped.Load(), env.ingress.duplicated.Load(), env.ingress.delayed.Load())

	stalled := env.clientPC(steady[len(steady)-1]).viewer
	for _, id := range steady {
		viewer := env.clientPC(id).viewer
		if viewer == stalled {
			// A complete two-second outage is repaired by a keyframe, not by
			// retransmission; recovery was asserted after each blackout.
			viewer.mu.Lock()
			viewer.outage = true
			viewer.mu.Unlock()
		}
		env.checkViewer(viewer, viewer != lossy)
		viewer.mu.Lock()
		t.Logf("viewer %s: tracks=%d plis=%d", id, len(viewer.all), viewer.plis)
		viewer.mu.Unlock()
	}
	env.teardown()
	env.checkRouterEmpty()
}

// teardown is idempotent and also runs after a fatal assertion, so leak
// detection reports only resources the SFU itself failed to release.
func (e *chaosEnv) teardown() {
	e.tornDown.Do(func() {
		for _, viewer := range e.viewers() {
			e.leaveViewer(viewer.id)
		}
		if e.pub != nil {
			e.pub.close()
		}
		e.unregister(chaosPublisherID)
		_ = e.voice.ClosePeer(chaosPublisherID)
		e.pending.Wait()
		if err := e.engine.Close(); err != nil {
			e.t.Error(err)
		}
	})
}

func chaosHeap() uint64 {
	runtime.GC()
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return stats.HeapInuse
}

// chaosCycle runs one round of churn, stalls, codec switching and a
// publisher restart while healthy viewers keep watching.
func (e *chaosEnv) chaosCycle(steady []string, healthy []*chaosViewer, cycle int) {
	t := e.t
	measure := func(label string, d time.Duration, maxPLIs int, action func()) {
		from := time.Now()
		for _, viewer := range healthy {
			viewer.mu.Lock()
			viewer.measureFrom = from
			for _, stats := range viewer.all {
				stats.maxFrameGap = 0
			}
			frames := 0
			for _, stats := range viewer.all {
				frames += stats.frames
			}
			viewer.mu.Unlock()
		}
		before := map[*chaosViewer]int{}
		for _, viewer := range healthy {
			viewer.mu.Lock()
			for _, stats := range viewer.all {
				before[viewer] += stats.frames
			}
			viewer.mu.Unlock()
		}
		stopWatch := make(chan struct{})
		watchDone := make(chan struct{})
		go func() {
			defer close(watchDone)
			dumped := false
			for {
				select {
				case <-stopWatch:
					return
				case <-time.After(100 * time.Millisecond):
				}
				for _, viewer := range healthy {
					viewer.mu.Lock()
					var last time.Time
					for _, stats := range viewer.all {
						if stats.lastFrameAt.After(last) {
							last = stats.lastFrameAt
						}
					}
					viewer.mu.Unlock()
					if dir := os.Getenv("NOXA_VIDEO_CHAOS_DUMP"); dir != "" && !dumped && time.Since(last) > 350*time.Millisecond {
						dumped = true
						buf := make([]byte, 64<<20)
						n := runtime.Stack(buf, true)
						_ = os.WriteFile(fmt.Sprintf("%s/freeze-%s-%d.txt", dir, label, cycle), buf[:n], 0o600)
					}
				}
			}
		}()
		sentBefore := e.pub.sent.Load()
		e.pub.maxGap.Store(0)
		if action != nil {
			action()
		}
		time.Sleep(d)
		close(stopWatch)
		<-watchDone
		elapsed := time.Since(from)
		for _, viewer := range healthy {
			viewer.mu.Lock()
			frames, gap := 0, time.Duration(0)
			for _, stats := range viewer.all {
				frames += stats.frames
				gap = max(gap, stats.maxFrameGap)
			}
			viewer.measureFrom = time.Time{}
			viewer.mu.Unlock()
			got := frames - before[viewer]
			// Judge against frames actually produced: an oversubscribed race
			// run starves the synthetic encoder too, which is not an SFU stall.
			want := int(e.pub.sent.Load() - sentBefore - chaosFPS/5)
			allowed := 500*time.Millisecond + time.Duration(e.pub.maxGap.Load())
			report := e.errorf
			if chaosRace {
				// Control operations hold Router.mu across Pion track calls;
				// the race detector stretches those to hundreds of ms on a
				// shared two-core runner. Timing is asserted without -race.
				report = t.Logf
			}
			if got < want*8/10 {
				report("cycle %d %s: healthy viewer %s received %d of %d frames", cycle, label, viewer.id, got, want)
			}
			if gap > allowed {
				report("cycle %d %s: healthy viewer %s froze for %v (allowed %v)", cycle, label, viewer.id, gap, allowed)
			}
		}
		if maxPLIs >= 0 && !chaosRace {
			if n := e.pub.plisSince(from); n > maxPLIs {
				e.errorf("cycle %d %s: publisher received %d keyframe requests, want <= %d", cycle, label, n, maxPLIs)
			}
		}
		t.Logf("cycle %d %s: %v (until %s), publisher PLIs %d", cycle, label, elapsed.Round(time.Millisecond), time.Now().Format("15:04:05.000"), e.pub.plisSince(from))
	}

	// Steady state with ingress chaos and one lossy downlink. Repairs must not
	// turn into keyframe requests for everyone.
	measure("steady", 3*time.Second, 2, nil)

	// A viewer stops reading (application stall), then its link blacks out.
	stalled := e.clientPC(steady[len(steady)-1]).viewer
	measure("stall", 2*time.Second, -1, func() { stalled.stalled.Store(true) })
	stalled.stalled.Store(false)
	measure("blackout", 2*time.Second, -1, func() { stalled.link.setBlackout(true) })
	stalled.link.setBlackout(false)
	recovered := time.Now()
	if !e.waitFor(chaosKeyTimeout, func() bool { return stalled.decodableSince(recovered) }) {
		stalled.mu.Lock()
		missing, last := 0, time.Time{}
		for _, stats := range stalled.all {
			missing += len(stats.missing)
			if stats.lastFrameAt.After(last) {
				last = stats.lastFrameAt
			}
		}
		e.errorf("cycle %d: stalled viewer %s did not recover a decodable stream after the blackout (missing=%d lastFrame=%v keyAt=%v lastPLI=%v plis=%d)", cycle, stalled.id,
			missing, time.Since(last), time.Since(stalled.keyAt), time.Since(stalled.lastPLI), stalled.plis)
		stalled.mu.Unlock()
	}

	// Rapid join/leave/rejoin of extra HEVC viewers.
	measure("churn", 0, -1, func() {
		for round := range 3 {
			for i := range 3 {
				id := fmt.Sprintf("churn%d", i)
				viewer := e.joinViewer(id, true)
				if round == 2 && i == 0 {
					since := time.Now()
					if !e.waitFor(chaosKeyTimeout, func() bool { return viewer.keyframeSince(since.Add(-time.Second)) }) {
						e.errorf("cycle %d: churn viewer %s had no keyframe within %v", cycle, id, chaosKeyTimeout)
					}
				}
			}
			time.Sleep(time.Duration(50+100*round) * time.Millisecond)
			for i := range 3 {
				if viewer := e.leaveViewer(fmt.Sprintf("churn%d", i)); viewer != nil {
					e.checkViewer(viewer, true)
				}
			}
		}
	})

	// A viewer without HEVC switches the single upload to H.264 for everyone.
	switchAt := time.Now()
	legacy := e.joinViewer("legacy", false)
	if !e.waitFor(3*time.Second, func() bool { return e.pub.currentMime() == pion.MimeTypeH264 }) {
		e.errorf("cycle %d: publisher did not switch to H.264 for a non-HEVC viewer", cycle)
	}
	e.expectGeneration("switch to H.264", chaosKeyTimeout)
	t.Logf("cycle %d: H.264 switch reached all viewers after %v", cycle, time.Since(switchAt).Round(time.Millisecond))
	measure("h264", time.Second, -1, nil)
	e.leaveViewer("legacy")
	e.checkViewer(legacy, true)
	if !e.waitFor(3*time.Second, func() bool { return e.pub.currentMime() == pion.MimeTypeH265 }) {
		e.errorf("cycle %d: publisher did not return to H.265", cycle)
	}
	e.expectGeneration("switch back to H.265", chaosKeyTimeout)

	// The publisher reconnects with a new DTLS identity and SSRC.
	generation := e.pub.lastSwitch().generation + 1
	e.pub.close()
	e.unregister(chaosPublisherID)
	e.startPublisher(generation)
	for _, id := range steady {
		e.watch(e.clientPC(id).viewer)
	}
	e.expectGeneration("publisher restart", chaosKeyTimeout)
}

// ingressReport logs the publisher-side dispatch counters once per run.
func (e *chaosEnv) ingressReport() {
	if e.reported.Swap(true) {
		return
	}
	e.router.mu.RLock()
	defer e.router.mu.RUnlock()
	for key, input := range e.router.videoIngress {
		encoded, _ := json.Marshal(input.dispatch.Load().snapshot(time.Now()))
		e.t.Logf("ingress %v dispatch: %s", key, encoded)
	}
	if e.pub != nil {
		e.t.Logf("publisher write failures: %d", e.pub.writeFailures.Load())
	}
}

// After every client left, no per-source state may survive in the router.
func (e *chaosEnv) checkRouterEmpty() {
	r := e.router
	r.mu.RLock()
	defer r.mu.RUnlock()
	sizes := map[string]int{
		"pubTracks": len(r.pubTracks), "pubPeers": len(r.pubPeers), "videoSources": len(r.videoSources),
		"videoSeen": len(r.videoSeen), "videoCodecs": len(r.videoCodecs), "videoIngress": len(r.videoIngress),
		"slotClaims": len(r.slotClaims), "keyframeLast": len(r.keyframeLast), "rtcpWriters": len(r.rtcpWriters),
		"layerPrefs": len(r.layerPrefs), "trackSlots": len(r.trackSlots),
	}
	for name, size := range sizes {
		if size != 0 {
			e.errorf("router retained %d %s entries after every client left", size, name)
		}
	}
	r.watchMu.RLock()
	defer r.watchMu.RUnlock()
	if len(r.watches) != 0 || len(r.publications) != 0 || len(r.watchSessions) != 0 {
		e.errorf("router retained watches=%d publications=%d sessions=%d", len(r.watches), len(r.publications), len(r.watchSessions))
	}
}
