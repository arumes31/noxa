// Package nativesender publishes natively encoded H.264 screen frames over a
// second, publish-only pion PeerConnection (peer "native"). Frames are put in
// the canonical access-unit form, encrypted with the channel media frame
// cipher (framecrypt, byte-identical to the webview's LiveKit transform) and
// packetized with the real SSRC/RTP timestamp that the cipher's IV binds.
package nativesender

import (
	"bytes"
	"errors"
	"sync"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/cc"
	"github.com/pion/interceptor/pkg/gcc"
	"github.com/pion/interceptor/pkg/stats"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4"

	"noxa/internal/framecrypt"
	"noxa/internal/netproto"
)

const (
	// TrackID is declared as slot "screen" in every native offer.
	TrackID    = "noxa-native-screen"
	streamID   = "noxa-native"
	mtu        = 1200
	clockRate  = 90000
	h264FMTP   = "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42001f"
	minBitrate = 300_000
	// AudioTrackID is declared as slot "screenaudio" when the share has audio.
	AudioTrackID = "noxa-native-screenaudio"
	audioRate    = 48000
	audioFrame   = 960 // 20 ms
	opusFMTP     = "minptime=10;useinbandfec=1;stereo=1;sprop-stereo=1"
)

// nextAudioTimestamp keeps 20 ms packets contiguous (last + 960) while
// following the capture clock: when capture resumes after a gap (nothing
// played, a pause) it jumps to the capture-time anchor, never backwards.
func nextAudioTimestamp(sentAny bool, last, anchor uint32) uint32 {
	if !sentAny {
		return anchor
	}
	expected := last + audioFrame
	if ahead := int32(anchor - expected); ahead > 2*audioFrame { // #nosec G115 -- serial number arithmetic
		return anchor
	}
	return expected
}

// Signaling carries the native peer's SDP and ICE over the control
// connection. The server never offers to the native peer: the client offers,
// the server answers; candidates flow both ways tagged Peer = "native".
type Signaling interface {
	Offer(netproto.WebRTCOffer) (netproto.WebRTCAnswer, error)
	Candidate(netproto.ICECandidate) error
}

// Config configures one sender.
type Config struct {
	ICEServers []webrtc.ICEServer
	Signaling  Signaling
	Keys       *framecrypt.Encryptor
	// AudioKeys ("self|screenaudio") adds the Opus screenaudio track; nil = video only.
	AudioKeys *framecrypt.Encryptor
	// OnKeyframeRequest runs on PLI/FIR from the SFU (and after a key gap).
	OnKeyframeRequest func()
	// OnEstimate receives the send-side bandwidth estimate (bits/s).
	OnEstimate     func(bps int)
	InitialBitrate int
	MaxBitrate     int
	// SettingEngine lets tests restrict ICE to loopback; zero value is fine.
	SettingEngine *webrtc.SettingEngine
}

// Stats are cumulative sender counters plus transport snapshots.
type Stats struct {
	SSRC                                     uint32
	FramesSent, KeyframesSent, FramesDropped uint64
	AudioSSRC                                uint32
	AudioFramesSent, AudioFramesDropped      uint64
	PacketsSent, BytesSent                   uint64
	RetransmittedPackets, RetransmittedBytes uint64
	RTT                                      time.Duration // 0 = unknown
	FractionLost                             float64
	HasRemote                                bool
	EstimateBPS                              int
}

// Sender is one native publish-only peer.
type Sender struct {
	cfg   Config
	pc    *webrtc.PeerConnection
	track *webrtc.TrackLocalStaticRTP
	rtp   *webrtc.RTPSender
	ssrc  uint32

	mu          sync.Mutex
	payloader   codecs.H264Payloader
	seq         uint16
	start       time.Time
	lastTS      uint32
	sentAny     bool
	sps, pps    []byte
	seqHeader   []byte
	keyGap      bool // frames were dropped for lack of a key: resume on a keyframe
	stats       Stats
	estimator   cc.BandwidthEstimator
	statsGetter stats.Getter
	rtxSSRC     uint32
	estimatorCh chan cc.BandwidthEstimator
	closed      bool

	audioTrack   *webrtc.TrackLocalStaticRTP
	audioSSRC    uint32
	audioSeq     uint16
	audioLastTS  uint32
	audioSentAny bool
}

// New creates the peer, adds the screen track and negotiates it.
func New(cfg Config) (*Sender, error) {
	if cfg.Signaling == nil || cfg.Keys == nil {
		return nil, errors.New("native sender needs signaling and keys")
	}
	if cfg.MaxBitrate <= 0 {
		cfg.MaxBitrate = 8_000_000
	}
	if cfg.InitialBitrate <= 0 {
		cfg.InitialBitrate = min(2_500_000, cfg.MaxBitrate)
	}
	media := &webrtc.MediaEngine{}
	feedback := []webrtc.RTCPFeedback{{Type: "goog-remb"}, {Type: "transport-cc"}, {Type: "ccm", Parameter: "fir"}, {Type: "nack"}, {Type: "nack", Parameter: "pli"}}
	if err := media.RegisterCodec(webrtc.RTPCodecParameters{RTPCodecCapability: webrtc.RTPCodecCapability{
		MimeType: webrtc.MimeTypeH264, ClockRate: clockRate, SDPFmtpLine: h264FMTP, RTCPFeedback: feedback}, PayloadType: 102}, webrtc.RTPCodecTypeVideo); err != nil {
		return nil, err
	}
	if err := media.RegisterCodec(webrtc.RTPCodecParameters{RTPCodecCapability: webrtc.RTPCodecCapability{
		MimeType: webrtc.MimeTypeRTX, ClockRate: clockRate, SDPFmtpLine: "apt=102"}, PayloadType: 103}, webrtc.RTPCodecTypeVideo); err != nil {
		return nil, err
	}
	opus := webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: audioRate, Channels: 2, SDPFmtpLine: opusFMTP}
	if err := media.RegisterCodec(webrtc.RTPCodecParameters{RTPCodecCapability: opus, PayloadType: 111}, webrtc.RTPCodecTypeAudio); err != nil {
		return nil, err
	}
	registry := &interceptor.Registry{}
	s := &Sender{cfg: cfg, start: time.Now(), estimatorCh: make(chan cc.BandwidthEstimator, 1)}
	congestion, err := cc.NewInterceptor(func() (cc.BandwidthEstimator, error) {
		return gcc.NewSendSideBWE(gcc.SendSideBWEInitialBitrate(cfg.InitialBitrate),
			gcc.SendSideBWEMinBitrate(minBitrate), gcc.SendSideBWEMaxBitrate(cfg.MaxBitrate))
	})
	if err != nil {
		return nil, err
	}
	congestion.OnNewPeerConnection(func(_ string, estimator cc.BandwidthEstimator) { s.estimatorCh <- estimator })
	registry.Add(congestion)
	// pion's PeerConnection.GetStats has no outbound RTP stats: read the
	// stats interceptor directly (media SSRC, RTX SSRC for retransmissions).
	statsFactory, err := stats.NewInterceptor()
	if err != nil {
		return nil, err
	}
	statsFactory.OnNewPeerConnection(func(_ string, getter stats.Getter) { s.statsGetter = getter })
	registry.Add(statsFactory)
	for _, configure := range []func() error{
		func() error { return webrtc.ConfigureNack(media, registry) },
		func() error { return webrtc.ConfigureRTCPReports(registry) },
		func() error { return webrtc.ConfigureTWCCHeaderExtensionSender(media, registry) },
	} {
		if err := configure(); err != nil {
			return nil, err
		}
	}
	options := []func(*webrtc.API){webrtc.WithMediaEngine(media), webrtc.WithInterceptorRegistry(registry)}
	if cfg.SettingEngine != nil {
		options = append(options, webrtc.WithSettingEngine(*cfg.SettingEngine))
	}
	pc, err := webrtc.NewAPI(options...).NewPeerConnection(webrtc.Configuration{ICEServers: cfg.ICEServers, BundlePolicy: webrtc.BundlePolicyMaxBundle})
	if err != nil {
		return nil, err
	}
	s.pc = pc
	select {
	case s.estimator = <-s.estimatorCh:
		s.estimator.OnTargetBitrateChange(func(bps int) {
			if s.cfg.OnEstimate != nil {
				s.cfg.OnEstimate(bps)
			}
		})
	default:
	}
	if s.track, err = webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: clockRate, SDPFmtpLine: h264FMTP}, TrackID, streamID); err != nil {
		_ = pc.Close()
		return nil, err
	}
	transceiver, err := pc.AddTransceiverFromTrack(s.track, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendonly})
	if err != nil {
		_ = pc.Close()
		return nil, err
	}
	s.rtp = transceiver.Sender()
	if encodings := s.rtp.GetParameters().Encodings; len(encodings) > 0 {
		s.ssrc, s.rtxSSRC = uint32(encodings[0].SSRC), uint32(encodings[0].RTX.SSRC)
	}
	s.stats.SSRC = s.ssrc
	go s.readRTCP()
	if cfg.AudioKeys != nil {
		// Same stream as the video: one capture clock for both RTP timelines.
		if s.audioTrack, err = webrtc.NewTrackLocalStaticRTP(opus, AudioTrackID, streamID); err != nil {
			_ = pc.Close()
			return nil, err
		}
		audio, err := pc.AddTransceiverFromTrack(s.audioTrack, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendonly})
		if err != nil {
			_ = pc.Close()
			return nil, err
		}
		if encodings := audio.Sender().GetParameters().Encodings; len(encodings) > 0 {
			s.audioSSRC = uint32(encodings[0].SSRC)
		}
		s.stats.AudioSSRC = s.audioSSRC
		go func() { // drain RTCP so the interceptors keep running
			for {
				if _, _, err := audio.Sender().ReadRTCP(); err != nil {
					return
				}
			}
		}()
	}
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		init := c.ToJSON()
		candidate := netproto.ICECandidate{Candidate: init.Candidate, Peer: netproto.PeerNative}
		if init.SDPMid != nil {
			candidate.SDPMid = *init.SDPMid
		}
		if init.SDPMLineIndex != nil {
			candidate.SDPMLineIndex = *init.SDPMLineIndex
		}
		_ = cfg.Signaling.Candidate(candidate)
	})
	if err := s.negotiate(); err != nil {
		_ = pc.Close()
		return nil, err
	}
	return s, nil
}

func (s *Sender) negotiate() error {
	offer, err := s.pc.CreateOffer(nil)
	if err != nil {
		return err
	}
	if err := s.pc.SetLocalDescription(offer); err != nil {
		return err
	}
	tracks := []netproto.TrackSlot{{TrackID: TrackID, Slot: "screen"}}
	if s.audioTrack != nil {
		tracks = append(tracks, netproto.TrackSlot{TrackID: AudioTrackID, Slot: "screenaudio"})
	}
	answer, err := s.cfg.Signaling.Offer(netproto.WebRTCOffer{SDP: offer.SDP, Peer: netproto.PeerNative,
		Tracks: tracks, MediaE2EE: netproto.MediaE2EEVersion})
	if err != nil {
		return err
	}
	return s.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer.SDP})
}

// AddCandidate adds a remote (server) ICE candidate.
func (s *Sender) AddCandidate(c netproto.ICECandidate) error {
	mid, index := c.SDPMid, c.SDPMLineIndex
	return s.pc.AddICECandidate(webrtc.ICECandidateInit{Candidate: c.Candidate, SDPMid: &mid, SDPMLineIndex: &index})
}

// ConnectionState exposes the peer state for fallback decisions.
func (s *Sender) ConnectionState() webrtc.PeerConnectionState { return s.pc.ConnectionState() }

// OnConnectionStateChange registers a state observer.
func (s *Sender) OnConnectionStateChange(f func(webrtc.PeerConnectionState)) {
	s.pc.OnConnectionStateChange(f)
}

func (s *Sender) readRTCP() {
	for {
		packets, _, err := s.rtp.ReadRTCP()
		if err != nil {
			return
		}
		for _, p := range packets {
			switch p.(type) {
			case *rtcp.PictureLossIndication, *rtcp.FullIntraRequest:
				if s.cfg.OnKeyframeRequest != nil {
					s.cfg.OnKeyframeRequest()
				}
			}
		}
	}
}

// ErrDropped reports a frame withheld to keep the stream decodable (key gap,
// missing parameter sets) or because no media key is active (fail closed).
var ErrDropped = errors.New("native frame dropped")

// WriteFrame canonicalizes, encrypts and sends one encoder access unit.
func (s *Sender) WriteFrame(au []byte, keyframe bool, seqHeader []byte, captured time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("native sender closed")
	}
	if len(seqHeader) > 0 && !bytes.Equal(seqHeader, s.seqHeader) {
		// A reopened encoder (new size/bitrate) brings new parameter sets.
		s.seqHeader = bytes.Clone(seqHeader)
		for _, nal := range framecrypt.SplitAnnexB(seqHeader) {
			switch {
			case len(nal) > 0 && nal[0]&31 == 7:
				s.sps = nal
			case len(nal) > 0 && nal[0]&31 == 8:
				s.pps = nal
			}
		}
	}
	canonical, key, err := framecrypt.CanonicalH264(au, s.sps, s.pps)
	if err != nil {
		s.stats.FramesDropped++
		return err
	}
	if key {
		// Remember in-band parameter sets for later IDRs that omit them.
		if nals := framecrypt.SplitAnnexB(canonical); len(nals) == 3 {
			s.sps, s.pps = nals[0], nals[1]
		}
	}
	if s.keyGap && !key {
		s.stats.FramesDropped++
		s.requestKeyframe()
		return ErrDropped
	}
	ts := uint32(captured.Sub(s.start).Seconds() * clockRate)
	if s.sentAny && (ts == s.lastTS || ts-s.lastTS >= 1<<31) { // not newer (serial number arithmetic)
		ts = s.lastTS + 1 // The IV and replay window need strictly increasing timestamps.
	}
	encrypted, err := s.cfg.Keys.EncryptH264(canonical, s.ssrc, ts)
	if err != nil {
		s.stats.FramesDropped++
		if !s.keyGap {
			s.keyGap = true
		}
		return errors.Join(ErrDropped, err)
	}
	s.keyGap = false
	payloads := s.payloader.Payload(mtu, encrypted)
	for i, payload := range payloads {
		s.seq++
		packet := &rtp.Packet{Header: rtp.Header{Version: 2, Marker: i == len(payloads)-1, SequenceNumber: s.seq, Timestamp: ts, SSRC: s.ssrc}, Payload: payload}
		if err := s.track.WriteRTP(packet); err != nil {
			return err
		}
	}
	s.lastTS, s.sentAny = ts, true
	s.stats.FramesSent++
	if key {
		s.stats.KeyframesSent++
	}
	return nil
}

// WriteAudio encrypts and sends one 20 ms Opus packet whose first sample was
// captured at captured (the video capture clock). A packet without an active
// "self|screenaudio" key is dropped (ErrDropped), never sent in the clear.
func (s *Sender) WriteAudio(packet []byte, captured time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("native sender closed")
	}
	if s.audioTrack == nil {
		return errors.New("native sender has no audio track")
	}
	ts := nextAudioTimestamp(s.audioSentAny, s.audioLastTS, uint32(int64(captured.Sub(s.start).Seconds()*audioRate))) // #nosec G115 -- RTP timestamps wrap
	encrypted, err := s.cfg.AudioKeys.EncryptOpus(packet, s.audioSSRC, ts)
	if err != nil {
		s.stats.AudioFramesDropped++
		return errors.Join(ErrDropped, err)
	}
	s.audioSeq++
	if err := s.audioTrack.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, Marker: !s.audioSentAny, SequenceNumber: s.audioSeq, Timestamp: ts, SSRC: s.audioSSRC}, Payload: encrypted}); err != nil {
		return err
	}
	s.audioLastTS, s.audioSentAny = ts, true
	s.stats.AudioFramesSent++
	return nil
}

func (s *Sender) requestKeyframe() {
	if s.cfg.OnKeyframeRequest != nil {
		go s.cfg.OnKeyframeRequest()
	}
}

// Stats returns counters and the latest transport snapshot.
func (s *Sender) Stats() Stats {
	s.mu.Lock()
	out := s.stats
	estimator := s.estimator
	s.mu.Unlock()
	if estimator != nil {
		out.EstimateBPS = estimator.GetTargetBitrate()
	}
	if s.statsGetter == nil {
		return out
	}
	if st := s.statsGetter.Get(s.ssrc); st != nil {
		out.PacketsSent, out.BytesSent = st.OutboundRTPStreamStats.PacketsSent, st.OutboundRTPStreamStats.BytesSent
		if st.RemoteInboundRTPStreamStats.PacketsReceived > 0 || st.RemoteInboundRTPStreamStats.RoundTripTime > 0 {
			out.HasRemote = true
			out.RTT, out.FractionLost = st.RemoteInboundRTPStreamStats.RoundTripTime, st.FractionLost
		}
	}
	if s.rtxSSRC != 0 {
		if st := s.statsGetter.Get(s.rtxSSRC); st != nil {
			out.RetransmittedPackets, out.RetransmittedBytes = st.OutboundRTPStreamStats.PacketsSent, st.OutboundRTPStreamStats.BytesSent
			out.PacketsSent += out.RetransmittedPackets
			out.BytesSent += out.RetransmittedBytes
		}
	}
	return out
}

// Close tears the peer down.
func (s *Sender) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return s.pc.Close()
}
