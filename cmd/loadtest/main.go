// loadtest is a headless voicx client simulator for load testing. Each
// simulated client connects over the TCP control channel, authenticates
// (password path with a shared test account), joins a channel, sends global
// chat and pings, and optionally sends UDP pings to exercise the UDP path
// and its rate limiter.
//
// Usage:
//
//	loadtest -addr 127.0.0.1:12333 -clients 50 -duration 30s -ramp 5s \
//	    -unique-id <uid> -password <pw> [-channel 1] [-udp -udp-addr 127.0.0.1:12334] \
//	    [-tls | -tls-fingerprint <sha256> | -tls-insecure]
//
// Authentication uses a single shared account for all simulated clients
// (voicx allows multiple connections per unique ID). Create a test user
// first (there is no protocol-level registration; use psql or an admin
// token flow), e.g. via a one-off Go snippet calling auth.RegisterUser.
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"

	"voicx/internal/config"
	"voicx/internal/netproto"
	"voicx/internal/tlscert"
)

// options holds the load-test parameters.
type options struct {
	addr        string
	udpAddr     string
	clients     int
	duration    time.Duration
	ramp        time.Duration
	uniqueID    string
	password    string
	channel     int64
	udp         bool
	anonymous   bool
	tlsVerify   bool
	tlsPin      string
	tlsInsecure bool
	webrtc      bool
	relayOnly   bool
}

// stats accumulates load-test results.
type stats struct {
	connectsOK      atomic.Int64
	connectsFail    atomic.Int64
	authOK          atomic.Int64
	authFail        atomic.Int64
	sessionFail     atomic.Int64
	chatSent        atomic.Int64
	chatRecv        atomic.Int64
	pongs           atomic.Int64
	webrtcOK        atomic.Int64
	webrtcFail      atomic.Int64
	rtpSent         atomic.Int64
	rtpRecv         atomic.Int64
	rtpReceivers    atomic.Int64
	receiverPackets sync.Map // client index -> *atomic.Int64

	// authLatencyBuckets: <10ms, <50ms, <100ms, <500ms, <1s, >=1s.
	authLatency [6]atomic.Int64
}

func (s *stats) recordReceivedRTP(index int) {
	v, loaded := s.receiverPackets.LoadOrStore(index, &atomic.Int64{})
	if !loaded {
		s.rtpReceivers.Add(1)
	}
	v.(*atomic.Int64).Add(1)
	s.rtpRecv.Add(1)
}

// result fails closed on missing clients as well as explicitly counted errors.
// Successful local RTP writes do not establish delivery through the SFU.
func (s *stats) result(opts options) error {
	want := int64(opts.clients)
	if s.connectsOK.Load() != want || s.connectsFail.Load() != 0 || s.authOK.Load() != want || s.authFail.Load() != 0 {
		return errors.New("not all requested clients connected and authenticated successfully")
	}
	if s.sessionFail.Load() != 0 {
		return errors.New("one or more control sessions failed before the requested duration")
	}
	if opts.webrtc {
		if s.webrtcOK.Load() != want || s.webrtcFail.Load() != 0 || s.rtpSent.Load() == 0 {
			return errors.New("not all requested clients established WebRTC and published RTP successfully")
		}
		if opts.channel != 0 && opts.clients > 1 && s.rtpReceivers.Load() != want {
			return errors.New("RTP reception was not observed on every requested client")
		}
	}
	return nil
}

// bucketLatency records an auth round-trip latency.
func (s *stats) bucketLatency(d time.Duration) {
	var i int
	switch {
	case d < 10*time.Millisecond:
		i = 0
	case d < 50*time.Millisecond:
		i = 1
	case d < 100*time.Millisecond:
		i = 2
	case d < 500*time.Millisecond:
		i = 3
	case d < time.Second:
		i = 4
	default:
		i = 5
	}
	s.authLatency[i].Add(1)
}

// print writes the final report.
func (s *stats) print(opts options) {
	fmt.Println("--- loadtest report ---")
	fmt.Printf("clients=%d duration=%s ramp=%s\n", opts.clients, opts.duration, opts.ramp)
	fmt.Printf("connects: ok=%d fail=%d\n", s.connectsOK.Load(), s.connectsFail.Load())
	fmt.Printf("auth:     ok=%d fail=%d\n", s.authOK.Load(), s.authFail.Load())
	fmt.Printf("sessions: fail=%d\n", s.sessionFail.Load())
	fmt.Printf("chat:     sent=%d received=%d\n", s.chatSent.Load(), s.chatRecv.Load())
	fmt.Printf("pongs:    %d\n", s.pongs.Load())
	if opts.webrtc {
		fmt.Printf("webrtc:  ok=%d fail=%d opus_rtp_sent=%d opus_rtp_received=%d receivers=%d\n", s.webrtcOK.Load(), s.webrtcFail.Load(), s.rtpSent.Load(), s.rtpRecv.Load(), s.rtpReceivers.Load())
		for i := 0; i < opts.clients; i++ {
			var packets int64
			if v, ok := s.receiverPackets.Load(i); ok {
				packets = v.(*atomic.Int64).Load()
			}
			fmt.Printf("receiver client=%d opus_rtp_received=%d\n", i, packets)
		}
	}
	fmt.Printf("auth latency (ms): <10=%d <50=%d <100=%d <500=%d <1000=%d >=1000=%d\n",
		s.authLatency[0].Load(), s.authLatency[1].Load(), s.authLatency[2].Load(),
		s.authLatency[3].Load(), s.authLatency[4].Load(), s.authLatency[5].Load())
}

func main() {
	var opts options
	flag.StringVar(&opts.addr, "addr", "127.0.0.1"+config.DefaultTCPAddr, "control channel address")
	flag.StringVar(&opts.udpAddr, "udp-addr", "127.0.0.1"+config.DefaultUDPAddr, "UDP media address (with -udp)")
	flag.IntVar(&opts.clients, "clients", 10, "number of simulated clients")
	flag.DurationVar(&opts.duration, "duration", 30*time.Second, "load duration")
	flag.DurationVar(&opts.ramp, "ramp", 5*time.Second, "ramp-up period over which clients start")
	flag.StringVar(&opts.uniqueID, "unique-id", "", "account unique ID (shared by all clients)")
	flag.StringVar(&opts.password, "password", "", "account password")
	flag.Int64Var(&opts.channel, "channel", 1, "channel ID to join (0 = don't join)")
	flag.BoolVar(&opts.udp, "udp", false, "also send UDP pings to exercise the UDP path")
	flag.BoolVar(&opts.anonymous, "anonymous", false, "connect as anonymous guests (loadtest-N nicknames; -unique-id/-password not needed)")
	flag.BoolVar(&opts.tlsVerify, "tls", false, "dial with TLS 1.3 and verify the server with system roots")
	flag.StringVar(&opts.tlsPin, "tls-fingerprint", "", "dial with TLS 1.3 and require this SHA-256 certificate fingerprint")
	flag.BoolVar(&opts.tlsInsecure, "tls-insecure", false, "dial with unverified TLS 1.3 (explicit loopback-only test mode)")
	flag.BoolVar(&opts.webrtc, "webrtc", false, "publish a continuous Opus RTP stream from every simulated client")
	flag.BoolVar(&opts.relayOnly, "ice-relay-only", false, "require TURN relay candidates (for the Toxiproxy chaos profile)")
	flag.Parse()

	if _, _, err := controlTLSConfig(opts); err != nil {
		fmt.Fprintf(os.Stderr, "loadtest: %v\n", err)
		os.Exit(2)
	}
	if !opts.anonymous && (opts.uniqueID == "" || opts.password == "") {
		fmt.Fprintln(os.Stderr, "loadtest: -unique-id and -password are required (or use -anonymous)")
		os.Exit(2)
	}
	if opts.clients < 1 {
		fmt.Fprintln(os.Stderr, "loadtest: -clients must be >= 1")
		os.Exit(2)
	}
	if opts.webrtc && opts.clients < 100 {
		fmt.Fprintln(os.Stderr, "loadtest: -webrtc is intended for the 100-client SFU profile; continuing with the requested smaller count")
	}

	var st stats
	err := run(context.Background(), opts, &st)
	st.print(opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "loadtest: %v\n", err)
		os.Exit(1)
	}
}

// run executes the load test: it starts opts.clients simulated clients,
// staggered over the ramp period, waits for the duration, and returns.
func run(ctx context.Context, opts options, st *stats) error {
	ctx, cancel := context.WithTimeout(ctx, opts.duration)
	defer cancel()

	var wg sync.WaitGroup
	for i := 0; i < opts.clients; i++ {
		// Stagger starts over the ramp period.
		if opts.ramp > 0 && i > 0 {
			delay := opts.ramp / time.Duration(opts.clients)
			select {
			case <-ctx.Done():
				wg.Wait()
				return st.result(opts)
			case <-time.After(delay):
			}
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			simulateClient(ctx, opts, st, i)
		}(i)
	}
	wg.Wait()
	return st.result(opts)
}

// loggedFP prints the server fingerprint only on the first TLS dial.
var loggedFP sync.Once

// dialControl dials the control channel in plaintext or in the explicitly
// selected authenticated TLS mode. Unverified TLS is limited to loopback.
func dialControl(opts options) (net.Conn, error) {
	tlsConfig, useTLS, err := controlTLSConfig(opts)
	if err != nil {
		return nil, err
	}
	if !useTLS {
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(context.Background(), "tcp", opts.addr)
	}
	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: 5 * time.Second},
		Config:    tlsConfig,
	}
	conn, err := dialer.DialContext(context.Background(), "tcp", opts.addr)
	if err != nil {
		return nil, err
	}
	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		_ = conn.Close()
		return nil, fmt.Errorf("TLS dial returned a non-TLS connection")
	}
	loggedFP.Do(func() {
		if pc := tlsConn.ConnectionState().PeerCertificates; len(pc) > 0 {
			fmt.Printf("loadtest: server TLS fingerprint: %s\n", tlscert.FingerprintDER(pc[0].Raw))
		}
	})
	return tlsConn, nil
}

func controlTLSConfig(opts options) (*tls.Config, bool, error) {
	modes := 0
	if opts.tlsVerify {
		modes++
	}
	if strings.TrimSpace(opts.tlsPin) != "" {
		modes++
	}
	if opts.tlsInsecure {
		modes++
	}
	if modes > 1 {
		return nil, false, errors.New("choose only one of -tls, -tls-fingerprint, or -tls-insecure")
	}

	switch {
	case strings.TrimSpace(opts.tlsPin) != "":
		cfg, err := pinnedTLSConfig(opts.tlsPin)
		return cfg, true, err
	case opts.tlsInsecure:
		if !isLoopbackEndpoint(opts.addr) {
			return nil, false, fmt.Errorf("-tls-insecure is restricted to loopback addresses, got %q", opts.addr)
		}
		return &tls.Config{
			InsecureSkipVerify: true, // #nosec G402 -- explicitly requested test mode is restricted to loopback.
			MinVersion:         tls.VersionTLS13,
		}, true, nil
	case opts.tlsVerify:
		host, _, err := net.SplitHostPort(opts.addr)
		if err != nil || host == "" {
			return nil, false, fmt.Errorf("TLS address %q must include a host and port", opts.addr)
		}
		return &tls.Config{MinVersion: tls.VersionTLS13, ServerName: host}, true, nil
	default:
		return nil, false, nil
	}
}

func pinnedTLSConfig(fingerprint string) (*tls.Config, error) {
	expected, err := parseTLSFingerprint(fingerprint)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		InsecureSkipVerify: true, // #nosec G402 -- VerifyConnection authenticates the exact certificate fingerprint.
		MinVersion:         tls.VersionTLS13,
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return errors.New("server presented no TLS certificate")
			}
			got := sha256.Sum256(state.PeerCertificates[0].Raw)
			if subtle.ConstantTimeCompare(got[:], expected[:]) != 1 {
				return fmt.Errorf("TLS fingerprint = %s, want %s",
					tlscert.FingerprintDER(state.PeerCertificates[0].Raw), fingerprint)
			}
			return nil
		},
	}, nil
}

func parseTLSFingerprint(value string) ([sha256.Size]byte, error) {
	var fingerprint [sha256.Size]byte
	compact := strings.ReplaceAll(strings.TrimSpace(value), ":", "")
	if len(compact) != hex.EncodedLen(sha256.Size) {
		return fingerprint, fmt.Errorf("TLS fingerprint must contain %d SHA-256 bytes", sha256.Size)
	}
	raw, err := hex.DecodeString(compact)
	if err != nil {
		return fingerprint, fmt.Errorf("decoding TLS fingerprint: %w", err)
	}
	copy(fingerprint[:], raw)
	return fingerprint, nil
}

func isLoopbackEndpoint(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// simulateClient is one simulated client connection lifecycle.
func simulateClient(ctx context.Context, opts options, st *stats, index int) {
	conn, err := dialControl(opts)
	if err != nil {
		st.connectsFail.Add(1)
		return
	}
	defer func() { _ = conn.Close() }()
	conn = &controlConn{Conn: conn}
	st.connectsOK.Add(1)

	// Authenticate: anonymous guest (loadtest-N) or the shared account.
	authMsg := netproto.Authenticate{Username: opts.uniqueID, Password: opts.password}
	if opts.anonymous {
		authMsg = netproto.Authenticate{Anonymous: true, Nickname: fmt.Sprintf("loadtest-%d", index)}
	}
	start := time.Now()
	if err := writeMsg(conn, netproto.MsgAuthenticate, authMsg); err != nil {
		st.authFail.Add(1)
		return
	}
	f, err := readOfType(conn, netproto.MsgAuthResponse, 5*time.Second)
	if err != nil {
		st.authFail.Add(1)
		return
	}
	st.bucketLatency(time.Since(start))
	var resp netproto.AuthResponse
	if err := netproto.Decode(f, &resp); err != nil || !resp.OK {
		st.authFail.Add(1)
		return
	}
	st.authOK.Add(1)

	// Consume the snapshot.
	if _, err := readOfType(conn, netproto.MsgSnapshot, 5*time.Second); err != nil {
		st.sessionFail.Add(1)
		return
	}

	// Request channel membership; receiver evidence verifies media delivery.
	if opts.channel != 0 {
		if err := writeMsg(conn, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: opts.channel}); err != nil {
			st.sessionFail.Add(1)
			return
		}
	}

	var pc *webrtc.PeerConnection
	if opts.webrtc {
		pc, err = startOpusPublisher(conn, resp.ICEServers, opts.relayOnly, ctx, st, index)
		if err != nil {
			st.webrtcFail.Add(1)
			return
		}
		defer func() { _ = pc.Close() }()
	}

	// UDP pings (optional): one packet per second.
	var udpDone chan struct{}
	if opts.udp {
		udpDone = make(chan struct{})
		go udpPinger(opts.udpAddr, udpDone)
		defer close(udpDone)
	}

	// Reader: count incoming pongs and chat events.
	readErr := make(chan error, 1)
	go func() {
		for {
			f, err := netproto.ReadFrame(conn)
			if err != nil {
				readErr <- err
				return
			}
			switch netproto.MessageType(f.Type) {
			case netproto.MsgPong:
				st.pongs.Add(1)
			case netproto.MsgEvent:
				st.chatRecv.Add(1)
			case netproto.MsgPing:
				// Answer server-initiated keepalive pings.
				_ = writeMsg(conn, netproto.MsgPong, netproto.Pong{})
			case netproto.MsgICECandidate:
				if pc != nil {
					var candidate netproto.ICECandidate
					if netproto.Decode(f, &candidate) == nil {
						mid := candidate.SDPMid
						line := uint16(candidate.SDPMLineIndex)
						_ = pc.AddICECandidate(webrtc.ICECandidateInit{Candidate: candidate.Candidate, SDPMid: &mid, SDPMLineIndex: &line})
					}
				}
			case netproto.MsgWebRTCOffer:
				if pc != nil {
					if err := answerRenegotiation(conn, pc, f); err != nil {
						if ctx.Err() == nil {
							st.webrtcFail.Add(1)
						}
						readErr <- err
						return
					}
				}
			}
		}
	}()

	chatTicker := time.NewTicker(2 * time.Second)
	defer chatTicker.Stop()
	pingTicker := time.NewTicker(5 * time.Second)
	defer pingTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-readErr:
			if ctx.Err() == nil {
				st.sessionFail.Add(1)
			}
			return
		case <-chatTicker.C:
			if err := writeMsg(conn, netproto.MsgChatSend, netproto.ChatSend{Text: "loadtest ping"}); err != nil {
				if ctx.Err() == nil {
					st.sessionFail.Add(1)
				}
				return
			}
			st.chatSent.Add(1)
		case <-pingTicker.C:
			if err := writeMsg(conn, netproto.MsgPing, netproto.Ping{}); err != nil {
				if ctx.Err() == nil {
					st.sessionFail.Add(1)
				}
				return
			}
		}
	}
}

// startOpusPublisher creates a real Pion peer and writes a valid Opus silence
// packet every 20ms. Waiting for local ICE gathering avoids a separate client
// trickle writer and makes the 100-client runner deterministic on localhost.
func startOpusPublisher(conn net.Conn, supplied []netproto.ICEServer, relayOnly bool, ctx context.Context, st *stats, index int) (*webrtc.PeerConnection, error) {
	configuration := webrtc.Configuration{}
	if relayOnly {
		configuration.ICETransportPolicy = webrtc.ICETransportPolicyRelay
	}
	for _, server := range supplied {
		configuration.ICEServers = append(configuration.ICEServers, webrtc.ICEServer{
			URLs: server.URLs, Username: server.Username, Credential: server.Credential,
		})
	}
	pc, err := webrtc.NewPeerConnection(configuration)
	if err != nil {
		return nil, err
	}
	pc.OnTrack(func(remote *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		if remote.Kind() != webrtc.RTPCodecTypeAudio {
			return
		}
		for {
			if _, _, err := remote.ReadRTP(); err != nil {
				return
			}
			st.recordReceivedRTP(index)
		}
	})
	var connected atomic.Bool
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateConnected && connected.CompareAndSwap(false, true) {
			st.webrtcOK.Add(1)
		}
		if state == webrtc.PeerConnectionStateFailed && ctx.Err() == nil {
			st.webrtcFail.Add(1)
		}
	})
	track, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{
		MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2,
	}, "load-opus", "voicx-load")
	if err != nil {
		_ = pc.Close()
		return nil, err
	}
	if _, err := pc.AddTrack(track); err != nil {
		_ = pc.Close()
		return nil, err
	}
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		_ = pc.Close()
		return nil, err
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		_ = pc.Close()
		return nil, err
	}
	select {
	case <-ctx.Done():
		_ = pc.Close()
		return nil, ctx.Err()
	case <-time.After(10 * time.Second):
		_ = pc.Close()
		return nil, errors.New("ICE gathering timed out")
	case <-gathered:
	}
	local := pc.LocalDescription()
	if local == nil {
		_ = pc.Close()
		return nil, errors.New("local SDP unavailable")
	}
	if err := writeMsg(conn, netproto.MsgWebRTCOffer, netproto.WebRTCOffer{
		SDP: local.SDP, Tracks: []netproto.TrackSlot{{TrackID: track.ID(), Slot: "mic"}},
	}); err != nil {
		_ = pc.Close()
		return nil, err
	}
	answer, earlyCandidates, err := readWebRTCAnswer(conn, 10*time.Second)
	if err != nil {
		_ = pc.Close()
		return nil, err
	}
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer.SDP}); err != nil {
		_ = pc.Close()
		return nil, err
	}
	for _, candidate := range earlyCandidates {
		mid := candidate.SDPMid
		line := candidate.SDPMLineIndex
		if err := pc.AddICECandidate(webrtc.ICECandidateInit{Candidate: candidate.Candidate, SDPMid: &mid, SDPMLineIndex: &line}); err != nil {
			_ = pc.Close()
			return nil, err
		}
	}
	sequence, timestamp, ssrc, err := readRTPIdentifiers(rand.Reader)
	if err != nil {
		_ = pc.Close()
		return nil, fmt.Errorf("generating RTP identifiers: %w", err)
	}
	go func() {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				packet := &rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: sequence, Timestamp: timestamp, SSRC: ssrc}, Payload: []byte{0xF8, 0xFF, 0xFE}}
				if err := track.WriteRTP(packet); err != nil {
					if ctx.Err() == nil {
						st.webrtcFail.Add(1)
					}
					return
				}
				st.rtpSent.Add(1)
				sequence++
				timestamp += 960
			}
		}
	}()
	return pc, nil
}

func answerRenegotiation(conn net.Conn, pc *webrtc.PeerConnection, f *netproto.Frame) error {
	var offer netproto.WebRTCOffer
	if err := netproto.Decode(f, &offer); err != nil {
		return err
	}
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer.SDP}); err != nil {
		return err
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		return err
	}
	if err := pc.SetLocalDescription(answer); err != nil {
		return err
	}
	return writeMsg(conn, netproto.MsgWebRTCAnswer, netproto.WebRTCAnswer{SDP: pc.LocalDescription().SDP})
}

// Control frames contain separate header and payload writes; serialize the
// complete frame across the signaling reader and periodic traffic writer.
type controlConn struct {
	net.Conn
	writeMu sync.Mutex
}

func readRTPIdentifiers(source io.Reader) (uint16, uint32, uint32, error) {
	var seed [10]byte
	if _, err := io.ReadFull(source, seed[:]); err != nil {
		return 0, 0, 0, err
	}
	return binary.BigEndian.Uint16(seed[0:2]),
		binary.BigEndian.Uint32(seed[2:6]),
		binary.BigEndian.Uint32(seed[6:10]), nil
}

func readWebRTCAnswer(conn net.Conn, timeout time.Duration) (netproto.WebRTCAnswer, []netproto.ICECandidate, error) {
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()
	var candidates []netproto.ICECandidate
	for {
		f, err := netproto.ReadFrame(conn)
		if err != nil {
			return netproto.WebRTCAnswer{}, nil, err
		}
		switch netproto.MessageType(f.Type) {
		case netproto.MsgICECandidate:
			var candidate netproto.ICECandidate
			if err := netproto.Decode(f, &candidate); err == nil {
				candidates = append(candidates, candidate)
			}
		case netproto.MsgWebRTCAnswer:
			var answer netproto.WebRTCAnswer
			if err := netproto.Decode(f, &answer); err != nil {
				return netproto.WebRTCAnswer{}, nil, err
			}
			return answer, candidates, nil
		case netproto.MsgPing:
			_ = writeMsg(conn, netproto.MsgPong, netproto.Pong{})
		}
	}
}

// udpPinger sends one UDP ping per second to addr until done closes.
func udpPinger(addr string, done chan struct{}) {
	raddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return
	}
	conn, err := net.DialUDP("udp", nil, raddr)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			_, _ = conn.Write([]byte{netproto.UDPMsgPing})
		}
	}
}

// writeMsg encodes and writes one control message.
func writeMsg(conn net.Conn, mt netproto.MessageType, msg any) error {
	if c, ok := conn.(*controlConn); ok {
		c.writeMu.Lock()
		defer c.writeMu.Unlock()
	}
	f, err := netproto.Encode(mt, msg)
	if err != nil {
		return err
	}
	return netproto.WriteFrame(conn, f)
}

// readOfType reads frames until one of the wanted type arrives or the
// deadline passes.
func readOfType(conn net.Conn, mt netproto.MessageType, timeout time.Duration) (*netproto.Frame, error) {
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()
	for {
		f, err := netproto.ReadFrame(conn)
		if err != nil {
			return nil, err
		}
		if netproto.MessageType(f.Type) == mt {
			return f, nil
		}
	}
}
