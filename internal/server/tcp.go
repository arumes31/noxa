// Package server hosts the long-running noxa server components. This file
// implements the TCP control listener: it accepts connections, frames messages
// using the netproto wire format, dispatches them to per-message-type
// handlers, and tracks connected clients in a thread-safe registry.
//
// The handlers live in handlers.go and are wired to the auth, state,
// channels, broadcast, and permissions backends via Deps (see deps.go). A
// client must authenticate before any command other than Authenticate and
// Ping is accepted.
package server

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"go.uber.org/zap"

	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/chatcrypto"
	"noxa/internal/config"
	"noxa/internal/filetransfer"
	"noxa/internal/metrics"
	"noxa/internal/netproto"
	"noxa/internal/state"
	"noxa/internal/tlscert"
	"noxa/internal/webrtc"
)

// Error codes sent in MsgError frames.
const (
	errCodeUnknown          = 1 // unknown message type
	errCodeMalformed        = 2 // malformed message payload
	errCodeNotAuthenticated = 3 // command requires authentication
	errCodePermissionDenied = 4 // caller lacks the required permission
	errCodeUnavailable      = 5 // backend dependency unavailable
	errCodeNotFound         = 6 // referenced entity not found
	errCodeConflict         = 7 // optimistic concurrency precondition failed
)

// Client represents a single connected control-channel client.
type Client struct {
	ID       string
	Conn     net.Conn
	Username string // nickname, once authenticated
	UniqueID string // TS3-style unique ID, once authenticated
	UserID   int64  // database users.id, once authenticated

	mu                 sync.RWMutex
	authed             bool
	revoked            bool   // role-mode removal closes protected work before socket cleanup
	disconnectCleaned  bool   // membership/media cleanup is idempotent
	bot                bool   // authenticated account metadata, not a capability
	authorizationModel string // negotiated before any authentication path
	// rulesPending gates a session that still owes the operator's rules an
	// answer (215). It is per connection, not per account, because a guest
	// has no users row to record an acceptance against.
	rulesPending bool
	// The authentication snapshot covers updates before queue registration.
	// A pending delivery deadline covers both queue residence and socket writes.
	mediaLimitsReady           bool
	mediaLimitsPending         uint64
	mediaLimitsTimer           *time.Timer
	mediaLimitsTimerGeneration uint64

	// Activity and connection stats (Client Info dialog).
	lastActive time.Time // last received frame
	bytesIn    int64     // payload bytes received
	bytesOut   int64     // payload bytes sent
	lastPingAt time.Time // last server-initiated Ping sent
	rttNs      int64     // smoothed RTT in nanoseconds (EWMA)
	rttKnown   bool      // whether any Pong was received

	// Pending challenge-response handshake state (set on Authenticate without
	// a password, consumed by AuthSignature).
	challenge     []byte
	challengeUID  string
	challengeNick string
	challengeExp  time.Time

	// x25519Key is the encryption key presented at authenticate time so the
	// server can seal the global generation and the MOTD into the auth
	// response (133). The identity PublicKey is Ed25519 and cannot be sealed
	// to, hence the separate field.
	x25519Key string

	wmu contextMutex // serializes frame writes to Conn
	// Role-mode member operations lock after acquiring the policy lease, so
	// a concurrent move cannot change the scope checked by a moderation action.
	// Packet writes share read leases; scope mutations retain exclusive access.
	roleActionMu sync.RWMutex
}

// isAuthed reports whether the client has completed authentication.
func (c *Client) isAuthed() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.authed && !c.revoked
}

func (c *Client) sessionRevoked() bool { c.mu.RLock(); defer c.mu.RUnlock(); return c.revoked }

func (c *Client) revokeSession() { c.mu.Lock(); defer c.mu.Unlock(); c.revoked = true }

func (c *Client) needsDisconnectCleanup() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.authed && !c.disconnectCleaned
}

func (c *Client) markDisconnectCleaned() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.disconnectCleaned = true
	c.mediaLimitsReady = false
	c.mediaLimitsPending = 0
	if c.mediaLimitsTimer != nil {
		c.mediaLimitsTimer.Stop()
		c.mediaLimitsTimer = nil
	}
}

// setIdentity atomically records the authenticated identity on the client.
func (c *Client) setIdentity(uniqueID, nickname string, userID int64, bot bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.UniqueID = uniqueID
	c.Username = nickname
	c.UserID = userID
	c.bot = userID != 0 && bot
	c.authed = true
}

func (c *Client) userID() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.UserID
}

// uniqueID returns the client's authenticated unique ID ("" before auth).
func (c *Client) uniqueID() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.UniqueID
}

// setX25519 records the encryption key presented at authenticate time.
func (c *Client) setX25519(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.x25519Key = key
}

// x25519 returns the encryption key presented at authenticate time ("" for
// clients that supplied none).
func (c *Client) x25519() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.x25519Key
}

// noteReceived records inbound activity for the Client Info stats.
func (c *Client) noteReceived(payloadBytes int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastActive = time.Now()
	c.bytesIn += int64(payloadBytes)
}

func (c *Client) lastActivity() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastActive
}

// noteSent records outbound payload bytes for the Client Info stats.
func (c *Client) noteSent(payloadBytes int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bytesOut += int64(payloadBytes)
}

// notePingSent records that a server-initiated Ping was sent (for RTT
// matching).
func (c *Client) notePingSent() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastPingAt = time.Now()
}

// notePong measures the RTT against the last server Ping and folds it into
// the smoothed value.
func (c *Client) notePong() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lastPingAt.IsZero() {
		return
	}
	sample := time.Since(c.lastPingAt).Nanoseconds()
	c.rttNs = ewmaRTT(c.rttNs, sample, c.rttKnown)
	c.rttKnown = true
}

// ewmaRTT folds a new RTT sample into the exponentially weighted moving
// average (alpha = 1/8). The first sample seeds the average.
func ewmaRTT(prev, sample int64, known bool) int64 {
	if !known {
		return sample
	}
	return prev*7/8 + sample/8
}

// clientStats returns a snapshot of the Client Info stats.
type clientStats struct {
	lastActive time.Time
	bytesIn    int64
	bytesOut   int64
	rttNs      int64
	rttKnown   bool
}

// stats returns a snapshot of the activity stats.
func (c *Client) stats() clientStats {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return clientStats{
		lastActive: c.lastActive,
		bytesIn:    c.bytesIn,
		bytesOut:   c.bytesOut,
		rttNs:      c.rttNs,
		rttKnown:   c.rttKnown,
	}
}

// setChallenge stores a pending auth challenge for the challenge-response
// handshake, along with the requested nickname (used for guest logins).
func (c *Client) setChallenge(uniqueID, nickname string, challenge []byte, expires time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.challengeUID = uniqueID
	c.challengeNick = nickname
	c.challenge = challenge
	c.challengeExp = expires
}

// takeChallenge returns and clears the pending challenge if it belongs to
// uniqueID and has not expired. An empty uniqueID matches any pending
// challenge (used by the guest public-key path, which does not know its
// unique ID in advance). It also returns the stored nickname.
func (c *Client) takeChallenge(uniqueID string) ([]byte, string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.challenge == nil || time.Now().After(c.challengeExp) {
		return nil, "", false
	}
	if uniqueID != "" && c.challengeUID != uniqueID {
		return nil, "", false
	}
	ch := c.challenge
	nick := c.challengeNick
	c.challenge = nil
	c.challengeUID = ""
	c.challengeNick = ""
	c.challengeExp = time.Time{}
	return ch, nick, true
}

// TCPServer accepts and serves control-channel connections.
type TCPServer struct {
	roleRecordingOwners   sync.Map   // channel ID -> initiating registered user ID
	roleRevokedRecordings sync.Map   // channel IDs awaiting cleanup; tap delivery is denied
	roleRecordingMu       sync.Mutex // serialize recording start/stop and ownership publication
	// Order: Authority, roleMetadataMu, client roleActionMu, backend locks.
	// SDP metadata must not cross membership/status changes. Packet delivery
	// does not acquire this mutex, so a slow SDP reader cannot stall all audio.
	roleMetadataMu      sync.Mutex
	cfg                 *config.Config
	logger              *zap.Logger
	deps                *Deps
	configMu            sync.RWMutex
	configSaveMu        contextMutex // persistence, runtime publication and audit order
	mediaLimitsRevision uint64       // protected with cfg's media fields by configMu
	serverTextMu        contextMutex // text edits and join-time re-seal write-back

	// lifecycleMu protects the listener lifecycle, raw accepted-connection
	// registry, and every connWG.Add. Shutdown transitions to stopping while
	// holding this lock, so Wait can never race a later Add.
	lifecycleMu      sync.Mutex
	lifecycleState   tcpLifecycleState
	listener         net.Listener
	acceptedConns    map[net.Conn]struct{}
	connWG           sync.WaitGroup
	listen           func(context.Context, string, string) (net.Listener, error)
	prepareTLS       func() (tls.Certificate, string, error)
	startup          *tcpStartup
	started          chan struct{}
	acceptDone       chan struct{}
	shutdownDone     chan struct{}
	shutdownErr      error
	shutdownDoneOK   bool
	shutdownTimedOut bool

	// tlsFingerprint is the SHA-256 fingerprint of the control-channel
	// certificate (empty when TLS is disabled). Set in Start, or earlier by
	// UseTLSMaterial.
	tlsFingerprint string

	// tlsCert is the control-channel certificate when the caller supplied it
	// up front. The file-transfer port must present the SAME certificate, so
	// the binary generates it once and hands it to both rather than letting
	// two racing tlscert.Ensure calls mint two self-signed certs.
	tlsCert    *tls.Certificate
	tlsCertSet bool

	// chatKeys manages the per-scope chat encryption keys and their
	// persisted generations (91).
	chatKeys *chatKeyManager

	// rotPending coalesces channel key rotations inside
	// chat_key_rotate_min_seconds so a flapping client cannot mint one
	// persisted generation per reconnect.
	rotMu         sync.Mutex
	rotPending    map[int64]bool
	rotationAfter func(time.Duration, func())

	// Chat infrastructure (wave 5a): rate limiter, spam tracker, slow-mode
	// tracker, and the memoised runtime moderation lists (117/118).
	chatRate     *chatRateLimiter
	chatSpam     *spamTracker
	chatSlow     *slowTracker
	typingRate   *typingTracker
	chatFilters  *chatFilterCache
	pokes        pokeTracker
	beforeHandle func()
	afterAccept  func()

	loginLimiter *auth.LoginFailureLimiter

	mu      sync.RWMutex
	clients map[string]*Client

	// startedAt feeds the public server-info uptime (313).
	startedAt time.Time

	stopOnce sync.Once
	stopCh   chan struct{}
}

type tcpLifecycleState uint8

const (
	tcpLifecycleNew tcpLifecycleState = iota
	tcpLifecycleStarting
	tcpLifecycleRunning
	tcpLifecycleStopping
	tcpLifecycleStopped
)

// tcpStartup owns listener preparation before publication. Shutdown cancels
// it and waits for done before declaring the terminal lifecycle result.
type tcpStartup struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// New constructs a TCPServer wired to the given backend dependencies. The
// listener is created lazily in Start so that construction never blocks and
// Shutdown is always safe to call. deps may be nil; handlers that need a
// missing dependency reply with an "unavailable" error.
func New(cfg *config.Config, logger *zap.Logger, deps *Deps) *TCPServer {
	var scopeKeys ScopeKeyStore
	var kek *chatcrypto.KEKRing
	var chatCryptoFailure func(string)
	loginLimiter := auth.NewLoginFailureLimiter(auth.LoginFailureLimiterConfig{})
	if deps != nil {
		scopeKeys, kek = deps.ScopeKeys, deps.ChatKEK
		if deps.Metrics != nil {
			chatCryptoFailure = deps.Metrics.IncChatCryptoFailure
		}
		if deps.LoginLimiter != nil {
			loginLimiter = deps.LoginLimiter
		}
	}
	s := &TCPServer{
		cfg:           cfg,
		logger:        logger,
		deps:          deps,
		acceptedConns: make(map[net.Conn]struct{}),
		listen: func(ctx context.Context, network, address string) (net.Listener, error) {
			return (&net.ListenConfig{}).Listen(ctx, network, address)
		},
		prepareTLS: func() (tls.Certificate, string, error) {
			return tlscert.Ensure(cfg.TLSDir, cfg.TLSCertFile, cfg.TLSKeyFile,
				[]string{"localhost", cfg.ServerName})
		},
		shutdownDone: make(chan struct{}),
		started:      make(chan struct{}),
		clients:      make(map[string]*Client),
		stopCh:       make(chan struct{}),
		startedAt:    time.Now(),
		chatKeys:     newChatKeyManager(scopeKeys, kek, logger, chatCryptoFailure),
		rotPending:   make(map[int64]bool),
		rotationAfter: func(delay time.Duration, callback func()) {
			time.AfterFunc(delay, callback)
		},
		chatRate:     newChatRateLimiter(cfg.ChatRateMsgs, time.Duration(cfg.ChatRateWindowSeconds)*time.Second),
		chatSpam:     newSpamTracker(),
		chatSlow:     newSlowTracker(),
		typingRate:   newTypingTracker(),
		loginLimiter: loginLimiter,

		chatFilters: &chatFilterCache{},
	}
	// Install the voice pipeline callbacks (talk/video permission gates,
	// speaking-state announcements, and renegotiation offer delivery).
	if deps != nil && deps.Authority != nil && deps.FileTransfer != nil {
		deps.FileTransfer.SetAccessGuard(s.guardRoleFileTransfer)
	}
	if deps != nil && deps.Voice != nil {
		deps.Voice.SetHandlers(s.canTalk, s.onSpeakingChanged)
		deps.Voice.SetVideoHandlers(s.canPublishVideo)
		if deps.Authority != nil {
			deps.Voice.SetMediaGuard(s.guardRoleMedia)
			deps.Voice.SetPublisherGuard(func(webrtc.PublisherAccess) bool { return false })
			deps.Voice.SetOfferGuard(func(clientID string, send func() error) error {
				client, ok := s.clientByID(clientID)
				if !ok {
					return authorization.ErrRoleForbidden
				}
				return s.withRoleChannelControl(context.Background(), client, authorization.Connect, true, func(ctx context.Context, _ int64) error {
					s.refreshRolePublishers(ctx.Value(roleLeaseKey{}).(roleLease).evaluator)
					return send()
				})
			})
		}
		deps.Voice.SetOfferSender(func(clientID, offerSDP string) error {
			client, ok := s.clientByID(clientID)
			if !ok {
				return errors.New("client not connected")
			}
			// OfferGuard already holds the current policy and membership lease.
			return s.writeMessage(client, netproto.MsgWebRTCOffer, netproto.WebRTCOffer{SDP: offerSDP})
		})
	}
	return s
}

// Start binds the TCP listener and serves connections until ctx is cancelled
// or Shutdown is called. It returns when the accept loop exits. When
// tls_enabled is set the listener is wrapped in TLS (self-signed cert from
// tls_dir or the configured cert/key files).
func (s *TCPServer) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	s.lifecycleMu.Lock()
	if s.lifecycleState != tcpLifecycleNew {
		s.lifecycleMu.Unlock()
		return nil
	}
	startCtx, cancelStart := context.WithCancel(ctx)
	startup := &tcpStartup{cancel: cancelStart, done: make(chan struct{})}
	s.lifecycleState = tcpLifecycleStarting
	s.startup = startup
	tlsCert, tlsFingerprint, tlsCertSet := s.tlsCert, s.tlsFingerprint, s.tlsCertSet
	s.lifecycleMu.Unlock()
	defer cancelStart()
	defer close(startup.done)

	var cert tls.Certificate
	if s.cfg.TLSEnabled {
		if tlsCertSet {
			cert = *tlsCert
		} else {
			preparedCert, fingerprint, err := s.prepareTLS()
			if err != nil {
				if s.finishStartFailure(startup) {
					return fmt.Errorf("preparing control-channel TLS: %w", err)
				}
				return nil
			}
			cert, tlsFingerprint = preparedCert, fingerprint
		}
	}
	if err := startCtx.Err(); err != nil {
		if s.finishStartFailure(startup) {
			return err
		}
		return nil
	}

	ln, err := s.listen(startCtx, "tcp", s.cfg.TCPAddr)
	if err != nil {
		if s.finishStartFailure(startup) {
			return fmt.Errorf("tcp listen on %s: %w", s.cfg.TCPAddr, err)
		}
		return nil
	}
	if err := startCtx.Err(); err != nil {
		_ = ln.Close()
		if s.finishStartFailure(startup) {
			return err
		}
		return nil
	}
	if s.cfg.TLSEnabled {
		ln = tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13})
	}

	// Start and Shutdown only serialize the state transition itself. Listener
	// and TLS preparation stay outside the mutex, so a stalled startup cannot
	// make Shutdown miss its caller deadline. A shutdown that wins this final
	// check closes this unpublished listener instead of ever exposing it.
	s.lifecycleMu.Lock()
	if s.lifecycleState != tcpLifecycleStarting || s.startup != startup {
		s.lifecycleMu.Unlock()
		_ = ln.Close()
		return nil
	}
	acceptDone := make(chan struct{})
	s.listener = ln
	s.acceptDone = acceptDone
	s.lifecycleState = tcpLifecycleRunning
	s.startup = nil
	s.tlsFingerprint = tlsFingerprint
	close(s.started)
	s.lifecycleMu.Unlock()
	if s.cfg.TLSEnabled {
		s.logger.Info("TCP control listener started (TLS)",
			zap.String("addr", s.cfg.TCPAddr),
			zap.String("tls_fingerprint", tlsFingerprint),
		)
	} else {
		s.logger.Info("TCP control listener started (plaintext!)", zap.String("addr", s.cfg.TCPAddr))
	}

	// Close the listener when the context is cancelled so Accept unblocks.
	go func() {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.shutdownTimeout())
			_ = s.Shutdown(shutdownCtx)
			cancel()
		case <-s.stopCh:
		}
	}()

	defer close(acceptDone)
	for {
		conn, err := ln.Accept()
		if err != nil {
			if s.stopping() {
				return nil
			}
			s.stopOnce.Do(s.beginShutdown)
			return fmt.Errorf("tcp accept: %w", err)
		}
		if s.afterAccept != nil {
			s.afterAccept()
		}
		s.lifecycleMu.Lock()
		if s.lifecycleState != tcpLifecycleRunning || s.listener != ln {
			s.lifecycleMu.Unlock()
			_ = conn.Close()
			continue
		}
		s.acceptedConns[conn] = struct{}{}
		s.connWG.Add(1)
		s.lifecycleMu.Unlock()
		s.logger.Debug("TCP connection accepted", zap.String("remote", conn.RemoteAddr().String()))
		s.metricsSink().IncTCPConnections()
		go s.serveConn(ctx, conn)
	}
}

// finishStartFailure clears a startup claim only when shutdown did not win it.
// It reports whether Start should return the original startup error.
func (s *TCPServer) finishStartFailure(startup *tcpStartup) bool {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.lifecycleState != tcpLifecycleStarting || s.startup != startup {
		return false
	}
	s.lifecycleState = tcpLifecycleNew
	s.startup = nil
	return true
}

// TLSFingerprint returns the SHA-256 fingerprint of the control-channel
// certificate, or "" when TLS is disabled.
func (s *TCPServer) TLSFingerprint() string {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	return s.tlsFingerprint
}

// UseTLSMaterial installs the control-channel certificate instead of letting
// Start mint its own. The file-transfer port presents the same certificate,
// and two independent tlscert.Ensure calls on a fresh install would race to
// create two different self-signed certs. Call before Start.
func (s *TCPServer) UseTLSMaterial(cert tls.Certificate, fingerprint string) {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.tlsCert, s.tlsFingerprint, s.tlsCertSet = &cert, fingerprint, true
}

// EnsureGlobalScopeKey mints the global chat generation at boot. Global is a
// fixed, known scope, so minting it eagerly is safe and removes the only
// legitimate lazy mint from a hot path (91).
func (s *TCPServer) EnsureGlobalScopeKey(ctx context.Context) error {
	_, _, err := s.chatKeys.EnsureScope(ctx, globalChatScope)
	return err
}

// Shutdown permanently stops the listener, closes every accepted raw
// connection, and waits for their handlers to exit. Callers share one
// terminal result; the first incomplete caller deadline becomes that result.
func (s *TCPServer) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.stopOnce.Do(s.beginShutdown)

	select {
	case <-s.shutdownDone:
		return s.shutdownResult()
	case <-ctx.Done():
		// A completed graceful shutdown wins a simultaneous context deadline.
		select {
		case <-s.shutdownDone:
			return s.shutdownResult()
		default:
		}
		return s.recordShutdownDeadline(ctx.Err())
	}
}

func (s *TCPServer) beginShutdown() {
	s.lifecycleMu.Lock()
	s.lifecycleState = tcpLifecycleStopping
	startup := s.startup
	s.startup = nil
	listener := s.listener
	acceptDone := s.acceptDone
	connections := make([]net.Conn, 0, len(s.acceptedConns))
	for conn := range s.acceptedConns {
		connections = append(connections, conn)
	}
	s.lifecycleMu.Unlock()
	if startup != nil {
		startup.cancel()
	}

	close(s.stopCh)
	go func() {
		var shutdownErr error
		shutdownErr = joinTCPShutdownError(shutdownErr, closeTCPListener(listener))
		// Give authenticated clients a fixed semantic reason before EOF. Raw
		// unauthenticated sockets still close immediately. Writes run in parallel
		// and each has a short deadline, independent of the number of clients.
		s.mu.RLock()
		clients := make([]*Client, 0, len(s.clients))
		for _, client := range s.clients {
			if client.isAuthed() {
				clients = append(clients, client)
			}
		}
		s.mu.RUnlock()
		var notices sync.WaitGroup
		for _, client := range clients {
			notices.Add(1)
			go func() {
				defer notices.Done()
				s.sendTerminalEvent(client, "server_shutdown", struct{}{})
			}()
		}
		notices.Wait()
		for _, conn := range connections {
			shutdownErr = joinTCPShutdownError(shutdownErr, conn.Close())
		}
		if startup != nil {
			<-startup.done
		}
		if acceptDone != nil {
			<-acceptDone
		}
		s.connWG.Wait()
		s.completeShutdown(shutdownErr)
	}()
}

func closeTCPListener(listener net.Listener) error {
	if listener == nil {
		return nil
	}
	return listener.Close()
}

func joinTCPShutdownError(current, err error) error {
	if err == nil || errors.Is(err, net.ErrClosed) {
		return current
	}
	return errors.Join(current, err)
}

func (s *TCPServer) shutdownResult() error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	return s.shutdownErr
}

func (s *TCPServer) completeShutdown(closeErr error) {
	s.lifecycleMu.Lock()
	timedOut := s.shutdownTimedOut
	if !timedOut {
		s.shutdownErr = errors.Join(s.shutdownErr, closeErr)
	}
	s.lifecycleState = tcpLifecycleStopped
	s.shutdownDoneOK = true
	close(s.shutdownDone)
	s.lifecycleMu.Unlock()

	if timedOut && closeErr != nil {
		s.logger.Warn("TCP shutdown cleanup error after deadline", zap.Error(closeErr))
	}
}

// recordShutdownDeadline makes the first caller deadline the shared terminal
// result. Later callers cannot observe a successful result after an earlier
// bounded shutdown has already timed out.
func (s *TCPServer) recordShutdownDeadline(ctxErr error) error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.shutdownDoneOK {
		return s.shutdownErr
	}
	if !s.shutdownTimedOut {
		if s.shutdownErr == nil {
			s.shutdownErr = ctxErr
		} else {
			s.shutdownErr = errors.Join(s.shutdownErr, ctxErr)
		}
		s.shutdownTimedOut = true
	}
	return s.shutdownErr
}

func (s *TCPServer) stopping() bool {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	return s.lifecycleState >= tcpLifecycleStopping
}

func (s *TCPServer) shutdownTimeout() time.Duration {
	if s.cfg != nil && s.cfg.ShutdownTimeout > 0 {
		return s.cfg.ShutdownTimeout
	}
	return 30 * time.Second
}

// serveConn is deliberately the outermost handler boundary. handleConn's
// cleanup defers run first on a panic; only then does this wrapper log the
// fixed, non-payload panic metadata and retire lifecycle bookkeeping.
func (s *TCPServer) serveConn(ctx context.Context, conn net.Conn) {
	defer s.connWG.Done()
	defer func() {
		s.lifecycleMu.Lock()
		delete(s.acceptedConns, conn)
		s.lifecycleMu.Unlock()
	}()
	defer func() {
		if recovered := recover(); recovered != nil {
			s.logger.Error("TCP connection handler panic",
				zap.String("panic_type", fmt.Sprintf("%T", recovered)),
				zap.Stack("stack"),
			)
		}
	}()

	s.handleConn(ctx, conn)
}

// handleConn services a single client connection for its lifetime.
func (s *TCPServer) handleConn(ctx context.Context, conn net.Conn) {
	defer func() {
		if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			s.logger.Debug("closing client connection", zap.Error(err))
		}
	}()

	// Bound the entire unauthenticated exchange, including TLS and blocked
	// replies. Incoming pings or partial frames must not renew this deadline.
	authTimeout := 10 * time.Second
	s.configMu.RLock()
	if timeout := s.cfg.ClientTimeoutSeconds; timeout > 0 && timeout < 10 {
		authTimeout = time.Duration(timeout) * time.Second
	}
	s.configMu.RUnlock()
	authDeadline := time.Now().Add(authTimeout)
	if err := conn.SetDeadline(authDeadline); err != nil {
		return
	}
	authCtx, cancelAuth := context.WithDeadline(ctx, authDeadline)
	defer cancelAuth()

	client := &Client{
		ID:         newClientID(),
		Conn:       conn,
		lastActive: time.Now(),
	}
	s.register(client)
	defer func() {
		if s.deps != nil && s.deps.Authority != nil {
			// Keep the session discoverable until serialized cleanup marks it
			// revoked; account bans must also find connections already closing.
			s.onDisconnect(client)
			s.unregister(client.ID)
			return
		}
		s.unregister(client.ID)
		s.onDisconnect(client)
	}()

	// (217) max-clients enforcement: config max_clients, tightened by the
	// serveredit override. 0 = unlimited.
	if max := s.EffectiveMaxClients(authCtx); max > 0 && s.clientCount() > max {
		_ = s.sendGlobalError(client, errCodeUnavailable, "server is full")
		return
	}

	s.logger.Info("client connected",
		zap.String("client_id", client.ID),
		zap.String("remote", conn.RemoteAddr().String()),
	)

	// Server-initiated keepalive: Ping every 15s; matching Pongs feed the
	// smoothed RTT reported in Client Info.
	pingStop := make(chan struct{})
	pingDone := make(chan struct{})
	defer func() {
		close(pingStop)
		// A ping write can be blocked while the connection handler is leaving.
		// Closing the raw connection before waiting guarantees the child exits.
		_ = conn.Close()
		<-pingDone
	}()
	go s.servePingLoop(client, pingStop, pingDone)
	if s.beforeHandle != nil {
		s.beforeHandle()
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		default:
		}

		frame, err := netproto.ReadFrame(conn)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				s.logger.Info("client disconnected",
					zap.String("client_id", client.ID),
					zap.String("remote", conn.RemoteAddr().String()),
				)
				return
			}
			s.logger.Warn("read frame error",
				zap.String("client_id", client.ID),
				zap.Error(err),
			)
			return
		}

		client.noteReceived(len(frame.Payload))
		if !authDeadline.IsZero() && !time.Now().Before(authDeadline) {
			return
		}

		requestCtx := ctx
		if !authDeadline.IsZero() {
			requestCtx = authCtx
		}
		if err := s.dispatch(requestCtx, client, frame); err != nil {
			s.logger.Warn("dispatch error",
				zap.String("client_id", client.ID),
				zap.String("msg_type", netproto.MessageType(frame.Type).String()),
				zap.Error(err),
			)
			return
		}
		if !authDeadline.IsZero() && client.isAuthed() {
			if authCtx.Err() != nil {
				return
			}
			if err := conn.SetDeadline(time.Time{}); err != nil {
				return
			}
			cancelAuth()
			authDeadline = time.Time{}
		}
	}
}

// dispatch routes a frame to the appropriate handler based on its type. Any
// command other than Authenticate and Ping is rejected until the client has
// authenticated.
func (s *TCPServer) dispatch(ctx context.Context, client *Client, f *netproto.Frame) error {
	mt := netproto.MessageType(f.Type)
	ctx = context.WithValue(ctx, requestOriginContextKey{}, mt)

	if !client.isAuthed() && mt != netproto.MsgAuthenticate && mt != netproto.MsgAuthSignature && mt != netproto.MsgPing {
		return s.sendErrorFor(client, mt, errCodeNotAuthenticated, "not authenticated")
	}
	if client.rulesBlocked() && !allowedWhileRulesPending[mt] {
		return s.sendErrorFor(client, mt, errCodePermissionDenied, "accept the server rules before continuing")
	}
	switch mt {
	case netproto.MsgRoleChannelQuery:
		return s.handleRoleChannelQuery(ctx, client, f)
	case netproto.MsgRoleChannelChange:
		return s.handleRoleChannelChange(ctx, client, f)
	case netproto.MsgRoleQuery:
		return s.rolePolicyRead(ctx, client, func(ctx context.Context) error { return s.handleRoleQuery(ctx, client, f) })
	case netproto.MsgRoleMemberQuery:
		return s.rolePolicyRead(ctx, client, func(ctx context.Context) error { return s.handleRoleMembers(ctx, client, f) })
	case netproto.MsgRoleChange:
		return s.handleRoleChange(ctx, client, f)
	case netproto.MsgAccessCheck:
		return s.rolePolicyRead(ctx, client, func(ctx context.Context) error { return s.handleAccessCheck(ctx, client, f) })
	case netproto.MsgChannelAccessPreview:
		return s.rolePolicyRead(ctx, client, func(ctx context.Context) error { return s.handleChannelAccessPreview(ctx, client, f) })
	case netproto.MsgAuthenticate:
		return s.handleAuthenticate(ctx, client, f)
	case netproto.MsgAuthSignature:
		return s.handleAuthSignature(ctx, client, f)
	case netproto.MsgJoinChannel:
		return s.handleJoinChannel(ctx, client, f)
	case netproto.MsgMoveClient:
		return s.handleMoveClient(ctx, client, f)
	case netproto.MsgKickClient:
		return s.handleKickClient(ctx, client, f)
	case netproto.MsgChatSend:
		return s.handleChatSend(ctx, client, f)
	case netproto.MsgWebRTCOffer:
		return s.handleWebRTCOffer(ctx, client, f)
	case netproto.MsgMemberVoiceSet:
		return s.handleMemberVoiceSet(ctx, client, f)
	case netproto.MsgWebRTCAnswer:
		return s.handleWebRTCAnswer(ctx, client, f)
	case netproto.MsgICECandidate:
		return s.handleICECandidate(ctx, client, f)
	case netproto.MsgWhisperSet:
		return s.handleWhisperSet(ctx, client, f)
	case netproto.MsgPositionUpdate:
		return s.handlePositionUpdate(ctx, client, f)
	case netproto.MsgVideoQuality:
		return s.handleVideoQuality(ctx, client, f)
	case netproto.MsgPrioritySpeaker:
		return s.handlePrioritySpeaker(ctx, client, f)
	case netproto.MsgRecordingControl:
		return s.handleRecordingControl(ctx, client, f)
	case netproto.MsgFileTransferInit:
		return s.handleFileTransferInit(ctx, client, f)
	case netproto.MsgFileList:
		return s.handleFileList(ctx, client, f)
	case netproto.MsgFileDelete:
		return s.handleFileDelete(ctx, client, f)
	case netproto.MsgFileRename:
		return s.handleFileRename(ctx, client, f)
	case netproto.MsgFileVersions:
		return s.handleFileVersions(ctx, client, f)
	case netproto.MsgFileLink:
		return s.handleFileLink(ctx, client, f)
	case netproto.MsgServerIconSet:
		return s.handleServerIconSet(ctx, client, f)
	case netproto.MsgServerIconGet:
		return s.handleServerIconGet(ctx, client, f)
	case netproto.MsgServerBannerSet:
		return s.handleServerBannerSet(ctx, client, f)
	case netproto.MsgServerBannerGet:
		return s.handleServerBannerGet(ctx, client, f)
	case netproto.MsgSetStatus:
		return s.handleSetStatus(ctx, client, f)
	case netproto.MsgPoke:
		return s.handlePoke(ctx, client, f)
	case netproto.MsgServerInfoQuery:
		return s.handleServerInfoQuery(ctx, client, f)
	case netproto.MsgAvatarSet:
		return s.handleAvatarSet(ctx, client, f)
	case netproto.MsgAvatarGet:
		return s.handleAvatarGet(ctx, client, f)
	case netproto.MsgChannelIconSet:
		return s.handleChannelIconSet(ctx, client, f)
	case netproto.MsgChannelIconGet:
		return s.handleChannelIconGet(ctx, client, f)
	case netproto.MsgComplaint:
		return s.handleComplaint(ctx, client, f)
	case netproto.MsgScreenShare:
		return s.handleScreenShare(ctx, client, f)
	case netproto.MsgKeyPublish:
		return s.handleKeyPublish(ctx, client, f)
	case netproto.MsgKeyRequest:
		return s.handleKeyRequest(ctx, client, f)
	case netproto.MsgChatHistory:
		return s.handleChatHistory(ctx, client, f)
	case netproto.MsgChatEdit:
		return s.handleChatEdit(ctx, client, f)
	case netproto.MsgChatDelete:
		return s.handleChatDelete(ctx, client, f)
	case netproto.MsgChatPin:
		return s.handleChatPin(ctx, client, f)
	case netproto.MsgChatPins:
		return s.handleChatPins(ctx, client, f)
	case netproto.MsgChatKeyRequest:
		return s.handleChatKeyRequest(ctx, client, f)
	case netproto.MsgChatReact:
		return s.handleChatReact(ctx, client, f)
	case netproto.MsgChatFilterGet:
		return s.handleChatFilterGet(ctx, client, f)
	case netproto.MsgChatFilterSet:
		return s.handleChatFilterSet(ctx, client, f)
	case netproto.MsgTyping:
		return s.handleTyping(ctx, client, f)
	case netproto.MsgChatDelivered:
		return s.handleChatDelivered(ctx, client, f)
	case netproto.MsgChatRead:
		return s.handleChatRead(ctx, client, f)
	case netproto.MsgEmojiUpload:
		return s.handleEmojiUpload(ctx, client, f)
	case netproto.MsgEmojiList:
		return s.handleEmojiList(ctx, client, f)
	case netproto.MsgEmojiGet:
		return s.handleEmojiGet(ctx, client, f)
	case netproto.MsgEmojiDelete:
		return s.handleEmojiDelete(ctx, client, f)
	case netproto.MsgEmojiRename:
		return s.handleEmojiRename(ctx, client, f)
	case netproto.MsgServerConfigQuery:
		return s.handleServerConfigQuery(ctx, client, f)
	case netproto.MsgServerConfigSet:
		return s.handleServerConfigSet(ctx, client, f)
	case netproto.MsgMediaLimitsSet:
		return s.handleMediaLimitsSet(ctx, client, f)
	case netproto.MsgPreKeyPublish:
		return s.handlePreKeyPublish(ctx, client, f)
	case netproto.MsgPreKeyQuery:
		return s.handlePreKeyQuery(ctx, client, f)
	case netproto.MsgClientInfoQuery:
		return s.handleClientInfoQuery(ctx, client, f)
	case netproto.MsgAuditLog:
		return s.handleAuditLog(ctx, client, f)
	case netproto.MsgBanList:
		return s.handleBanList(ctx, client, f)
	case netproto.MsgRoleBanRemove:
		return s.handleRoleBanRemove(ctx, client, f)
	case netproto.MsgRoleChannelIconSet:
		return s.handleRoleChannelIconSet(ctx, client, f)
	case netproto.MsgComplaintList:
		return s.handleComplaintList(ctx, client, f)
	case netproto.MsgComplaintClear:
		return s.handleComplaintClear(ctx, client, f)
	case netproto.MsgServerRulesAccept:
		return s.handleServerRulesAccept(ctx, client, f)
	case netproto.MsgChannelSubscribe:
		return s.handleChannelSubscribe(ctx, client, f)
	case netproto.MsgPing:
		return s.handlePing(ctx, client, f)
	case netproto.MsgPong:
		client.notePong()
		return nil
	default:
		s.logger.Warn("unknown message type",
			zap.String("client_id", client.ID),
			zap.Uint16("msg_type", f.Type),
		)
		return s.sendErrorFor(client, mt, errCodeUnknown, fmt.Sprintf("unknown message type %d", f.Type))
	}
}

// requestOriginContextKey keeps the immutable origin local to one dispatch
// call. Helpers already receive ctx, so forwarding it avoids mutable Client
// state while retaining explicit sendErrorFor origin arguments at every call
// site.
type requestOriginContextKey struct{}

func requestOrigin(ctx context.Context) netproto.MessageType {
	origin, _ := ctx.Value(requestOriginContextKey{}).(netproto.MessageType)
	return origin
}

// allowedWhileRulesPending is deliberately small: central dispatch denies new
// message types by default until the client accepts the current rules.
var allowedWhileRulesPending = map[netproto.MessageType]bool{
	netproto.MsgServerRulesAccept: true,
	netproto.MsgPing:              true,
	netproto.MsgPong:              true,
}

// servePingLoop contains the per-connection child goroutine so a panic there
// cannot escape the process or outlive handleConn's cleanup boundary.
func (s *TCPServer) servePingLoop(client *Client, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	defer func() {
		if recovered := recover(); recovered != nil {
			s.logger.Error("TCP ping loop panic",
				zap.String("panic_type", fmt.Sprintf("%T", recovered)),
				zap.Stack("stack"),
			)
		}
	}()
	s.pingLoop(client, stop)
}

// pingLoop sends a server-initiated Ping every 15s until stopped; matching
// Pongs feed the client's smoothed RTT.
func (s *TCPServer) pingLoop(client *Client, stop <-chan struct{}) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.configMu.RLock()
			timeout := s.cfg.ClientTimeoutSeconds
			s.configMu.RUnlock()
			last := client.lastActivity()
			if timeout > 0 && !last.IsZero() && time.Since(last) > time.Duration(timeout)*time.Second {
				_ = client.Conn.Close()
				return
			}
			client.notePingSent()
			if err := s.writeMessage(client, netproto.MsgPing, netproto.Ping{}); err != nil {
				return
			}
		}
	}
}

// onDisconnect performs the post-disconnect cleanup for an authenticated
// client: remove it from the state manager and the broadcaster, announce the
// departure to the remaining clients, and trigger the temp-channel cleanup
// check for the channel it left.
func (s *TCPServer) onDisconnect(client *Client) {
	if !client.needsDisconnectCleanup() || s.deps == nil {
		return
	}
	if s.deps.Authority != nil && s.deps.FileTransfer != nil {
		s.deps.FileTransfer.RevokeTransfers(func(p filetransfer.Principal, _ int64, _ string) bool {
			return p.SessionID != client.ID
		})
	}

	var channelID int64
	var wasInvisible bool
	var alreadyCleaned bool
	func() {
		if s.deps.Authority != nil {
			s.roleMetadataMu.Lock()
			defer s.roleMetadataMu.Unlock()
			client.roleActionMu.Lock()
			defer client.roleActionMu.Unlock()
		}
		if !client.needsDisconnectCleanup() {
			alreadyCleaned = true
			return
		}
		if s.deps.Authority != nil {
			client.revokeSession()
			s.unregister(client.ID)
		}
		if s.deps.State != nil {
			var removed *state.Client
			if s.deps.Channels != nil {
				removed, _ = s.deps.Channels.RemoveClient(client.ID)
			} else {
				removed, _ = s.deps.State.RemoveClient(client.ID)
			}
			if removed != nil {
				channelID = removed.ChannelID
				wasInvisible = removed.Status == "invisible"
			}
		}
		// Remove voice metadata in the same membership critical section. Key
		// rotation below acquires Authority and must run after releasing it.
		if s.deps.Voice != nil {
			if err := s.deps.Voice.ClosePeer(client.ID); err != nil {
				s.logger.Warn("voice session teardown failed", zap.String("client_id", client.ID), zap.Error(err))
			}
		}
		client.markDisconnectCleaned()
	}()
	if alreadyCleaned {
		return
	}

	if s.deps.Broadcast != nil {
		s.deps.Broadcast.Unregister(client.ID)
		if wasInvisible {
			// Invisible users were never visible to non-admins; their leave
			// is admin-only too (381).
			s.broadcastToAdmins(eventUserLeft, userEvent{
				ClientID: client.ID,
				UniqueID: client.UniqueID,
				Nickname: client.Username,
			})
		} else {
			s.broadcastEvent(eventUserLeft, userEvent{
				ClientID: client.ID,
				UniqueID: client.UniqueID,
				Nickname: client.Username,
			})
		}
	}

	// Rotate the chat key of the channel the client left (4b).
	if channelID != 0 {
		s.rotateScopeKey(context.Background(), channelID)
	}

}

// --- helpers ---------------------------------------------------------------

// metricsSink returns the configured metrics sink, or a no-op when none is
// wired.
func (s *TCPServer) metricsSink() metrics.Sink {
	if s.deps == nil || s.deps.Metrics == nil {
		return metrics.Noop{}
	}
	return s.deps.Metrics
}

// writeMessage encodes and writes a single message to the client connection.
// Writes are serialized per client so handler replies and broadcast events
// never interleave on the wire.
func (s *TCPServer) writeMessage(client *Client, mt netproto.MessageType, msg any) error {
	return s.writeMessageInContext(context.Background(), client, mt, msg)
}

func (s *TCPServer) writeMessageInContext(ctx context.Context, client *Client, mt netproto.MessageType, msg any) error {
	frame, err := netproto.Encode(mt, msg)
	if err != nil {
		return err
	}
	return s.writeFrameInContext(ctx, client, frame)
}

func (s *TCPServer) writeFrameInContext(ctx context.Context, client *Client, frame *netproto.Frame) error {
	if err := client.wmu.LockContext(ctx); err != nil {
		return err
	}
	defer client.wmu.Unlock()
	return s.writeFrameLocked(ctx, client, frame)
}

// writeFrameLocked requires client.wmu. Authentication takes that lock before
// reading its configuration baseline, so queued updates cannot overtake it.
func (s *TCPServer) writeFrameLocked(ctx context.Context, client *Client, frame *netproto.Frame) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Explicit operation/reply deadlines also apply in legacy mode. Role-mode
	// writes additionally have a five-second ceiling so they cannot hold an
	// authorization lease indefinitely and prevent revocation.
	deadline, _ := ctx.Deadline()
	if s.deps != nil && s.deps.Authority != nil {
		ceiling := time.Now().Add(5 * time.Second)
		if deadline.IsZero() || ceiling.Before(deadline) {
			deadline = ceiling
		}
	}
	if !deadline.IsZero() {
		if err := client.Conn.SetWriteDeadline(deadline); err != nil {
			return err
		}
		defer func() { _ = client.Conn.SetWriteDeadline(time.Time{}) }()
	}
	if err := netproto.WriteFrame(client.Conn, frame); err != nil {
		s.logger.Warn("write error",
			zap.String("client_id", client.ID),
			zap.String("msg_type", netproto.MessageType(frame.Type).String()),
			zap.Error(err),
		)
		return err
	}
	client.noteSent(len(frame.Payload))
	return nil
}

// sendErrorFor writes an Error frame explicitly correlated to one inbound
// request. The origin travels as an immutable argument; it is never stored on
// Client, so concurrent dispatches cannot leak one request's origin into
// another response.
func (s *TCPServer) sendErrorFor(client *Client, origin netproto.MessageType, code uint16, message string) error {
	errMsg := netproto.Error{Code: code, Message: message, OriginType: uint16(origin)}
	return s.writeMessage(client, netproto.MsgError, errMsg)
}

// sendGlobalError is for asynchronous server-originated failures, which have
// no inbound request to correlate.
func (s *TCPServer) sendGlobalError(client *Client, code uint16, message string) error {
	return s.writeMessage(client, netproto.MsgError, netproto.Error{Code: code, Message: message})
}

// register adds a client to the registry.
func (s *TCPServer) register(c *Client) {
	s.mu.Lock()
	s.clients[c.ID] = c
	s.mu.Unlock()
}

// unregister removes a client from the registry.
func (s *TCPServer) unregister(id string) {
	s.mu.Lock()
	delete(s.clients, id)
	s.mu.Unlock()
}

// clientCount returns the number of registered connections (217).
func (s *TCPServer) clientCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.clients)
}

// clientByID returns the registered client with the given ID, if any.
func (s *TCPServer) clientByID(id string) (*Client, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.clients[id]
	return c, ok
}

// clientByUniqueID returns the registered, authenticated client with the
// given unique ID, if any.
func (s *TCPServer) clientByUniqueID(uniqueID string) (*Client, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, c := range s.clients {
		if c.uniqueID() == uniqueID {
			return c, true
		}
	}
	return nil, false
}

// newClientID returns a short random hex identifier suitable for logging and
// registry keys. It is not a security-sensitive value.
func newClientID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Fallback to a timestamp-based ID if crypto/rand fails.
		return fmt.Sprintf("c-%x", time.Now().UnixNano())
	}
	return "c-" + hex.EncodeToString(b[:])
}
