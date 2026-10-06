// Package connectionbenchmark measures a private synthetic RTP round trip through
// the existing server media transport. It never captures or plays audio.
package connectionbenchmark

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/pion/rtp"
	pion "github.com/pion/webrtc/v4"
	"golang.org/x/crypto/nacl/box"
	"noxa/internal/broadcast"
	"noxa/internal/netproto"
	"noxa/internal/version"
)

// Options are supplied natively from an already authenticated server tab.
// Fingerprint is the certificate pin of that live connection, not a new TOFU pin.
type Options struct{ Address, Fingerprint, ServerPassword string }

// Progress deliberately exposes no addresses, credentials or session identifiers.
type Progress struct {
	Phase          string `json:"phase"`
	ElapsedSeconds int    `json:"elapsed_seconds"`
}

type control struct {
	net.Conn
	mu     sync.Mutex
	queued []*netproto.Frame
	guard  func(*netproto.Frame) error
}

func (c *control) write(kind netproto.MessageType, value any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	f, err := netproto.Encode(kind, value)
	if err != nil {
		return err
	}
	return netproto.WriteFrame(c.Conn, f)
}
func (c *control) read(want netproto.MessageType) (*netproto.Frame, error) {
	for {
		frame, err := netproto.ReadFrame(c)
		if err != nil {
			return nil, err
		}
		if c.guard != nil {
			if err := c.guard(frame); err != nil {
				return nil, err
			}
		}
		switch netproto.MessageType(frame.Type) {
		case netproto.MsgError, netproto.MsgAuthChallenge:
			return nil, errors.New("benchmark guest operation was rejected")
		case netproto.MsgPing:
			if err := c.write(netproto.MsgPong, netproto.Pong{}); err != nil {
				return nil, err
			}
		default:
			if netproto.MessageType(frame.Type) == want {
				return frame, nil
			}
			if want == netproto.MsgWebRTCAnswer && netproto.MessageType(frame.Type) == netproto.MsgICECandidate {
				if len(c.queued) >= 64 {
					return nil, errors.New("too many pending media candidates")
				}
				c.queued = append(c.queued, frame)
			}
		}
	}
}

func trustedTLS(pin string) (*tls.Config, error) {
	want, err := hex.DecodeString(strings.ReplaceAll(pin, ":", ""))
	if err != nil || len(want) != sha256.Size {
		return nil, errors.New("an existing trusted TLS connection is required")
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, InsecureSkipVerify: true, // TOFU: exact existing certificate pin replaces CA validation.
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return errors.New("server certificate absent")
			}
			actual := sha256.Sum256(state.PeerCertificates[0].Raw)
			if subtle.ConstantTimeCompare(actual[:], want) != 1 {
				return errors.New("server certificate does not match the trusted connection")
			}
			return nil
		},
	}, nil
}

// Run emits 20 seconds of synthetic Opus packets, followed by a two-second drain.
// Unreturned packets include anything arriving after that drain; this is not a
// definitive network-loss or acoustic-quality test. All work ends within 45 s.
func Run(parent context.Context, options Options, progress func(Progress)) (Result, error) {
	return run(parent, options, progress, 20*time.Second, 2*time.Second)
}

func run(parent context.Context, options Options, progress func(Progress), duration, drain time.Duration) (Result, error) {
	config, err := trustedTLS(options.Fingerprint)
	if err != nil {
		return Result{}, err
	}
	ctx, cancel := context.WithTimeout(parent, 45*time.Second)
	defer cancel()
	report := func(phase string, seconds int) {
		if progress != nil {
			progress(Progress{Phase: phase, ElapsedSeconds: seconds})
		}
	}
	report("connecting", 0)
	dialer := tls.Dialer{NetDialer: &net.Dialer{Timeout: 5 * time.Second}, Config: config}
	conn, err := dialer.DialContext(ctx, "tcp", options.Address)
	if err != nil {
		return Result{}, fmt.Errorf("benchmark connection failed: %w", err)
	}
	c := &control{Conn: conn}
	defer func() { _ = c.Close() }()
	stopClose := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stopClose()
	if err := c.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
		return Result{}, err
	}
	auth, info, err := authenticate(c, options.ServerPassword)
	if err != nil {
		return Result{}, err
	}
	if err := joinEcho(c, auth.ClientID, info.EchoChannelID); err != nil {
		return Result{}, err
	}
	// Membership can change during ICE/SDP negotiation. Check every control
	// frame immediately after the acknowledged private join, including frames
	// read while waiting for the first SDP answer.
	c.guard = func(frame *netproto.Frame) error { return validateEchoFrame(frame, auth.ClientID, info.EchoChannelID) }
	pcConfig := pion.Configuration{}
	for _, ice := range auth.ICEServers {
		pcConfig.ICEServers = append(pcConfig.ICEServers, pion.ICEServer{URLs: ice.URLs, Username: ice.Username, Credential: ice.Credential})
	}
	pc, err := pion.NewPeerConnection(pcConfig)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = pc.Close() }()
	stopPeer := context.AfterFunc(ctx, func() { _ = pc.Close() })
	defer stopPeer()
	track, err := pion.NewTrackLocalStaticRTP(pion.RTPCodecCapability{MimeType: pion.MimeTypeOpus, ClockRate: 48000, Channels: 2}, "connection-benchmark", "synthetic-self-echo")
	if err != nil {
		return Result{}, err
	}
	sender, err := pc.AddTrack(track)
	if err != nil {
		return Result{}, err
	}
	measure := newMeasurement()
	connected := make(chan struct{})
	var once sync.Once
	pc.OnConnectionStateChange(func(state pion.PeerConnectionState) {
		if state == pion.PeerConnectionStateConnected {
			once.Do(func() { close(connected) })
		}
		if state == pion.PeerConnectionStateFailed {
			cancel()
		}
	})
	pc.OnTrack(func(remote *pion.TrackRemote, _ *pion.RTPReceiver) {
		if remote.Kind() != pion.RTPCodecTypeAudio || (remote.ID() != auth.ClientID && remote.ID() != auth.ClientID+"|mic") {
			cancel()
			return
		}
		for {
			packet, _, err := remote.ReadRTP()
			if err != nil {
				return
			}
			measure.receivedPacket(packet.SequenceNumber, packet.Timestamp, time.Now())
		}
	})
	// Draining RTCP is required for interceptor feedback; shutdown closes the reader.
	rtcpDone := make(chan struct{})
	go func() {
		defer close(rtcpDone)
		for {
			if _, _, err := sender.ReadRTCP(); err != nil {
				return
			}
		}
	}()
	defer func() { _ = pc.Close(); <-rtcpDone }()
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		return Result{}, err
	}
	gathered := pion.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		return Result{}, err
	}
	select {
	case <-ctx.Done():
		return Result{}, ctx.Err()
	case <-gathered:
	}
	if err := c.write(netproto.MsgWebRTCOffer, netproto.WebRTCOffer{SDP: pc.LocalDescription().SDP, Tracks: []netproto.TrackSlot{{TrackID: track.ID(), Slot: "mic"}}}); err != nil {
		return Result{}, err
	}
	frame, err := c.read(netproto.MsgWebRTCAnswer)
	if err != nil {
		return Result{}, err
	}
	var answer netproto.WebRTCAnswer
	if err := netproto.Decode(frame, &answer); err != nil {
		return Result{}, err
	}
	if err := pc.SetRemoteDescription(pion.SessionDescription{Type: pion.SDPTypeAnswer, SDP: answer.SDP}); err != nil {
		return Result{}, err
	}
	if err := c.SetDeadline(time.Now().Add(40 * time.Second)); err != nil {
		return Result{}, err
	}
	readerDone := make(chan struct{})
	privateReady := make(chan struct{})
	go func() {
		defer close(readerDone)
		defer cancel()
		_ = readSignals(c, pc, auth.ClientID, info.EchoChannelID, privateReady)
	}()
	defer func() { _ = c.Close(); <-readerDone }()
	select {
	case <-ctx.Done():
		return Result{}, ctx.Err()
	case <-connected:
	}
	// A control round trip after ICE establishment ensures the signal reader
	// has validated membership events queued during negotiation before RTP starts.
	if err := c.write(netproto.MsgServerInfoQuery, netproto.ServerInfoQuery{}); err != nil {
		return Result{}, err
	}
	select {
	case <-ctx.Done():
		return Result{}, ctx.Err()
	case <-privateReady:
	}
	report("running", 0)
	if err := sendPackets(ctx, track, measure, duration, report); err != nil {
		return Result{}, err
	}
	report("draining", int(duration.Seconds()))
	timer := time.NewTimer(drain)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return Result{}, ctx.Err()
	case <-timer.C:
	}
	result := measure.summary()
	result.ServerVersion = info.Version
	result.DurationSeconds = int(duration.Seconds())
	result.DrainSeconds = int(drain.Seconds())
	if transport := sender.Transport(); transport != nil {
		if pair, err := transport.ICETransport().GetSelectedCandidatePair(); err == nil && pair != nil {
			result.Protocol = pair.Remote.Protocol.String()
			result.CandidateType = pair.Remote.Typ.String()
		}
	}
	return result, nil
}

func authenticate(c *control, serverPassword string) (netproto.AuthResponse, netproto.ServerInfoResponse, error) {
	var auth netproto.AuthResponse
	var info netproto.ServerInfoResponse
	public, _, err := box.GenerateKey(rand.Reader)
	if err != nil {
		return auth, info, err
	}
	nonce := make([]byte, 4)
	if _, err := rand.Read(nonce); err != nil {
		return auth, info, err
	}
	request := netproto.Authenticate{Anonymous: true, Nickname: "ConnectionTest-" + hex.EncodeToString(nonce), ServerPassword: serverPassword,
		AuthorizationModels: []string{netproto.AuthorizationModelRolesV1}, X25519PublicKey: base64.StdEncoding.EncodeToString(public[:]), ClientVersion: version.Short()}
	if err := c.write(netproto.MsgAuthenticate, request); err != nil {
		return auth, info, err
	}
	frame, err := c.read(netproto.MsgAuthResponse)
	if err != nil {
		return auth, info, err
	}
	if netproto.Decode(frame, &auth) != nil || !auth.OK || auth.ClientID == "" || auth.AuthorizationModel != netproto.AuthorizationModelRolesV1 {
		return auth, info, errors.New("temporary benchmark guests are unavailable on this server")
	}
	if err := c.write(netproto.MsgServerInfoQuery, netproto.ServerInfoQuery{}); err != nil {
		return auth, info, err
	}
	frame, err = c.read(netproto.MsgServerInfoResponse)
	if err != nil {
		return auth, info, err
	}
	if netproto.Decode(frame, &info) != nil || !info.EchoPrivate || info.EchoChannelID <= 0 {
		return auth, info, errors.New("private Echo Test is unavailable; no media was sent")
	}
	return auth, info, nil
}

func joinEcho(c *control, client string, channel int64) error {
	if err := c.write(netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: channel, AckRequested: true}); err != nil {
		return err
	}
	frame, err := c.read(netproto.MsgChannelJoined)
	if err != nil {
		return err
	}
	var joined netproto.ChannelJoined
	if netproto.Decode(frame, &joined) != nil || joined.ClientID != client || joined.ChannelID != channel {
		return errors.New("private Echo Test membership was not confirmed")
	}
	return nil
}

func ownEcho(nodes []*broadcast.ChannelNode, client string, channel int64) bool {
	for _, node := range nodes {
		if node == nil {
			continue
		}
		for _, member := range node.Clients {
			if member != nil && member.ClientID == client {
				return node.ChannelID == channel && member.ChannelID == channel
			}
		}
		if ownEcho(node.Children, client, channel) {
			return true
		}
	}
	return false
}

func readSignals(c *control, pc *pion.PeerConnection, client string, channel int64, privateReady chan struct{}) error {
	var confirmed sync.Once
	for {
		var frame *netproto.Frame
		var err error
		if len(c.queued) > 0 {
			frame = c.queued[0]
			c.queued = c.queued[1:]
		} else {
			frame, err = netproto.ReadFrame(c)
		}
		if err != nil {
			return err
		}
		if err := validateEchoFrame(frame, client, channel); err != nil {
			return err
		}
		switch netproto.MessageType(frame.Type) {
		case netproto.MsgServerInfoResponse:
			var info netproto.ServerInfoResponse
			if netproto.Decode(frame, &info) != nil || !info.EchoPrivate || info.EchoChannelID != channel {
				return errors.New("private Echo Test changed during negotiation")
			}
			confirmed.Do(func() { close(privateReady) })
		case netproto.MsgPing:
			if err := c.write(netproto.MsgPong, netproto.Pong{}); err != nil {
				return err
			}
		case netproto.MsgError:
			return errors.New("server stopped benchmark")
		case netproto.MsgICECandidate:
			var candidate netproto.ICECandidate
			if netproto.Decode(frame, &candidate) != nil {
				return errors.New("invalid media candidate")
			}
			line := candidate.SDPMLineIndex
			if err := pc.AddICECandidate(pion.ICECandidateInit{Candidate: candidate.Candidate, SDPMid: &candidate.SDPMid, SDPMLineIndex: &line}); err != nil {
				return err
			}
		case netproto.MsgWebRTCOffer:
			var offer netproto.WebRTCOffer
			if netproto.Decode(frame, &offer) != nil {
				return errors.New("invalid media offer")
			}
			if err := pc.SetRemoteDescription(pion.SessionDescription{Type: pion.SDPTypeOffer, SDP: offer.SDP}); err != nil {
				return err
			}
			answer, err := pc.CreateAnswer(nil)
			if err != nil {
				return err
			}
			if err := pc.SetLocalDescription(answer); err != nil {
				return err
			}
			if err := c.write(netproto.MsgWebRTCAnswer, netproto.WebRTCAnswer{SDP: pc.LocalDescription().SDP}); err != nil {
				return err
			}
		}
	}
}

func validateEchoFrame(frame *netproto.Frame, client string, channel int64) error {
	switch netproto.MessageType(frame.Type) {
	case netproto.MsgSnapshot:
		var snapshot broadcast.TreeSnapshot
		if netproto.Decode(frame, &snapshot) != nil || !ownEcho(snapshot.RootChannels, client, channel) {
			return errors.New("benchmark no longer in private Echo Test")
		}
	case netproto.MsgEvent:
		var event struct {
			Type string `json:"type"`
			Data struct {
				ClientID   string  `json:"client_id"`
				ChannelID  int64   `json:"channel_id"`
				ChannelIDs []int64 `json:"channel_ids"`
			} `json:"data"`
		}
		if json.Unmarshal(frame.Payload, &event) != nil {
			return errors.New("invalid membership event")
		}
		if (event.Type == "user_moved" && event.Data.ClientID == client && event.Data.ChannelID != channel) ||
			(event.Type == "kicked" && event.Data.ClientID == client) {
			return errors.New("benchmark left private Echo Test")
		}
		if event.Type == "channel_deleted" {
			if event.Data.ChannelID == channel {
				return errors.New("private Echo Test was removed")
			}
			for _, deleted := range event.Data.ChannelIDs {
				if deleted == channel {
					return errors.New("private Echo Test was removed")
				}
			}
		}
	}
	return nil
}

func sendPackets(ctx context.Context, track *pion.TrackLocalStaticRTP, m *measurement, duration time.Duration, progress func(string, int)) error {
	base := time.Now()
	due := base
	end := base.Add(duration)
	sequence := uint16(1)
	timestamp := uint32(960)
	timer := time.NewTimer(0)
	defer timer.Stop()
	lastProgress := -1
	for due.Before(end) {
		timer.Reset(max(0, time.Until(due)))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
		now := time.Now()
		if err := ctx.Err(); err != nil {
			return err
		}
		if !now.Before(end) {
			break
		}
		m.sentPacket(sequence, timestamp, now, now.Sub(due))
		if err := track.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: sequence, Timestamp: timestamp}, Payload: []byte{0xf8, 0xff, 0xfe}}); err != nil {
			return err
		}
		seconds := int(now.Sub(base).Seconds())
		if seconds != lastProgress {
			progress("running", seconds)
			lastProgress = seconds
		}
		sequence++
		timestamp += 960
		// Do not catch up a stalled sender by emitting a misleading packet burst.
		due = due.Add(20 * time.Millisecond)
		if due.Before(now) {
			due = now.Add(20 * time.Millisecond)
		}
	}
	return nil
}
