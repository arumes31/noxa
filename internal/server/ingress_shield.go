package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"sync"
	"time"

	"go.uber.org/zap"

	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/config"
	"noxa/internal/netproto"
)

// Anti-raid ingress shield (#7). In "auto" mode a surge of new control
// connections (many per second, from many addresses) raises the shield for
// ingress_shield_hold; "on" keeps it raised. While raised, every login must
// first solve a proof-of-work challenge bound to its connection, so a flood
// of logins costs the attacker CPU before it costs the server a database
// lookup. The pre-auth connection caps (preauth_admission.go) still apply.

const (
	// powChallengeTTL bounds how long a challenge stays answerable.
	powChallengeTTL = 60 * time.Second
)

var (
	// errRefusedShuttingDown closes a pre-auth connection while a shutdown
	// is scheduled (#52).
	errRefusedShuttingDown = errors.New("server is shutting down")
	// errIngressShieldRejected closes a connection that cannot or did not
	// pass the shield.
	errIngressShieldRejected = errors.New("ingress shield rejected the connection")
)

// Shield overrides set through ServerQuery; "" follows the configured mode.
const (
	shieldOverrideNone = ""
	shieldOverrideOn   = "on"
	shieldOverrideOff  = "off"
)

type ingressShield struct {
	mu sync.Mutex
	// window holds the arrivals of the last second, oldest first, bounded by
	// the arrival threshold; ips counts addresses within it.
	window      []shieldArrival
	ips         map[string]int
	raisedUntil time.Time
	override    string
	reported    bool // last effective state that was logged and audited
	// audits tracks transition audit writes so shutdown can drain them.
	audits sync.WaitGroup
}

type shieldArrival struct {
	at time.Time
	ip string
}

// recordLocked adds an arrival to the rolling one-second window and reports
// whether the window is a surge. The window keeps at most limit arrivals:
// once that many arrived within a second the count threshold is met, and the
// distinct addresses among the newest limit arrivals decide the rest.
func (sh *ingressShield) recordLocked(ip string, now time.Time, settings config.IngressShieldSettings) bool {
	if sh.ips == nil {
		sh.ips = make(map[string]int)
	}
	drop := func() {
		old := sh.window[0]
		sh.window = sh.window[1:]
		if sh.ips[old.ip]--; sh.ips[old.ip] <= 0 {
			delete(sh.ips, old.ip)
		}
	}
	cutoff := now.Add(-time.Second)
	for len(sh.window) > 0 && !sh.window[0].at.After(cutoff) {
		drop()
	}
	for len(sh.window) >= settings.Arrivals {
		drop()
	}
	sh.window = append(sh.window, shieldArrival{at: now, ip: ip})
	sh.ips[ip]++
	return len(sh.window) >= settings.Arrivals && len(sh.ips) >= settings.DistinctIPs
}

// IngressShieldState is the shield's state for operators.
type IngressShieldState struct {
	Mode       string    `json:"mode"`
	Override   string    `json:"override,omitempty"`
	Active     bool      `json:"active"`
	Until      time.Time `json:"until,omitzero"`
	Difficulty int       `json:"difficulty"`
}

func (s *TCPServer) shieldSettings() config.IngressShieldSettings {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	if s.cfg == nil {
		return (&config.Config{}).IngressShieldSettings()
	}
	return s.cfg.IngressShieldSettings()
}

// activeLocked reports the effective state; the caller holds sh.mu.
func (sh *ingressShield) activeLocked(settings config.IngressShieldSettings, now time.Time) bool {
	switch sh.override {
	case shieldOverrideOn:
		return true
	case shieldOverrideOff:
		return false
	}
	switch settings.Mode {
	case config.IngressShieldOn:
		return true
	case config.IngressShieldAuto:
		return now.Before(sh.raisedUntil)
	default:
		return false
	}
}

// noteIngressArrival records a new control connection from ip and raises an
// automatic shield on a surge. Call it for every accepted connection.
func (s *TCPServer) noteIngressArrival(ip string, now time.Time) {
	settings := s.shieldSettings()
	sh := &s.shield
	sh.mu.Lock()
	// An expired shield is recorded as lowered before a new surge raises it
	// again, so every raise has its own transition.
	expiredChanged, expiredActive := s.shieldTransitionLocked(settings, now)
	surge := settings.Mode == config.IngressShieldAuto && sh.recordLocked(ip, now, settings)
	if surge {
		sh.raisedUntil = now.Add(settings.Hold)
	}
	changed, active := s.shieldTransitionLocked(settings, now)
	sh.mu.Unlock()
	if expiredChanged {
		s.reportShieldChange(expiredActive, "expired")
	}
	if changed {
		s.reportShieldChange(active, "surge")
	}
}

// shieldTransitionLocked compares the effective state with the last
// reported one; the caller holds s.shield.mu.
func (s *TCPServer) shieldTransitionLocked(settings config.IngressShieldSettings, now time.Time) (changed, active bool) {
	active = s.shield.activeLocked(settings, now)
	if active == s.shield.reported {
		return false, active
	}
	s.shield.reported = active
	return true, active
}

// ingressShieldActive reports whether logins must solve a challenge now.
func (s *TCPServer) ingressShieldActive(now time.Time) bool {
	settings := s.shieldSettings()
	s.shield.mu.Lock()
	changed, active := s.shieldTransitionLocked(settings, now)
	s.shield.mu.Unlock()
	if changed {
		s.reportShieldChange(active, "expired")
	}
	return active
}

// IngressShieldStatus returns the shield's state for ServerQuery.
func (s *TCPServer) IngressShieldStatus(now time.Time) IngressShieldState {
	settings := s.shieldSettings()
	s.shield.mu.Lock()
	changed, active := s.shieldTransitionLocked(settings, now)
	state := IngressShieldState{Mode: settings.Mode, Override: s.shield.override, Active: active, Difficulty: settings.Difficulty}
	if settings.Mode == config.IngressShieldAuto && now.Before(s.shield.raisedUntil) {
		state.Until = s.shield.raisedUntil
	}
	s.shield.mu.Unlock()
	if changed {
		s.reportShieldChange(active, "expired")
	}
	return state
}

// SetIngressShieldOverride forces the shield on or off, or ("") returns it to
// the configured mode. actor is recorded in the audit log.
func (s *TCPServer) SetIngressShieldOverride(ctx context.Context, actor, override string) (IngressShieldState, error) {
	switch override {
	case shieldOverrideNone, shieldOverrideOn, shieldOverrideOff:
	default:
		return IngressShieldState{}, fmt.Errorf("override %q must be on, off or auto", override)
	}
	now := time.Now()
	settings := s.shieldSettings()
	s.shield.mu.Lock()
	s.shield.override = override
	changed, active := s.shieldTransitionLocked(settings, now)
	s.shield.mu.Unlock()
	label := override
	if label == shieldOverrideNone {
		label = "auto"
	}
	s.audit(ctx, actor, "ingress_shield_override", "", "override="+label)
	if changed {
		s.reportShieldChange(active, "override")
	}
	return s.IngressShieldStatus(now), nil
}

// reportShieldChange logs, counts and audits a shield state change.
func (s *TCPServer) reportShieldChange(active bool, cause string) {
	event := "lowered"
	if active {
		event = "raised"
	}
	s.metricsSink().IncIngressShield(event)
	s.logger.Warn("ingress shield "+event, zap.String("cause", cause))
	if s.deps == nil || s.deps.Groups == nil {
		return
	}
	s.shield.audits.Add(1)
	go func() {
		defer s.shield.audits.Done()
		s.audit(context.Background(), "system", "ingress_shield_"+event, "", "cause="+cause)
	}()
}

// shutdownPendingSeconds is the time left before an announced shutdown, or
// 0 when none is scheduled.
func (s *TCPServer) shutdownPendingSeconds(now time.Time) int {
	at := s.shutdownAt.Load()
	if at == 0 {
		return 0
	}
	return max(shutdownSeconds(time.Unix(0, at).Sub(now)), 0)
}

// reconnectJitterSeconds spreads reconnects after a restart: more sessions
// get a wider window so they do not all return in the same second.
func (s *TCPServer) reconnectJitterSeconds() int {
	sessions := 0
	if s.deps != nil && s.deps.State != nil {
		sessions = len(s.deps.State.ListClients())
	}
	return min(max(sessions/20, 5), 60)
}

func randomSeconds(limit int) int {
	if limit <= 0 {
		return 0
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(limit)+1))
	if err != nil {
		return limit / 2
	}
	return int(n.Int64())
}

// preauthGate runs before the pre-auth login handlers. It refuses logins
// while a shutdown is scheduled (#52) and enforces the ingress shield (#7).
// handled reports that the frame was answered here and must not reach its
// handler; err closes the connection.
func (s *TCPServer) preauthGate(client *Client, f *netproto.Frame, mt netproto.MessageType, now time.Time) (handled bool, err error) {
	if mt != netproto.MsgAuthenticate && mt != netproto.MsgAuthSignature && mt != netproto.MsgDeviceRequest {
		return false, nil
	}
	shuttingDown := s.shutdownPendingSeconds(now) > 0
	shield := !shuttingDown && !client.powPassed() && s.ingressShieldActive(now)
	if !shuttingDown && !shield {
		return false, nil
	}
	if mt != netproto.MsgAuthenticate {
		if shuttingDown {
			client.markCloseCause(netproto.ControlCloseShutdown)
			return true, errRefusedShuttingDown
		}
		// A device-link request or signature cannot start before a solved
		// login on this connection while the shield is raised.
		s.metricsSink().IncIngressShield("rejected")
		_ = s.sendErrorFor(client, mt, errCodeMalformed, netproto.ReasonIngressShieldBusy)
		client.markCloseCause(netproto.ControlClosePreauthRejected)
		return true, errIngressShieldRejected
	}
	var msg netproto.Authenticate
	if err := netproto.Decode(f, &msg); err != nil {
		return false, nil // the handler reports the malformed request
	}
	if shuttingDown {
		client.markCloseCause(netproto.ControlCloseShutdown)
		if slices.Contains(msg.ClientFeatures, netproto.ClientFeatureRetryHint) {
			wait := s.shutdownPendingSeconds(now) + randomSeconds(s.reconnectJitterSeconds())
			_ = s.writeMessage(client, netproto.MsgAuthResponse, netproto.AuthResponse{Reason: netproto.RestartingReason(wait)})
		}
		// Older clients see a closed connection, which they retry like any
		// network failure; a rejection reason would stop their reconnects.
		return true, errRefusedShuttingDown
	}
	if len(msg.PowPrefix) > 0 {
		if client.consumePowChallenge(msg.PowPrefix, msg.PowNonce, now) {
			s.metricsSink().IncIngressShield("solved")
			return false, nil
		}
		s.metricsSink().IncIngressShield("failed")
		_ = s.writeMessage(client, netproto.MsgAuthResponse, netproto.AuthResponse{Reason: "proof of work rejected"})
		client.markCloseCause(netproto.ControlClosePreauthRejected)
		return true, errIngressShieldRejected
	}
	if !slices.Contains(msg.ClientFeatures, netproto.ClientFeaturePow) {
		s.metricsSink().IncIngressShield("rejected")
		_ = s.writeMessage(client, netproto.MsgAuthResponse, netproto.AuthResponse{Reason: netproto.ReasonIngressShieldUpdate})
		client.markCloseCause(netproto.ControlClosePreauthRejected)
		return true, errIngressShieldRejected
	}
	challenge, err := client.issuePowChallenge(s.shieldSettings().Difficulty, now)
	if err != nil {
		return true, err
	}
	s.metricsSink().IncIngressShield("challenged")
	return true, s.writeMessage(client, netproto.MsgPowChallenge, challenge)
}

// powState is a connection's proof-of-work progress, guarded by Client.mu.
type powState struct {
	prefix     []byte
	difficulty int
	expires    time.Time
	passed     bool
	window     time.Time // pre-auth deadline granted by the first challenge
}

// powLoginDeadline is the extended pre-auth deadline for a connection that
// received a challenge, or zero.
func (c *Client) powLoginDeadline() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pow.window
}

// drainShieldAudits waits (bounded by ctx) for pending transition audits.
func (s *TCPServer) drainShieldAudits(ctx context.Context) {
	done := make(chan struct{})
	go func() {
		s.shield.audits.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

func (c *Client) powPassed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pow.passed
}

// issuePowChallenge replaces any earlier challenge on this connection.
func (c *Client) issuePowChallenge(difficulty int, now time.Time) (netproto.PowChallenge, error) {
	prefix := make([]byte, netproto.PowPrefixBytes)
	if _, err := rand.Read(prefix); err != nil {
		return netproto.PowChallenge{}, err
	}
	c.mu.Lock()
	window := c.pow.window
	if window.IsZero() {
		// The first challenge on a connection grants one bounded login window
		// (the read loop extends the pre-auth deadline once); later challenges
		// on the same connection never extend it again.
		window = now.Add(powChallengeTTL)
	}
	c.pow = powState{prefix: prefix, difficulty: difficulty, expires: now.Add(powChallengeTTL), window: window}
	c.mu.Unlock()
	return netproto.PowChallenge{
		Algorithm: netproto.PowAlgorithm, Prefix: prefix, Difficulty: difficulty,
		ExpiresIn: int(powChallengeTTL / time.Second),
	}, nil
}

// consumePowChallenge accepts one valid, unexpired answer to this
// connection's challenge; any answer uses the challenge up.
func (c *Client) consumePowChallenge(prefix []byte, nonce uint64, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.pow
	c.pow.prefix = nil
	if len(st.prefix) == 0 || now.After(st.expires) || subtle.ConstantTimeCompare(prefix, st.prefix) != 1 {
		return false
	}
	if !netproto.PowSolutionValid(st.prefix, nonce, st.difficulty) {
		return false
	}
	c.pow.passed = true
	return true
}

// shieldOverrideLabel renders an override for operators.
func shieldOverrideLabel(o string) string {
	if o == shieldOverrideNone {
		return "auto"
	}
	return o
}

// IntegrationIngressShield returns the shield state to a ServerQuery
// operator with manage_server.
func (s *TCPServer) IntegrationIngressShield(ctx context.Context, principal auth.IntegrationPrincipal) (netproto.IngressShieldStatus, error) {
	var status netproto.IngressShieldStatus
	err := s.withIntegrationPolicy(ctx, principal, func(ctx context.Context, e *authorization.RoleEvaluator) error {
		if !e.Evaluate(principal.UserID(), 0, authorization.ManageServer).Allowed {
			return authorization.ErrRoleForbidden
		}
		status = shieldStatus(s.IngressShieldStatus(time.Now()))
		return nil
	})
	return status, err
}

// SetIntegrationIngressShield forces the shield on or off, or returns it to
// the configured mode; requires manage_server and is audited.
func (s *TCPServer) SetIntegrationIngressShield(ctx context.Context, principal auth.IntegrationPrincipal, request netproto.IngressShieldSet) (netproto.IngressShieldStatus, error) {
	if !request.Valid() {
		return netproto.IngressShieldStatus{}, authorization.ErrRoleInvalid
	}
	override := request.Override
	if override == "auto" {
		override = shieldOverrideNone
	}
	var status netproto.IngressShieldStatus
	err := s.withIntegrationPolicy(ctx, principal, func(ctx context.Context, e *authorization.RoleEvaluator) error {
		if !e.Evaluate(principal.UserID(), 0, authorization.ManageServer).Allowed {
			return authorization.ErrRoleForbidden
		}
		state, err := s.SetIngressShieldOverride(ctx, principal.UniqueID(), override)
		if err != nil {
			return err
		}
		status = shieldStatus(state)
		return nil
	})
	return status, err
}

func shieldStatus(state IngressShieldState) netproto.IngressShieldStatus {
	status := netproto.IngressShieldStatus{Mode: state.Mode, Override: shieldOverrideLabel(state.Override), Active: state.Active, Difficulty: state.Difficulty}
	if !state.Until.IsZero() {
		status.Until = state.Until.Unix()
	}
	return status
}
