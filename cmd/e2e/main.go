// e2e is a live-server end-to-end checklist runner for noxa. It connects to
// a running server and exercises the health, UDP, auth, channel, chat,
// permission, file-transfer, and ServerQuery paths, printing PASS/FAIL per
// check and exiting non-zero unless everything passes.
//
// Usage:
//
//	e2e -addr 127.0.0.1:12333 -alice-uid <uid> -alice-pass <pw> \
//	    -bob-uid <uid> -bob-pass <pw> -admin-uid <uid> -admin-pass <pw> \
//	    [-tls | -tls-fingerprint <sha256> | -tls-insecure]
//
// The *-uid flags take the unique IDs printed by cmd/adduser. All endpoints
// default to localhost with the standard ports.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/nacl/box"
	"golang.org/x/crypto/nacl/secretbox"

	"noxa/internal/config"
	"noxa/internal/netproto"
	"noxa/internal/tlscert"
)

// options holds the e2e parameters.
type options struct {
	authorizationModel string
	addr               string
	queryAddr          string
	healthURL          string
	udpAddr            string
	fileAddr           string
	aliceUID           string
	alicePass          string
	aliceNickname      string
	bobUID             string
	bobPass            string
	adminUID           string
	adminPass          string
	serverPass         string
	filePayload        int64
	tlsVerify          bool
	tlsPin             string
	tlsInsecure        bool
	chaos              bool
	chaosStopCmd       string
	chaosStartCmd      string
}

// file-transfer frame types (mirror internal/filetransfer, unexported).
const (
	ftInit   uint16 = 1
	ftChunk  uint16 = 2
	ftDigest uint16 = 3
	ftStatus uint16 = 4
)

const readTimeout = 5 * time.Second

type controlTLSMode struct {
	verify   bool
	pin      string
	insecure bool
}

var e2eTLSMode controlTLSMode

// loggedFP prints the server fingerprint only on the first TLS dial.
var loggedFP sync.Once

// dialTCP dials the control channel in plaintext or in the explicitly selected
// authenticated TLS mode. Unverified TLS is limited to loopback.
func dialTCP(addr string) (net.Conn, error) {
	tlsConfig, useTLS, err := controlTLSConfig(addr, e2eTLSMode)
	if err != nil {
		return nil, err
	}
	if !useTLS {
		return (&net.Dialer{Timeout: readTimeout}).DialContext(context.Background(), "tcp", addr)
	}
	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: readTimeout},
		Config:    tlsConfig,
	}
	conn, err := dialer.DialContext(context.Background(), "tcp", addr)
	if err != nil {
		return nil, err
	}
	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		_ = conn.Close()
		return nil, errors.New("TLS dial returned a non-TLS connection")
	}
	loggedFP.Do(func() {
		if pc := tlsConn.ConnectionState().PeerCertificates; len(pc) > 0 {
			fmt.Printf("e2e: server TLS fingerprint: %s\n", tlscert.FingerprintDER(pc[0].Raw))
		}
	})
	return tlsConn, nil
}

func controlTLSConfig(addr string, mode controlTLSMode) (*tls.Config, bool, error) {
	modes := 0
	if mode.verify {
		modes++
	}
	if strings.TrimSpace(mode.pin) != "" {
		modes++
	}
	if mode.insecure {
		modes++
	}
	if modes > 1 {
		return nil, false, errors.New("choose only one of -tls, -tls-fingerprint, or -tls-insecure")
	}

	switch {
	case strings.TrimSpace(mode.pin) != "":
		cfg, err := fingerprintVerifiedTLSConfig(mode.pin)
		return cfg, true, err
	case mode.insecure:
		if !isLoopbackEndpoint(addr) {
			return nil, false, fmt.Errorf("-tls-insecure is restricted to loopback addresses, got %q", addr)
		}
		return &tls.Config{
			InsecureSkipVerify: true, // #nosec G402 -- explicitly requested test mode is restricted to loopback.
			MinVersion:         tls.VersionTLS13,
		}, true, nil
	case mode.verify:
		host, _, err := net.SplitHostPort(addr)
		if err != nil || host == "" {
			return nil, false, fmt.Errorf("TLS address %q must include a host and port", addr)
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

// client is one control-channel connection.
type client struct {
	conn     net.Conn
	uid      string
	clientID string
	nickname string

	// E2EE chat (wave 4b): own X25519 pair + per-scope chat keys captured
	// from MsgChannelKey frames.
	e2ePub      [32]byte
	e2ePriv     [32]byte
	scopeKeys   map[int64]map[uint32][32]byte
	scopeLatest map[int64]uint32
}

// eventEnvelope mirrors the broadcaster's {"type","data"} shape.
type eventEnvelope struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// clientsByConn registers live clients so the frame readers can capture
// MsgChannelKey frames opportunistically.
var clientsByConn sync.Map

// initClientKeys generates the client's X25519 pair BEFORE authenticating, so
// the public half can ride along on Authenticate and the server can seal the
// global generation and the MOTD straight into the AuthResponse (133).
func initClientKeys(c *client) error {
	c.scopeKeys = map[int64]map[uint32][32]byte{}
	c.scopeLatest = map[int64]uint32{}
	pub, priv, err := box.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	c.e2ePub, c.e2ePriv = *pub, *priv
	return nil
}

// registerClient stores the connection association and publishes the client's
// X25519 public key into the server directory (DM peers resolve it there).
func registerClient(c *client) error {
	clientsByConn.Store(c.conn, c)
	return writeMsg(c.conn, netproto.MsgKeyPublish, netproto.KeyPublish{
		PublicKey: base64.StdEncoding.EncodeToString(c.e2ePub[:]),
	})
}

// unsealScopeKey opens one sealed generation with the client's private key.
func unsealScopeKey(c *client, ck netproto.ChannelKey) ([32]byte, bool) {
	var out [32]byte
	raw, err := base64.StdEncoding.DecodeString(ck.SealedKey)
	if err != nil {
		return out, false
	}
	key, ok := box.OpenAnonymous(nil, raw, &c.e2ePub, &c.e2ePriv)
	if !ok || len(key) != 32 {
		return out, false
	}
	copy(out[:], key)
	return out, true
}

// installScopeKeys unseals a bundle of generations for a scope. current says
// whether they are the scope's live generation (the AuthResponse) or archival
// ones piggybacked on a history/pins page, which must not move scopeLatest.
func installScopeKeys(c *client, scope int64, keys []netproto.ChannelKey, current bool) {
	for _, ck := range keys {
		k, ok := unsealScopeKey(c, ck)
		if !ok {
			continue
		}
		if c.scopeKeys[scope] == nil {
			c.scopeKeys[scope] = map[uint32][32]byte{}
		}
		c.scopeKeys[scope][ck.KeyID] = k
		if current && ck.KeyID > c.scopeLatest[scope] {
			c.scopeLatest[scope] = ck.KeyID
		}
	}
}

// historyBody opens one history entry. It mirrors the real client: the server
// never fills Body, so a non-empty Body on the wire is a protocol violation.
func historyBody(c *client, scope int64, m netproto.ChatHistoryEntry) (string, error) {
	if m.Body != "" {
		return "", fmt.Errorf("server sent PLAINTEXT history body for message %d", m.ID)
	}
	if m.BodyEnc == "" {
		return "", nil
	}
	key, ok := c.scopeKeys[scope][m.KeyID]
	if !ok {
		return "", fmt.Errorf("no key for generation %d", m.KeyID)
	}
	return e2eOpenScope(m.BodyEnc, key)
}

// captureChannelKey unseals and stores a scope key frame for the connection's
// client. Called by the frame readers for every MsgChannelKey frame.
func captureChannelKey(conn net.Conn, f *netproto.Frame) {
	v, ok := clientsByConn.Load(conn)
	if !ok {
		return
	}
	c := v.(*client)
	var ck netproto.ChannelKey
	if err := netproto.Decode(f, &ck); err != nil {
		return
	}
	raw, err := base64.StdEncoding.DecodeString(ck.SealedKey)
	if err != nil {
		return
	}
	key, ok := box.OpenAnonymous(nil, raw, &c.e2ePub, &c.e2ePriv)
	if !ok || len(key) != 32 {
		return
	}
	var k [32]byte
	copy(k[:], key)
	if c.scopeKeys[ck.ChannelID] == nil {
		c.scopeKeys[ck.ChannelID] = map[uint32][32]byte{}
	}
	c.scopeKeys[ck.ChannelID][ck.KeyID] = k
	c.scopeLatest[ck.ChannelID] = ck.KeyID
}

// awaitScopeKey reads frames until the client holds a key for the scope.
func awaitScopeKey(conn net.Conn, c *client, scope int64) error {
	deadline := time.Now().Add(readTimeout)
	for time.Now().Before(deadline) {
		if _, ok := c.scopeLatest[scope]; ok {
			return nil
		}
		if _, err := readOfType(conn, netproto.MsgChannelKey, time.Until(deadline)); err != nil {
			return fmt.Errorf("no chat key for scope %d: %w", scope, err)
		}
	}
	return fmt.Errorf("no chat key for scope %d", scope)
}

// fetchPub resolves a user's X25519 public key from the server directory.
func fetchPub(conn net.Conn, uid string) ([32]byte, error) {
	var out [32]byte
	if err := writeMsg(conn, netproto.MsgKeyRequest, netproto.KeyRequest{UniqueID: uid}); err != nil {
		return out, err
	}
	f, err := readOfType(conn, netproto.MsgKeyResponse, readTimeout)
	if err != nil {
		return out, err
	}
	var resp netproto.KeyResponse
	if err := netproto.Decode(f, &resp); err != nil {
		return out, err
	}
	if resp.PublicKey == "" {
		return out, fmt.Errorf("no public key published for %s", uid)
	}
	raw, err := base64.StdEncoding.DecodeString(resp.PublicKey)
	if err != nil || len(raw) != 32 {
		return out, fmt.Errorf("invalid public key for %s", uid)
	}
	copy(out[:], raw)
	return out, nil
}

// e2eSealScope encrypts a channel/global message with the scope key.
func e2eSealScope(text string, key [32]byte) (string, error) {
	var nonce [24]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(append(nonce[:], secretbox.Seal(nil, []byte(text), &nonce, &key)...)), nil
}

// e2eOpenScope decrypts a channel/global message with the scope key.
func e2eOpenScope(blobB64 string, key [32]byte) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(blobB64)
	if err != nil || len(raw) < 24 {
		return "", errors.New("invalid scope ciphertext")
	}
	var nonce [24]byte
	copy(nonce[:], raw[:24])
	plain, ok := secretbox.Open(nil, raw[24:], &nonce, &key)
	if !ok {
		return "", errors.New("scope open failed")
	}
	return string(plain), nil
}

// e2eSealDM encrypts a direct message for the recipient's public key.
func e2eSealDM(text string, recipientPub, senderPriv [32]byte) (string, error) {
	var nonce [24]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(append(nonce[:], box.Seal(nil, []byte(text), &nonce, &recipientPub, &senderPriv)...)), nil
}

// e2eOpenDM decrypts a direct message with the sender's public key.
func e2eOpenDM(blobB64 string, senderPub, recipientPriv [32]byte) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(blobB64)
	if err != nil || len(raw) < 24 {
		return "", errors.New("invalid DM ciphertext")
	}
	var nonce [24]byte
	copy(nonce[:], raw[:24])
	plain, ok := box.Open(nil, raw[24:], &nonce, &senderPub, &recipientPriv)
	if !ok {
		return "", errors.New("DM open failed")
	}
	return string(plain), nil
}

func main() {
	var o options
	flag.StringVar(&o.authorizationModel, "authorization-model", netproto.AuthorizationModelRolesV1, "required authorization model (roles-v1)")
	flag.StringVar(&o.addr, "addr", "127.0.0.1"+config.DefaultTCPAddr, "control channel address")
	flag.StringVar(&o.queryAddr, "query-addr", config.DefaultQueryAddr, "ServerQuery address")
	flag.StringVar(&o.healthURL, "health-url", "http://127.0.0.1"+config.DefaultHealthAddr, "health endpoint base URL")
	flag.StringVar(&o.udpAddr, "udp-addr", "127.0.0.1"+config.DefaultUDPAddr, "UDP media address")
	flag.StringVar(&o.fileAddr, "file-addr", "127.0.0.1"+config.DefaultFileAddr, "file-transfer address (fallback; the port from the init response wins)")
	flag.StringVar(&o.aliceUID, "alice-uid", "", "alice's unique ID")
	flag.StringVar(&o.alicePass, "alice-pass", "", "alice's password")
	flag.StringVar(&o.aliceNickname, "alice-nick", "alice", "alice's account nickname (nickname-login check)")
	flag.StringVar(&o.bobUID, "bob-uid", "", "bob's unique ID")
	flag.StringVar(&o.bobPass, "bob-pass", "", "bob's password")
	flag.StringVar(&o.adminUID, "admin-uid", "", "admin's unique ID (ServerQuery)")
	flag.StringVar(&o.adminPass, "admin-pass", "", "admin's password")
	flag.StringVar(&o.serverPass, "server-password", "", "global server password (if set)")
	flag.Int64Var(&o.filePayload, "file-payload", 64*1024, "upload test payload size in bytes")
	flag.BoolVar(&o.tlsVerify, "tls", false, "dial the control channel with TLS 1.3 and verify the server with system roots")
	flag.StringVar(&o.tlsPin, "tls-fingerprint", "", "dial the control channel with TLS 1.3 and require this SHA-256 certificate fingerprint")
	flag.BoolVar(&o.tlsInsecure, "tls-insecure", false, "dial with unverified TLS 1.3 (explicit loopback-only test mode)")
	flag.BoolVar(&o.chaos, "chaos", false, "additionally run the database chaos drill (467): stops PostgreSQL mid-traffic and verifies recovery (needs Docker; disruptive)")
	flag.StringVar(&o.chaosStopCmd, "chaos-stop-cmd", "docker compose stop postgres", "command that takes the database down (split on whitespace, run without a shell)")
	flag.StringVar(&o.chaosStartCmd, "chaos-start-cmd", "docker compose start postgres", "command that brings the database back up (split on whitespace, run without a shell)")
	flag.Parse()
	e2eTLSMode = controlTLSMode{verify: o.tlsVerify, pin: o.tlsPin, insecure: o.tlsInsecure}
	if _, _, err := controlTLSConfig(o.addr, e2eTLSMode); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: %v\n", err)
		os.Exit(2)
	}

	for _, req := range []struct{ name, val string }{
		{"alice-uid", o.aliceUID}, {"alice-pass", o.alicePass},
		{"bob-uid", o.bobUID}, {"bob-pass", o.bobPass},
		{"admin-uid", o.adminUID}, {"admin-pass", o.adminPass},
	} {
		if req.val == "" {
			fmt.Fprintf(os.Stderr, "e2e: -%s is required\n", req.name)
			os.Exit(2)
		}
	}

	os.Exit(runChecks(o))
}

// runChecks executes the checklist and returns the process exit code.
func runChecks(o options) int {
	if err := validateE2EProfile(o); err != nil {
		fmt.Printf("FAIL options: %v\n", err)
		return 2
	}
	return runRoleChecks(o)
}

// checkCtx carries shared state between checks.
type checkCtx struct {
	opts      options
	alice     *client
	bob       *client
	guest     *client
	channelID int64
}

func (c *checkCtx) close() {
	if c.alice != nil {
		clientsByConn.Delete(c.alice.conn)
		_ = c.alice.conn.Close()
	}
	if c.bob != nil {
		clientsByConn.Delete(c.bob.conn)
		_ = c.bob.conn.Close()
	}
	if c.guest != nil {
		clientsByConn.Delete(c.guest.conn)
		_ = c.guest.conn.Close()
	}
}

type check struct {
	name string
	run  func(*checkCtx) error
}

// ---------------------------------------------------------------------------
// Health
// ---------------------------------------------------------------------------

func httpGet(url string) (int, string, error) {
	client := &http.Client{Timeout: readTimeout}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		return 0, "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer closeE2EResource(resp.Body)
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, string(body), err
}

func checkHealthz(c *checkCtx) error {
	code, _, err := httpGet(c.opts.healthURL + "/healthz")
	if err != nil {
		return err
	}
	if code != 200 {
		return fmt.Errorf("healthz status = %d, want 200", code)
	}
	return nil
}

func checkReadyz(c *checkCtx) error {
	code, _, err := httpGet(c.opts.healthURL + "/readyz")
	if err != nil {
		return err
	}
	if code != 200 {
		return fmt.Errorf("readyz status = %d, want 200", code)
	}
	return nil
}

func checkMetrics(c *checkCtx) error {
	code, body, err := httpGet(c.opts.healthURL + "/metrics")
	if err != nil {
		return err
	}
	if code != 200 {
		return fmt.Errorf("metrics status = %d, want 200", code)
	}
	if !strings.Contains(body, "noxa_clients_connected") {
		return errors.New("metrics body missing noxa_clients_connected")
	}
	return nil
}

// ---------------------------------------------------------------------------
// UDP
// ---------------------------------------------------------------------------

func checkUDP(c *checkCtx) error {
	raddr, err := net.ResolveUDPAddr("udp", c.opts.udpAddr)
	if err != nil {
		return err
	}
	conn, err := net.DialUDP("udp", nil, raddr)
	if err != nil {
		return err
	}
	defer closeE2EResource(conn)

	if _, err := conn.Write([]byte{netproto.UDPMsgPing}); err != nil {
		return err
	}
	_ = conn.SetReadDeadline(time.Now().Add(readTimeout))
	buf := make([]byte, 64)
	n, _, err := conn.ReadFromUDP(buf)
	if err != nil {
		return err
	}
	if n < 1 || buf[0] != netproto.UDPMsgPong {
		return fmt.Errorf("reply = %x, want pong", buf[:n])
	}
	return nil
}

// ---------------------------------------------------------------------------
// Auth & protocol helpers
// ---------------------------------------------------------------------------

func dialAuth(addr, uid, password, serverPassword string, models ...string) (*client, error) {
	model, err := e2eAuthorizationModel(models)
	if err != nil {
		return nil, err
	}
	conn, err := dialTCP(addr)
	if err != nil {
		return nil, err
	}
	c := &client{conn: conn, uid: uid}
	if err := initClientKeys(c); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := writeMsg(conn, netproto.MsgAuthenticate, netproto.Authenticate{
		Username:            uid,
		Password:            password,
		ServerPassword:      serverPassword,
		X25519PublicKey:     base64.StdEncoding.EncodeToString(c.e2ePub[:]),
		AuthorizationModels: e2eAdvertisedModels(model),
	}); err != nil {
		_ = conn.Close()
		return nil, err
	}
	f, err := readOfType(conn, netproto.MsgAuthResponse, readTimeout)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	var resp netproto.AuthResponse
	if err := netproto.Decode(f, &resp); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if !resp.OK {
		_ = conn.Close()
		return nil, errors.New("auth rejected: " + resp.Reason)
	}
	if err := e2eCheckAuthorizationModel(resp, model); err != nil {
		_ = conn.Close()
		return nil, err
	}
	// The global generation rides along with the response; it is the CURRENT
	// one, so it may advance scopeLatest.
	installScopeKeys(c, 0, resp.ChatKeys, true)
	clientsByConn.Store(conn, c)
	if _, err := readOfType(conn, netproto.MsgSnapshot, readTimeout); err != nil {
		clientsByConn.Delete(conn)
		_ = conn.Close()
		return nil, fmt.Errorf("reading snapshot: %w", err)
	}
	c.clientID, c.nickname, c.uid = resp.ClientID, resp.Nickname, resp.UniqueID
	if err := registerClient(c); err != nil {
		clientsByConn.Delete(conn)
		_ = conn.Close()
		return nil, fmt.Errorf("e2e key publish: %w", err)
	}
	return c, nil
}

func writeMsg(conn net.Conn, mt netproto.MessageType, msg any) error {
	f, err := netproto.Encode(mt, msg)
	if err != nil {
		return err
	}
	return netproto.WriteFrame(conn, f)
}

func reportE2ECleanupError(action string, err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: cleanup %s: %v\n", action, err)
	}
}

func closeE2EResource(closer io.Closer) {
	reportE2ECleanupError("close failed", closer.Close())
}

func clearE2EReadDeadline(conn net.Conn) {
	reportE2ECleanupError("clear read deadline failed", conn.SetReadDeadline(time.Time{}))
}

func readOfType(conn net.Conn, mt netproto.MessageType, timeout time.Duration) (*netproto.Frame, error) {
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	defer clearE2EReadDeadline(conn)
	for {
		f, err := netproto.ReadFrame(conn)
		if err != nil {
			return nil, err
		}
		// Answer server-initiated keepalive pings while waiting.
		if netproto.MessageType(f.Type) == netproto.MsgPing {
			_ = writeMsg(conn, netproto.MsgPong, netproto.Pong{})
			continue
		}
		if netproto.MessageType(f.Type) == netproto.MsgChannelKey {
			captureChannelKey(conn, f)
		}
		if netproto.MessageType(f.Type) == mt {
			return f, nil
		}
		if netproto.MessageType(f.Type) == netproto.MsgError {
			var reply netproto.Error
			if err := netproto.Decode(f, &reply); err != nil {
				return nil, errors.New("malformed server error")
			}
			return nil, fmt.Errorf("server rejected E2E request (code=%d)", reply.Code)
		}
	}
}

// readEvent reads MsgEvent frames until the wanted envelope type arrives. A
// server error frame aborts the wait immediately (surfaces the real cause
// instead of a bare timeout).
func readEvent(conn net.Conn, want string, timeout time.Duration) (*eventEnvelope, error) {
	defer clearE2EReadDeadline(conn)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(deadline)
		f, err := netproto.ReadFrame(conn)
		if err != nil {
			return nil, err
		}
		// Answer server-initiated keepalive pings while waiting.
		if netproto.MessageType(f.Type) == netproto.MsgPing {
			_ = writeMsg(conn, netproto.MsgPong, netproto.Pong{})
			continue
		}
		if netproto.MessageType(f.Type) == netproto.MsgError {
			var e netproto.Error
			if err := netproto.Decode(f, &e); err == nil {
				return nil, fmt.Errorf("server error %d: %s (while waiting for %q)", e.Code, e.Message, want)
			}
			continue
		}
		if netproto.MessageType(f.Type) == netproto.MsgChannelKey {
			captureChannelKey(conn, f)
			continue
		}
		if netproto.MessageType(f.Type) != netproto.MsgEvent {
			continue
		}
		var env eventEnvelope
		if err := json.Unmarshal(f.Payload, &env); err != nil {
			return nil, err
		}
		if env.Type == want {
			return &env, nil
		}
	}
	return nil, fmt.Errorf("no %q event within %s", want, timeout)
}

type querySession struct {
	conn net.Conn
	r    *lineReader
}

// lineReader wraps a conn for line-based reads with a deadline.
type lineReader struct {
	conn      net.Conn
	buf       []byte
	readBytes uint64
}

func (l *lineReader) readLine(timeout time.Duration) (string, error) {
	_ = l.conn.SetReadDeadline(time.Now().Add(timeout))
	defer clearE2EReadDeadline(l.conn)
	one := make([]byte, 1)
	for {
		n, err := l.conn.Read(one)
		if err != nil {
			return "", err
		}
		if n <= 0 {
			continue
		}
		l.readBytes += uint64(n)
		if one[0] == '\n' {
			line := string(l.buf)
			l.buf = l.buf[:0]
			return strings.TrimRight(line, "\r"), nil
		}
		if len(l.buf) >= 8<<20 {
			return "", errors.New("query line exceeds response limit")
		}
		l.buf = append(l.buf, one[0])
	}
}

func dialQuery(addr, uid, password string, models ...string) (*querySession, error) {
	model, err := e2eAuthorizationModel(models)
	if err != nil {
		return nil, err
	}
	conn, err := (&net.Dialer{Timeout: readTimeout}).DialContext(context.Background(), "tcp", addr)
	if err != nil {
		return nil, err
	}
	q := &querySession{conn: conn, r: &lineReader{conn: conn}}
	connected := false
	defer func() {
		if !connected {
			_ = conn.Close()
		}
	}()
	// Banner: two lines.
	if _, err := q.r.readLine(readTimeout); err != nil {
		return nil, err
	}
	if _, err := q.r.readLine(readTimeout); err != nil {
		return nil, err
	}
	command := "login " + escapeE2EQuery(uid) + " " + escapeE2EQuery(password) + " authorization_model=" + model
	lines, err := q.cmd(command)
	if err != nil {
		return nil, err
	}
	if last := lines[len(lines)-1]; last != "error id=0 msg=ok" {
		return nil, fmt.Errorf("query login failed: %s", last)
	}
	if len(lines) != 2 || lines[0] != "authorization_model="+model {
		return nil, errors.New("query server did not confirm the selected authorization model")
	}
	connected = true
	return q, nil
}

// cmd writes a command and reads lines until the terminating error line.
func (q *querySession) cmd(command string) ([]string, error) {
	if _, err := q.conn.Write([]byte(command + "\n")); err != nil {
		return nil, err
	}
	var lines []string
	startBytes := q.r.readBytes
	deadline := time.Now().Add(queryResponseTimeout(command))
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, errors.New("query response timed out")
		}
		line, err := q.r.readLine(remaining)
		if err != nil {
			return nil, err
		}
		if q.r.readBytes-startBytes > 8<<20 {
			return nil, errors.New("query response exceeds limit")
		}
		lines = append(lines, line)
		if strings.HasPrefix(line, "error id=") {
			return lines, nil
		}
	}
}

// Role mutations have a 30-second operation budget followed by a 10-second
// commit acknowledgement window. Do not mistake a valid slow commit for a
// failed write, especially when the test server uses a remote database.
func queryResponseTimeout(command string) time.Duration {
	name, _, _ := strings.Cut(command, " ")
	switch name {
	case "rolechange", "channelchange":
		return 45 * time.Second
	case "rolelist", "rolemembers", "accesscheck", "channelquery":
		return 15 * time.Second
	default:
		return readTimeout
	}
}

func writeEncChannelChat(cl *client, channelID int64, text string) error {
	if err := awaitScopeKey(cl.conn, cl, channelID); err != nil {
		return err
	}
	id := cl.scopeLatest[channelID]
	key := cl.scopeKeys[channelID][id]
	blob, err := e2eSealScope(text, key)
	if err != nil {
		return err
	}
	return writeMsg(cl.conn, netproto.MsgChatSend, netproto.ChatSend{
		ChannelID: strconv.FormatInt(channelID, 10), Text: blob, Enc: true, KeyID: id,
	})
}

// readEncChannelChat reads chat events until one decrypts to wantText. It
// asserts the wire payload was ciphertext (the server never saw plaintext)
// and marked Enc.
func readEncChannelChat(cl *client, channelID int64, wantText string) error {
	deadline := time.Now().Add(readTimeout)
	for time.Now().Before(deadline) {
		env, err := readEvent(cl.conn, "chat", time.Until(deadline))
		if err != nil {
			return err
		}
		var chat netproto.ChatBroadcast
		if err := json.Unmarshal(env.Data, &chat); err != nil {
			return err
		}
		if !chat.Enc {
			return fmt.Errorf("server accepted/relayed PLAINTEXT chat: %s", env.Data)
		}
		if chat.Text == wantText {
			return fmt.Errorf("server relayed the message in plaintext")
		}
		key, ok := cl.scopeKeys[channelID][chat.KeyID]
		if !ok {
			continue // key for an older generation; keep waiting
		}
		plain, err := e2eOpenScope(chat.Text, key)
		if err == nil && plain == wantText {
			return nil
		}
	}
	return fmt.Errorf("encrypted chat %q not received/decryptable", wantText)
}

// checkChatHistory (5a/103): an encrypted channel message is retrievable as
// decrypted history by a channel member.
func checkChatHistory(c *checkCtx) error {
	text := "hist-" + randHex(4)
	if err := writeEncChannelChat(c.alice, c.channelID, text); err != nil {
		return fmt.Errorf("alice send: %w", err)
	}
	if err := readEncChannelChat(c.bob, c.channelID, text); err != nil {
		return fmt.Errorf("bob receive: %w", err)
	}

	if err := writeMsg(c.bob.conn, netproto.MsgChatHistory, netproto.ChatHistory{
		ChannelID: c.channelID, Limit: 10,
	}); err != nil {
		return err
	}
	f, err := readOfType(c.bob.conn, netproto.MsgChatHistoryResponse, readTimeout)
	if err != nil {
		return err
	}
	var resp netproto.ChatHistoryResponse
	if err := netproto.Decode(f, &resp); err != nil {
		return err
	}
	// The page carries the generations it references, sealed to bob's key.
	// They are ARCHIVAL: installing them must not move the send generation.
	installScopeKeys(c.bob, c.channelID, resp.Keys, false)
	for _, m := range resp.Messages {
		if m.FromUniqueID != c.alice.uid || m.Deleted {
			continue
		}
		body, err := historyBody(c.bob, c.channelID, m)
		if err != nil {
			return fmt.Errorf("history entry %d: %w", m.ID, err)
		}
		if body == text {
			return nil
		}
	}
	return fmt.Errorf("message %q not found decrypted in history (%d entries)", text, len(resp.Messages))
}

func (c *checkCtx) fileTransferAddr(port int) string {
	if port != 0 {
		host, _, err := net.SplitHostPort(c.opts.addr)
		if err == nil {
			return net.JoinHostPort(host, strconv.Itoa(port))
		}
	}
	return c.opts.fileAddr
}

func checkFiles(c *checkCtx) error {
	// Upload.
	payload := make([]byte, c.opts.filePayload)
	if _, err := rand.Read(payload); err != nil {
		return err
	}
	name := "e2e-" + randHex(4) + ".bin"

	if err := writeMsg(c.alice.conn, netproto.MsgFileTransferInit, netproto.FileTransferInit{
		ChannelID: c.channelID, Direction: "upload", Name: name, Size: int64(len(payload)),
	}); err != nil {
		return err
	}
	f, err := readOfType(c.alice.conn, netproto.MsgFileTransferInitResponse, readTimeout)
	if err != nil {
		return err
	}
	var initResp netproto.FileTransferInitResponse
	if err := netproto.Decode(f, &initResp); err != nil {
		return err
	}

	if err := uploadFile(c.fileTransferAddr(initResp.Port), initResp, payload); err != nil {
		return fmt.Errorf("upload: %w", err)
	}

	// Download.
	if err := writeMsg(c.bob.conn, netproto.MsgFileTransferInit, netproto.FileTransferInit{
		ChannelID: c.channelID, Direction: "download", Name: name,
	}); err != nil {
		return err
	}
	f, err = readOfType(c.bob.conn, netproto.MsgFileTransferInitResponse, readTimeout)
	if err != nil {
		return err
	}
	var dlResp netproto.FileTransferInitResponse
	if err := netproto.Decode(f, &dlResp); err != nil {
		return err
	}
	got, err := downloadFile(c.fileTransferAddr(dlResp.Port), dlResp)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	if !bytes.Equal(got, payload) {
		return fmt.Errorf("download content mismatch (%d vs %d bytes)", len(got), len(payload))
	}

	// List.
	if err := writeMsg(c.alice.conn, netproto.MsgFileList, netproto.FileList{ChannelID: c.channelID}); err != nil {
		return err
	}
	f, err = readOfType(c.alice.conn, netproto.MsgFileListResponse, readTimeout)
	if err != nil {
		return err
	}
	var list netproto.FileListResponse
	if err := netproto.Decode(f, &list); err != nil {
		return err
	}
	for _, e := range list.Entries {
		if e.Name == name {
			return nil
		}
	}
	return fmt.Errorf("file %q not in listing", name)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func writeFTJSON(conn net.Conn, frameType uint16, v any) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return netproto.WriteFrame(conn, &netproto.Frame{Type: frameType, Payload: payload})
}

func readStatusFrame(conn net.Conn) (bool, string, error) {
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	defer clearE2EReadDeadline(conn)
	f, err := netproto.ReadFrame(conn)
	if err != nil {
		return false, "", err
	}
	if f.Type != ftStatus {
		return false, "", fmt.Errorf("frame type = %d, want status", f.Type)
	}
	var st struct {
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	}
	if err := json.Unmarshal(f.Payload, &st); err != nil {
		return false, "", err
	}
	return st.OK, st.Error, nil
}

// dialFileTransfer follows the server's declared data-port mode. A TLS port
// must carry the certificate fingerprint learned over the authenticated
// control channel; plaintext is used only when the server explicitly says the
// data port is plaintext.
func dialFileTransfer(addr string, init netproto.FileTransferInitResponse) (net.Conn, error) {
	if !init.TLS {
		if strings.TrimSpace(init.TLSFingerprint) != "" {
			return nil, errors.New("file transfer response supplied a TLS fingerprint for a plaintext port")
		}
		return (&net.Dialer{Timeout: readTimeout}).DialContext(context.Background(), "tcp", addr)
	}
	if strings.TrimSpace(init.TLSFingerprint) == "" {
		return nil, errors.New("file transfer TLS response omitted its certificate fingerprint")
	}
	tlsConfig, err := fingerprintVerifiedTLSConfig(init.TLSFingerprint)
	if err != nil {
		return nil, fmt.Errorf("file transfer TLS fingerprint: %w", err)
	}
	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: readTimeout},
		Config:    tlsConfig,
	}
	conn, err := dialer.DialContext(context.Background(), "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("file transfer TLS: %w", err)
	}
	return conn, nil
}

func uploadFile(addr string, init netproto.FileTransferInitResponse, payload []byte) error {
	conn, err := dialFileTransfer(addr, init)
	if err != nil {
		return err
	}
	defer closeE2EResource(conn)

	if err := writeFTJSON(conn, ftInit, map[string]string{
		"token": init.Token, "transfer_id": init.TransferID,
	}); err != nil {
		return err
	}
	const chunk = 32 * 1024
	for off := 0; off < len(payload); off += chunk {
		end := off + chunk
		if end > len(payload) {
			end = len(payload)
		}
		if err := netproto.WriteFrame(conn, &netproto.Frame{Type: ftChunk, Payload: payload[off:end]}); err != nil {
			return err
		}
	}
	if err := writeFTJSON(conn, ftDigest, map[string]string{"sha256": sha256Hex(payload)}); err != nil {
		return err
	}
	ok, errMsg, err := readStatusFrame(conn)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("status: " + errMsg)
	}
	return nil
}

func downloadFile(addr string, init netproto.FileTransferInitResponse) ([]byte, error) {
	conn, err := dialFileTransfer(addr, init)
	if err != nil {
		return nil, err
	}
	defer closeE2EResource(conn)

	if err := writeFTJSON(conn, ftInit, map[string]string{
		"token": init.Token, "transfer_id": init.TransferID,
	}); err != nil {
		return nil, err
	}

	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	defer clearE2EReadDeadline(conn)
	var got []byte
	for {
		f, err := netproto.ReadFrame(conn)
		if err != nil {
			return nil, err
		}
		switch f.Type {
		case ftChunk:
			got = append(got, f.Payload...)
		case ftDigest:
			var d struct {
				SHA256 string `json:"sha256"`
			}
			if err := json.Unmarshal(f.Payload, &d); err != nil {
				return nil, err
			}
			if d.SHA256 != sha256Hex(got) {
				return nil, errors.New("digest mismatch")
			}
			ok, errMsg, err := readStatusFrame(conn)
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, errors.New("status: " + errMsg)
			}
			return got, nil
		default:
			return nil, fmt.Errorf("unexpected frame type %d", f.Type)
		}
	}
}

// ---------------------------------------------------------------------------
// ServerQuery
// ---------------------------------------------------------------------------

// dialGuest connects and authenticates as an anonymous guest.
func dialGuest(addr, nickname, serverPassword string, models ...string) (*client, error) {
	model, err := e2eAuthorizationModel(models)
	if err != nil {
		return nil, err
	}
	conn, err := dialTCP(addr)
	if err != nil {
		return nil, err
	}
	g := &client{conn: conn}
	if err := initClientKeys(g); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := writeMsg(conn, netproto.MsgAuthenticate, netproto.Authenticate{
		Anonymous:           true,
		Nickname:            nickname,
		ServerPassword:      serverPassword,
		X25519PublicKey:     base64.StdEncoding.EncodeToString(g.e2ePub[:]),
		AuthorizationModels: e2eAdvertisedModels(model),
	}); err != nil {
		_ = conn.Close()
		return nil, err
	}
	f, err := readOfType(conn, netproto.MsgAuthResponse, readTimeout)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	var resp netproto.AuthResponse
	if err := netproto.Decode(f, &resp); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if !resp.OK {
		_ = conn.Close()
		return nil, errors.New("guest auth rejected: " + resp.Reason)
	}
	if err := e2eCheckAuthorizationModel(resp, model); err != nil {
		_ = conn.Close()
		return nil, err
	}
	installScopeKeys(g, 0, resp.ChatKeys, true)
	clientsByConn.Store(conn, g)
	if _, err := readOfType(conn, netproto.MsgSnapshot, readTimeout); err != nil {
		clientsByConn.Delete(conn)
		_ = conn.Close()
		return nil, fmt.Errorf("reading snapshot: %w", err)
	}
	g.uid, g.clientID, g.nickname = resp.UniqueID, resp.ClientID, resp.Nickname
	if err := registerClient(g); err != nil {
		clientsByConn.Delete(conn)
		_ = conn.Close()
		return nil, fmt.Errorf("e2e key publish: %w", err)
	}
	return g, nil
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func waitForMove(conn net.Conn, clientID string) error {
	deadline := time.Now().Add(readTimeout)
	for time.Now().Before(deadline) {
		env, err := readEvent(conn, "user_moved", time.Until(deadline))
		if err != nil {
			return err
		}
		var moved struct {
			ClientID string `json:"client_id"`
		}
		if err := json.Unmarshal(env.Data, &moved); err != nil {
			return err
		}
		if moved.ClientID == clientID {
			return nil
		}
	}
	return fmt.Errorf("no user_moved for %s", clientID)
}

// chaosJoin puts a fresh session into the drill channel and waits for the
// scope key, so chat can be sealed without further reads.
func chaosJoin(cl *client, channelID int64) error {
	if err := writeMsg(cl.conn, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: channelID}); err != nil {
		return err
	}
	if err := waitForMove(cl.conn, cl.clientID); err != nil {
		return fmt.Errorf("membership: %w", err)
	}
	return awaitScopeKey(cl.conn, cl, channelID)
}

func checkChaosOutageReply(f *netproto.Frame) error {
	if netproto.MessageType(f.Type) != netproto.MsgError {
		return errors.New("DB-backed request succeeded while the database was confirmed down")
	}
	var e netproto.Error
	if err := netproto.Decode(f, &e); err != nil {
		return fmt.Errorf("undecodable error frame: %w", err)
	}
	// Code 5 is the protocol's backend-unavailable result. Permission or
	// malformed-request errors do not establish database outage handling.
	if e.Code != 5 {
		return fmt.Errorf("DB-backed request returned error %d (%s), want backend unavailable (5)", e.Code, e.Message)
	}
	fmt.Printf("e2e: chaos: DB-backed request answered with error frame %d (%s)\n", e.Code, e.Message)
	return nil
}
