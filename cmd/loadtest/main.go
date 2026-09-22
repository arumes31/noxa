// loadtest is a headless noxa client simulator for load testing. Each
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
// (noxa allows multiple connections per unique ID). Provision a test account
// with cmd/adduser and grant the test capabilities through the role editor.
// Authentication always uses roles-v1 with encrypted, confirmed role traffic.
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
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

	"noxa/internal/config"
	"noxa/internal/netproto"
	"noxa/internal/tlscert"
)

// options holds the load-test parameters.
type options struct {
	addr               string
	udpAddr            string
	clients            int
	duration           time.Duration
	ramp               time.Duration
	uniqueID           string
	password           string
	channel            int64
	udp                bool
	anonymous          bool
	tlsVerify          bool
	tlsPin             string
	tlsInsecure        bool
	webrtc             bool
	relayOnly          bool
	authorizationModel string
	loginGate          chan struct{}
}

// stats accumulates load-test results.
type stats struct {
	connectsOK       atomic.Int64
	connectsFail     atomic.Int64
	authOK           atomic.Int64
	authFail         atomic.Int64
	authFailures     sync.Map // client index -> authFailure; at most one per client
	sessionFail      atomic.Int64
	chatSent         atomic.Int64
	chatRecv         atomic.Int64
	chatConfirmed    sync.Map // client index -> first encrypted self-echo observed
	chatParticipants atomic.Int64
	pongs            atomic.Int64
	webrtcOK         atomic.Int64
	webrtcFail       atomic.Int64
	rtpSent          atomic.Int64
	rtpRecv          atomic.Int64
	rtpReceivers     atomic.Int64
	receiverPackets  sync.Map // client index -> *atomic.Int64

	// authLatencyBuckets: <10ms, <50ms, <100ms, <500ms, <1s, >=1s.
	authLatency [6]atomic.Int64
}

type authFailure struct {
	stage      string
	category   string
	serverCode uint16
	hasCode    bool
}

func (f authFailure) String() string {
	line := fmt.Sprintf("stage=%s category=%s", f.stage, f.category)
	if f.hasCode {
		line += fmt.Sprintf(" server_code=%d", f.serverCode)
	}
	return line
}

// recordAuthFailure retains fixed classifications, never credentials, peer
// messages, decoded payloads, or arbitrary transport error text.
func (s *stats) recordAuthFailure(index int, stage string, err error) {
	failure := authFailure{stage: stage, category: "transport"}
	var serverErr *serverReplyError
	var transportErr net.Error
	switch {
	case errors.As(err, &serverErr):
		failure.category, failure.serverCode, failure.hasCode = "server_error", serverErr.code, true
	case stage == "decode" || errors.Is(err, errMalformedServerReply):
		failure.category = "malformed_response"
	case stage == "rejected":
		failure.category = "rejected"
	case errors.Is(err, errAuthorizationModel):
		failure.category = "authorization_model_mismatch"
	case errors.As(err, &transportErr) && transportErr.Timeout():
		failure.category = "timeout"
	}
	if _, loaded := s.authFailures.LoadOrStore(index, failure); !loaded {
		s.authFail.Add(1)
	}
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
	if selectedAuthorizationModel(opts) == netproto.AuthorizationModelRolesV1 && s.chatParticipants.Load() != want {
		return errors.New("encrypted chat acceptance was not observed for every requested client")
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
	fmt.Printf("clients=%d duration=%s ramp=%s authorization_model=%s\n", opts.clients, opts.duration, opts.ramp, opts.authorizationModel)
	fmt.Printf("connects: ok=%d fail=%d\n", s.connectsOK.Load(), s.connectsFail.Load())
	fmt.Printf("auth:     ok=%d fail=%d\n", s.authOK.Load(), s.authFail.Load())
	modelMismatch := false
	for i := 0; i < opts.clients; i++ {
		if failure, ok := s.authFailures.Load(i); ok {
			fmt.Printf("auth_failure client=%d %s\n", i, failure.(authFailure))
			modelMismatch = modelMismatch || failure.(authFailure).category == "authorization_model_mismatch"
		}
	}
	if modelMismatch {
		fmt.Println("the server must support authorization_model=roles-v1")
	}
	fmt.Printf("sessions: fail=%d\n", s.sessionFail.Load())
	fmt.Printf("chat:     sent=%d received=%d\n", s.chatSent.Load(), s.chatRecv.Load())
	if selectedAuthorizationModel(opts) == netproto.AuthorizationModelRolesV1 {
		fmt.Printf("chat:     confirmed_clients=%d\n", s.chatParticipants.Load())
	}
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
	flag.StringVar(&opts.authorizationModel, "authorization-model", netproto.AuthorizationModelRolesV1, "required server authorization model (roles-v1)")
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
	if err := validateAuthorizationModel(opts.authorizationModel); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, opts.duration)
	defer cancel()
	// Account clients share an IP and principal. Serialize only authentication
	// so their in-flight verifications respect the server's login limiter.
	if !opts.anonymous {
		opts.loginGate = make(chan struct{}, 1)
	}

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
		cfg, err := fingerprintVerifiedTLSConfig(opts.tlsPin)
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

// fingerprintVerifiedTLSConfig replaces CA/hostname verification with an exact
// certificate pin. VerifyConnection enforces the pin on every TLS handshake,
// including resumed sessions. The explicit verification name also lets CodeQL
// distinguish this custom trust policy from accidentally disabled verification.
func fingerprintVerifiedTLSConfig(fingerprint string) (*tls.Config, error) {
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
	if err := validateAuthorizationModel(opts.authorizationModel); err != nil {
		st.recordAuthFailure(index, "model", errAuthorizationModel)
		return
	}
	model := selectedAuthorizationModel(opts)
	var chat *loadChat
	if model == netproto.AuthorizationModelRolesV1 {
		var err error
		chat, err = newLoadChat()
		if err != nil {
			st.recordAuthFailure(index, "keys", err)
			return
		}
	}
	// Do not open waiting sockets: the server's authentication deadline starts
	// when it accepts a connection, before this client's turn in the gate.
	var releaseLogin func()
	if opts.loginGate != nil {
		select {
		case opts.loginGate <- struct{}{}:
		case <-ctx.Done():
			st.recordAuthFailure(index, "wait", ctx.Err())
			return
		}
		var released sync.Once
		releaseLogin = func() { released.Do(func() { <-opts.loginGate }) }
		defer releaseLogin()
	}
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
	if chat != nil {
		authMsg.AuthorizationModels = []string{model}
		authMsg.X25519PublicKey = base64.StdEncoding.EncodeToString(chat.public[:])
	}
	start := time.Now()
	if err := writeMsg(conn, netproto.MsgAuthenticate, authMsg); err != nil {
		st.recordAuthFailure(index, "write", err)
		return
	}
	f, err := readOfType(conn, netproto.MsgAuthResponse, 5*time.Second)
	if err != nil {
		st.recordAuthFailure(index, "read", err)
		return
	}
	st.bucketLatency(time.Since(start))
	var resp netproto.AuthResponse
	if err := netproto.Decode(f, &resp); err != nil {
		st.recordAuthFailure(index, "decode", err)
		return
	}
	if !resp.OK {
		if resp.AuthorizationModel != "" && resp.AuthorizationModel != model {
			st.recordAuthFailure(index, "model", errAuthorizationModel)
			return
		}
		st.recordAuthFailure(index, "rejected", nil)
		return
	}
	if resp.AuthorizationModel != model {
		st.recordAuthFailure(index, "model", errAuthorizationModel)
		return
	}
	st.authOK.Add(1)
	if releaseLogin != nil {
		releaseLogin()
	}
	if chat != nil {
		for _, key := range resp.ChatKeys {
			if err := chat.install(key); err != nil {
				st.sessionFail.Add(1)
				return
			}
		}
	}

	// Consume the snapshot.
	if _, err := readOfType(conn, netproto.MsgSnapshot, 5*time.Second, chat.observe); err != nil {
		st.sessionFail.Add(1)
		return
	}

	// Request channel membership; receiver evidence verifies media delivery.
	if opts.channel != 0 {
		if err := writeMsg(conn, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: opts.channel}); err != nil {
			st.sessionFail.Add(1)
			return
		}
		if chat != nil {
			if err := awaitRoleJoin(conn, chat, resp.ClientID, opts.channel); err != nil {
				st.sessionFail.Add(1)
				return
			}
		}
	}

	var pc *webrtc.PeerConnection
	if opts.webrtc {
		pc, err = startOpusPublisher(conn, resp.ICEServers, opts.relayOnly, ctx, st, index, chat.observe)
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
	readTraffic := func() error {
		for {
			f, err := netproto.ReadFrame(conn)
			if err != nil {
				return err
			}
			switch netproto.MessageType(f.Type) {
			case netproto.MsgPong:
				st.pongs.Add(1)
			case netproto.MsgEvent:
				if chat != nil {
					received, confirmed, err := chat.receive(f, resp.ClientID)
					if err != nil {
						return err
					}
					if received {
						st.chatRecv.Add(1)
					}
					if confirmed {
						if _, loaded := st.chatConfirmed.LoadOrStore(index, true); !loaded {
							st.chatParticipants.Add(1)
						}
					}
				} else {
					var event struct {
						Type string `json:"type"`
					}
					if json.Unmarshal(f.Payload, &event) == nil && event.Type == "chat" {
						st.chatRecv.Add(1)
					}
				}
			case netproto.MsgError:
				return errControlRejected
			case netproto.MsgChannelKey:
				if chat != nil {
					var key netproto.ChannelKey
					if err := netproto.Decode(f, &key); err != nil {
						return err
					}
					if err := chat.install(key); err != nil {
						return err
					}
				}
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
						return err
					}
				}
			}
		}
	}
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		err := readTraffic()
		if err != nil && (ctx.Err() == nil || errors.Is(err, errControlRejected)) {
			st.sessionFail.Add(1)
		}
		readErr <- err
	}()
	defer func() { _ = conn.Close(); <-readerDone }()
	if chat != nil {
		message, err := chat.message()
		if err != nil {
			st.sessionFail.Add(1)
			return
		}
		if err := writeMsg(conn, netproto.MsgChatSend, message); err != nil {
			st.sessionFail.Add(1)
			return
		}
		st.chatSent.Add(1)
	}

	chatTicker := time.NewTicker(2 * time.Second)
	defer chatTicker.Stop()
	pingTicker := time.NewTicker(5 * time.Second)
	defer pingTicker.Stop()
	var chatSequence uint64

	for {
		select {
		case <-ctx.Done():
			return
		case <-readErr:
			return
		case <-chatTicker.C:
			chatSequence++
			message := netproto.ChatSend{Text: fmt.Sprintf("loadtest ping %d %d", index, chatSequence)}
			if chat != nil {
				var err error
				message, err = chat.message()
				if err != nil {
					st.sessionFail.Add(1)
					return
				}
			}
			if err := writeMsg(conn, netproto.MsgChatSend, message); err != nil {
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
func startOpusPublisher(conn net.Conn, supplied []netproto.ICEServer, relayOnly bool, ctx context.Context, st *stats, index int, observers ...func(*netproto.Frame) error) (*webrtc.PeerConnection, error) {
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
	}, "load-opus", "noxa-load")
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
	answer, earlyCandidates, earlyOffer, err := readWebRTCAnswer(conn, 10*time.Second, observers...)
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
	// The server can publish a renegotiation offer before the initial answer
	// reaches this reader. Apply it only after the first exchange is stable.
	if earlyOffer != nil {
		if err := answerRenegotiation(conn, pc, earlyOffer); err != nil {
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

func readWebRTCAnswer(conn net.Conn, timeout time.Duration, observers ...func(*netproto.Frame) error) (netproto.WebRTCAnswer, []netproto.ICECandidate, *netproto.Frame, error) {
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()
	var candidates []netproto.ICECandidate
	var earlyOffer *netproto.Frame
	for {
		f, err := netproto.ReadFrame(conn)
		if err != nil {
			return netproto.WebRTCAnswer{}, nil, nil, err
		}
		for _, observe := range observers {
			if err := observe(f); err != nil {
				return netproto.WebRTCAnswer{}, nil, nil, err
			}
		}
		switch netproto.MessageType(f.Type) {
		case netproto.MsgError:
			return netproto.WebRTCAnswer{}, nil, nil, errors.New("server rejected media setup")
		case netproto.MsgWebRTCOffer:
			// A peer can have only one unanswered local offer. Bound buffering
			// even if a broken server sends repeated offers without an answer.
			if earlyOffer != nil {
				return netproto.WebRTCAnswer{}, nil, nil, errors.New("multiple WebRTC offers before the initial answer")
			}
			earlyOffer = f
		case netproto.MsgICECandidate:
			var candidate netproto.ICECandidate
			if err := netproto.Decode(f, &candidate); err == nil {
				candidates = append(candidates, candidate)
			}
		case netproto.MsgWebRTCAnswer:
			var answer netproto.WebRTCAnswer
			if err := netproto.Decode(f, &answer); err != nil {
				return netproto.WebRTCAnswer{}, nil, nil, err
			}
			return answer, candidates, earlyOffer, nil
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

type serverReplyError struct {
	code uint16
}

func (e *serverReplyError) Error() string {
	return fmt.Sprintf("server rejected request (code=%d)", e.code)
}

var errMalformedServerReply = errors.New("malformed server error frame")

// readOfType reads frames until the requested type, a server rejection, or
// the deadline. Requesting MsgError explicitly still returns its raw frame.
func readOfType(conn net.Conn, mt netproto.MessageType, timeout time.Duration, observers ...func(*netproto.Frame) error) (*netproto.Frame, error) {
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()
	for {
		f, err := netproto.ReadFrame(conn)
		if err != nil {
			return nil, err
		}
		for _, observe := range observers {
			if err := observe(f); err != nil {
				return nil, err
			}
		}
		if netproto.MessageType(f.Type) == mt {
			return f, nil
		}
		if netproto.MessageType(f.Type) == netproto.MsgError {
			var reply netproto.Error
			if err := netproto.Decode(f, &reply); err != nil {
				return nil, errMalformedServerReply
			}
			return nil, &serverReplyError{code: reply.Code}
		}
	}
}
