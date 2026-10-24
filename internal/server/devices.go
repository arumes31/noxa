// devices.go manages linked devices: listing, renaming and revoking them,
// and the device-link flow behind "Other login for this server".
package server

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	"golang.org/x/time/rate"

	"noxa/internal/auth"
	"noxa/internal/eventbus"
	"noxa/internal/netproto"
	"noxa/internal/store"
)

// DeviceStore persists linked devices (implemented by *store.Store).
type DeviceStore interface {
	AddDevice(ctx context.Context, d store.NewDevice, limit int) (store.Device, error)
	DeviceByKey(ctx context.Context, ed25519Public string) (store.Device, error)
	DeviceByID(ctx context.Context, principal, deviceID string) (store.Device, error)
	ListDevices(ctx context.Context, principal string, includeRevoked bool) ([]store.Device, error)
	TouchDevice(ctx context.Context, deviceID string, at time.Time) error
	RenameDevice(ctx context.Context, principal, deviceID, name string) (store.Device, error)
	RevokeDevice(ctx context.Context, principal, deviceID, reason string) (store.Device, error)
	ResetDevices(ctx context.Context, principal, keep, reason string) ([]store.Device, error)
}

const (
	// deviceLinkTTL bounds an unclaimed link and, after a claim, the time
	// left for the decision and for the claiming device to sign in.
	deviceLinkTTL = 120 * time.Second
	// deviceLinkMaxProofFailures ends a link after this many wrong proofs.
	deviceLinkMaxProofFailures = 5
	// linkCodeAlphabet omits look-alikes (0/O, 1/I/L).
	linkCodeAlphabet = "23456789ABCDEFGHJKMNPQRSTUVWXYZ"
	linkCodeLength   = 8
)

var errDeviceRevoked = errors.New("device revoked")

// deviceLink is one pending "Other login for this server" link. Only the
// creator's session can decide it; the claiming device learns the outcome
// through link_status and then signs in with its own key.
type deviceLink struct {
	id              string
	secret          []byte
	code            string
	principal       string
	userID          int64
	creatorClientID string
	creatorDeviceID string
	creatorKey      string
	expires         time.Time
	state           string
	proofFailures   int

	claimKey      string
	claimX25519   string
	claimPlatform string
	claimVia      string
}

// deviceLinks holds pending links in memory; they never outlive 2 TTLs.
type deviceLinks struct {
	mu           sync.Mutex
	byID         map[string]*deviceLink
	byCode       map[string]string
	codeFailures map[string]*rate.Limiter
}

func newDeviceLinks() *deviceLinks {
	return &deviceLinks{
		byID:         make(map[string]*deviceLink),
		byCode:       make(map[string]string),
		codeFailures: make(map[string]*rate.Limiter),
	}
}

// sweepLocked drops links past expiry. Approved and denied links stay one
// extra TTL so the claiming device can still read the outcome.
func (l *deviceLinks) sweepLocked(now time.Time) {
	for id, link := range l.byID {
		grace := link.expires
		if link.state == netproto.LinkStateApproved || link.state == netproto.LinkStateDenied {
			grace = grace.Add(deviceLinkTTL)
		}
		if now.After(grace) {
			delete(l.byID, id)
			delete(l.byCode, link.code)
		}
	}
	if len(l.codeFailures) > 4096 {
		l.codeFailures = make(map[string]*rate.Limiter)
	}
}

func (l *deviceLinks) codeLimiterLocked(ip string) *rate.Limiter {
	limiter := l.codeFailures[ip]
	if limiter == nil {
		// Five wrong codes, then one more every two minutes.
		limiter = rate.NewLimiter(rate.Every(2*time.Minute), 5)
		l.codeFailures[ip] = limiter
	}
	return limiter
}

func randomLinkCode() (string, error) {
	buf := make([]byte, linkCodeLength)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	out := make([]byte, linkCodeLength)
	for i, b := range buf {
		// 31 symbols: the modulo bias of 256 % 31 is below 4% per symbol and
		// leaves the code well above the brute-force budget allowed by the
		// per-IP limiter and the link TTL.
		out[i] = linkCodeAlphabet[int(b)%len(linkCodeAlphabet)]
	}
	return string(out), nil
}

// NormalizeLinkCode uppercases a typed code and drops separators.
func NormalizeLinkCode(code string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(code) {
		if r == '-' || r == ' ' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// FormatLinkCode groups a code as XXXX-XXXX for display.
func FormatLinkCode(code string) string {
	if len(code) != linkCodeLength {
		return code
	}
	return code[:4] + "-" + code[4:]
}

func deviceInfo(d store.Device, currentDeviceID string) netproto.DeviceInfo {
	info := netproto.DeviceInfo{
		DeviceID:      d.DeviceID,
		Kind:          d.Kind,
		Name:          d.Name,
		Fingerprint:   netproto.KeyFingerprint(d.Ed25519Public),
		X25519Public:  d.X25519Public,
		FirstDevice:   d.FirstDevice,
		Ed25519Public: d.Ed25519Public,
		Confirmed:     d.FirstDevice || len(d.ApprovalSignature) > 0,
		ApprovedBy:    d.ApprovedBy,
		Current:       d.DeviceID == currentDeviceID,
		CreatedAt:     d.CreatedAt.Unix(),
		LastSeenAt:    d.LastSeenAt.Unix(),
	}
	if !d.ExpiryWarnedAt.IsZero() {
		info.ExpiryWarnedAt = d.ExpiryWarnedAt.Unix()
	}
	if !d.RevokedAt.IsZero() {
		info.RevokedAt = d.RevokedAt.Unix()
	}
	return info
}

func (c *Client) sessionKey() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.authSigningKey != "" {
		return c.authSigningKey
	}
	return c.verifiedGuestKey
}

func (c *Client) setDeviceID(id string) {
	c.mu.Lock()
	c.deviceID = id
	c.mu.Unlock()
}

func (c *Client) currentDeviceID() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.deviceID
}

// deviceLoginPrincipal maps a linked device key to its account. ok is false
// for keys that are not devices of another principal; a revoked device
// returns errDeviceRevoked.
func (s *TCPServer) deviceLoginPrincipal(ctx context.Context, publicKey string) (principal string, userID int64, ok bool, err error) {
	if s.deps == nil || s.deps.Devices == nil {
		return "", 0, false, nil
	}
	d, err := s.deps.Devices.DeviceByKey(ctx, publicKey)
	if errors.Is(err, store.ErrDeviceNotFound) {
		return "", 0, false, nil
	}
	if err != nil {
		return "", 0, false, err
	}
	if !d.Active() {
		return "", 0, false, errDeviceRevoked
	}
	return d.Principal, d.UserID, true, nil
}

// sessionDeviceRejection refuses sessions whose identity key belongs to a
// revoked device. It runs inside finishAuth's policy section.
func (s *TCPServer) sessionDeviceRejection(ctx context.Context, client *Client) (string, error) {
	key := client.sessionKey()
	if key == "" || s.deps == nil || s.deps.Devices == nil {
		return "", nil
	}
	d, err := s.deps.Devices.DeviceByKey(ctx, key)
	if errors.Is(err, store.ErrDeviceNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !d.Active() {
		return "this device was signed out; link it again", nil
	}
	return "", nil
}

// attachSessionDevice records which linked device a session uses. Keys that
// are not registered stay unattached until the session manages devices.
func (s *TCPServer) attachSessionDevice(ctx context.Context, client *Client, principal string) string {
	key := client.sessionKey()
	if key == "" || s.deps == nil || s.deps.Devices == nil {
		return ""
	}
	d, err := s.deps.Devices.DeviceByKey(ctx, key)
	if err != nil || !d.Active() || d.Principal != principal {
		return ""
	}
	if err := s.deps.Devices.TouchDevice(ctx, d.DeviceID, time.Now()); err != nil {
		s.logger.Warn("recording device activity failed", zap.Error(err))
	}
	client.setDeviceID(d.DeviceID)
	return d.DeviceID
}

// ensureDesktopDevice registers a desktop session's identity key as a device
// the first time it manages devices. Web sessions are only ever linked.
func (s *TCPServer) ensureDesktopDevice(ctx context.Context, client *Client) (store.Device, error) {
	if id := client.currentDeviceID(); id != "" {
		return s.deps.Devices.DeviceByID(ctx, client.uniqueID(), id)
	}
	key := client.sessionKey()
	if key == "" || client.isWebTransport() {
		return store.Device{}, store.ErrDeviceNotFound
	}
	d, err := s.deps.Devices.AddDevice(ctx, store.NewDevice{
		Principal:     client.uniqueID(),
		UserID:        client.userID(),
		Kind:          store.DeviceKindDesktop,
		Name:          "Desktop",
		Ed25519Public: key,
		X25519Public:  client.x25519(),
	}, s.cfg.Web.DeviceLimit())
	if err != nil {
		return store.Device{}, err
	}
	client.setDeviceID(d.DeviceID)
	return d, nil
}

func (s *TCPServer) handleDeviceRequest(ctx context.Context, client *Client, f *netproto.Frame) error {
	var r netproto.DeviceRequest
	if netproto.Decode(f, &r) != nil || !r.Valid() {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid device request")
	}
	if s.deps == nil || s.deps.Devices == nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "device management is unavailable")
	}
	if !client.isAuthed() {
		switch r.Action {
		case netproto.DeviceActionLinkClaim:
			return s.handleLinkClaim(ctx, client, r)
		case netproto.DeviceActionLinkStatus:
			return s.handleLinkStatus(client, r)
		case netproto.DeviceActionPasskeyLoginBegin, netproto.DeviceActionPasskeyLoginFinish:
			return s.handlePasskeyRequest(ctx, client, r)
		}
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeNotAuthenticated, "not authenticated")
	}
	if strings.HasPrefix(client.uniqueID(), "guest:") {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "sign in with an identity to manage devices")
	}
	principal := client.uniqueID()
	switch r.Action {
	case netproto.DeviceActionEndorse:
		return s.handleEndorse(ctx, client, r)
	case netproto.DeviceActionHistoryPut, netproto.DeviceActionHistoryGet, netproto.DeviceActionHistoryAck:
		return s.handleHistory(ctx, client, r)
	case netproto.DeviceActionPushConfig, netproto.DeviceActionPushSubscribe, netproto.DeviceActionPushUnsubscribe,
		netproto.DeviceActionPushPreferences, netproto.DeviceActionActivity, netproto.DeviceActionRead:
		return s.handlePushRequest(ctx, client, r)
	case netproto.DeviceActionPasskeyRegisterBegin, netproto.DeviceActionPasskeyRegisterFinish, netproto.DeviceActionPasskeyDelete:
		return s.handlePasskeyRequest(ctx, client, r)
	case netproto.DeviceActionPasskeyLoginBegin, netproto.DeviceActionPasskeyLoginFinish:
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "already authenticated")
	case netproto.DeviceActionList:
		if _, err := s.ensureDesktopDevice(ctx, client); err != nil && !errors.Is(err, store.ErrDeviceNotFound) && !errors.Is(err, store.ErrDeviceLimit) {
			return s.deviceFailure(ctx, client, err)
		}
		devices, err := s.deps.Devices.ListDevices(ctx, principal, false)
		if err != nil {
			return s.deviceFailure(ctx, client, err)
		}
		out := netproto.DeviceResult{Action: r.Action}
		current := client.currentDeviceID()
		for _, d := range devices {
			out.Devices = append(out.Devices, deviceInfo(d, current))
		}
		if keys, ok := s.passkeyStore(); ok {
			rows, err := keys.Passkeys(ctx, principal)
			if err != nil {
				return s.deviceFailure(ctx, client, err)
			}
			out.Passkeys = passkeyInfos(rows)
		}
		return s.writeMessage(client, netproto.MsgDeviceResult, out)
	case netproto.DeviceActionRename:
		d, err := s.deps.Devices.RenameDevice(ctx, principal, r.DeviceID, r.Name)
		if err != nil {
			return s.deviceFailure(ctx, client, err)
		}
		info := deviceInfo(d, "")
		s.notifyDevices(principal, netproto.DeviceEvent{Event: netproto.DeviceEventRenamed, Device: &info})
		info.Current = d.DeviceID == client.currentDeviceID()
		return s.writeMessage(client, netproto.MsgDeviceResult, netproto.DeviceResult{Action: r.Action, Device: &info})
	case netproto.DeviceActionRevoke:
		target, err := s.deps.Devices.DeviceByID(ctx, principal, r.DeviceID)
		if err != nil {
			return s.deviceFailure(ctx, client, err)
		}
		if !target.Active() {
			return s.deviceFailure(ctx, client, store.ErrDeviceNotFound)
		}
		if target.FirstDevice && target.DeviceID != client.currentDeviceID() {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "the first device cannot be signed out from another device")
		}
		d, err := s.revokeDevice(ctx, principal, r.DeviceID, store.DeviceRevokedByUser, principal)
		if err != nil {
			return s.deviceFailure(ctx, client, err)
		}
		info := deviceInfo(d, "")
		return s.writeMessage(client, netproto.MsgDeviceResult, netproto.DeviceResult{Action: r.Action, Device: &info})
	case netproto.DeviceActionLinkCreate:
		return s.handleLinkCreate(ctx, client)
	case netproto.DeviceActionLinkCancel:
		s.links.mu.Lock()
		if link := s.links.byID[r.LinkID]; link != nil && link.creatorClientID == client.ID {
			delete(s.links.byID, link.id)
			delete(s.links.byCode, link.code)
		}
		s.links.mu.Unlock()
		return s.writeMessage(client, netproto.MsgDeviceResult, netproto.DeviceResult{Action: r.Action, LinkID: r.LinkID})
	case netproto.DeviceActionLinkDecide:
		return s.handleLinkDecide(ctx, client, r)
	}
	return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid device request")
}

func (s *TCPServer) deviceFailure(ctx context.Context, client *Client, err error) error {
	switch {
	case errors.Is(err, store.ErrDeviceNotFound):
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeNotFound, "device not found")
	case errors.Is(err, store.ErrDeviceLimit):
		return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied,
			"this account has the maximum number of devices; remove one under Devices & passkeys")
	case errors.Is(err, store.ErrDeviceKeyInUse):
		return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "this device key is already registered")
	}
	s.logger.Warn("device request failed", zap.String("client_id", client.ID), zap.Error(err))
	return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "device request failed")
}

// revokeDevice revokes a device, ends its live sessions and tells the
// account's other sessions.
func (s *TCPServer) revokeDevice(ctx context.Context, principal, deviceID, reason, actor string) (store.Device, error) {
	d, err := s.deps.Devices.RevokeDevice(ctx, principal, deviceID, reason)
	if err != nil {
		return store.Device{}, err
	}
	s.afterDevicesRevoked(ctx, principal, []store.Device{d}, reason, actor)
	return d, nil
}

// ResetDevices revokes every device of the principal except keep and is
// used by administrators and integrations.
func (s *TCPServer) ResetDevices(ctx context.Context, principal, keep, actor string) ([]store.Device, error) {
	if s.deps == nil || s.deps.Devices == nil {
		return nil, errors.New("device management is unavailable")
	}
	revoked, err := s.deps.Devices.ResetDevices(ctx, principal, keep, store.DeviceRevokedByAdminReset)
	if err != nil {
		return nil, err
	}
	s.afterDevicesRevoked(ctx, principal, revoked, store.DeviceRevokedByAdminReset, actor)
	return revoked, nil
}

func (s *TCPServer) afterDevicesRevoked(ctx context.Context, principal string, revoked []store.Device, reason, actor string) {
	if len(revoked) == 0 {
		return
	}
	ids := make(map[string]struct{}, len(revoked))
	for _, d := range revoked {
		ids[d.DeviceID] = struct{}{}
		info := deviceInfo(d, "")
		s.notifyDevices(principal, netproto.DeviceEvent{Event: netproto.DeviceEventRevoked, Device: &info, Reason: reason})
		s.publishDeviceChange("revoked", principal, reason, info)
	}
	for _, c := range s.sessionsOf(principal) {
		if _, hit := ids[c.currentDeviceID()]; hit {
			c.revokeSession()
			_ = c.Conn.Close()
		}
	}
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	for _, d := range revoked {
		s.audit(auditCtx, actor, "device_revoke", principal, "device="+d.DeviceID+" name="+d.Name+" reason="+reason)
	}
}

// sessionsOf returns the authenticated sessions of a principal.
func (s *TCPServer) sessionsOf(principal string) []*Client {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*Client
	for _, c := range s.clients {
		if c.isAuthed() && c.uniqueID() == principal {
			out = append(out, c)
		}
	}
	return out
}

// deviceChange is the integration bus payload of a device change.
type deviceChange struct {
	Action   string              `json:"action"`
	UniqueID string              `json:"unique_id"`
	Reason   string              `json:"reason,omitempty"`
	Device   netproto.DeviceInfo `json:"device"`
}

// publishDeviceChange reports a device change to integrations. The
// projection admits administrators only and drops the encryption key.
func (s *TCPServer) publishDeviceChange(action, principal, reason string, info netproto.DeviceInfo) {
	if s.deps == nil || s.deps.PublishIntegrationEvent == nil {
		return
	}
	info.X25519Public, info.Ed25519Public, info.Current = "", "", false
	data, err := json.Marshal(deviceChange{Action: action, UniqueID: principal, Reason: reason, Device: info})
	if err != nil {
		return
	}
	s.deps.PublishIntegrationEvent(eventbus.DeviceChangedType, data)
}

func (s *TCPServer) notifyDevices(principal string, event netproto.DeviceEvent) {
	for _, c := range s.sessionsOf(principal) {
		if err := s.writeMessage(c, netproto.MsgDeviceEvent, event); err != nil {
			s.logger.Debug("device event delivery failed", zap.String("client_id", c.ID), zap.Error(err))
		}
	}
}

func (s *TCPServer) handleLinkCreate(ctx context.Context, client *Client) error {
	origin, err := s.cfg.Web.PublicOrigin()
	if !s.cfg.Web.Enabled || err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "this server has not enabled the mobile web app")
	}
	creator, err := s.ensureDesktopDevice(ctx, client)
	if err != nil {
		if errors.Is(err, store.ErrDeviceNotFound) {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "only a linked device can link another device")
		}
		return s.deviceFailure(ctx, client, err)
	}
	active, err := s.deps.Devices.ListDevices(ctx, client.uniqueID(), false)
	if err != nil {
		return s.deviceFailure(ctx, client, err)
	}
	if len(active) >= s.cfg.Web.DeviceLimit() {
		return s.deviceFailure(ctx, client, store.ErrDeviceLimit)
	}
	secret := make([]byte, 32)
	idBytes := make([]byte, 16)
	if _, err := rand.Read(secret); err != nil {
		return err
	}
	if _, err := rand.Read(idBytes); err != nil {
		return err
	}
	link := &deviceLink{
		id:              base64.RawURLEncoding.EncodeToString(idBytes),
		secret:          secret,
		principal:       client.uniqueID(),
		userID:          client.userID(),
		creatorClientID: client.ID,
		creatorDeviceID: creator.DeviceID,
		creatorKey:      creator.Ed25519Public,
		expires:         time.Now().Add(deviceLinkTTL),
		state:           netproto.LinkStatePending,
	}
	s.links.mu.Lock()
	s.links.sweepLocked(time.Now())
	// One pending link per device: a new one replaces the previous.
	for id, existing := range s.links.byID {
		if existing.creatorDeviceID == creator.DeviceID && existing.state == netproto.LinkStatePending {
			delete(s.links.byID, id)
			delete(s.links.byCode, existing.code)
		}
	}
	for {
		code, err := randomLinkCode()
		if err != nil {
			s.links.mu.Unlock()
			return err
		}
		if _, taken := s.links.byCode[code]; !taken {
			link.code = code
			break
		}
	}
	s.links.byID[link.id] = link
	s.links.byCode[link.code] = link.id
	s.links.mu.Unlock()

	fragment := url.Values{"id": {link.id}, "k": {base64.RawURLEncoding.EncodeToString(secret)}}
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	s.audit(auditCtx, client.uniqueID(), "device_link_create", client.uniqueID(), "device="+creator.DeviceID)
	return s.writeMessage(client, netproto.MsgDeviceResult, netproto.DeviceResult{
		Action:    netproto.DeviceActionLinkCreate,
		LinkID:    link.id,
		Secret:    secret,
		Code:      FormatLinkCode(link.code),
		URL:       origin + "/link#" + fragment.Encode(),
		ExpiresAt: link.expires.Unix(),
		State:     link.state,
	})
}

// handleLinkClaim runs before authentication. Unknown, expired and used
// links all answer the same way so a claim cannot probe link existence.
func (s *TCPServer) handleLinkClaim(ctx context.Context, client *Client, r netproto.DeviceRequest) error {
	unavailable := func() error {
		s.metricsSink().IncAuthFailure("tcp", "device_link")
		return s.writeMessage(client, netproto.MsgDeviceResult, netproto.DeviceResult{
			Action: r.Action, State: netproto.LinkStateExpired,
		})
	}
	if _, err := auth.LoadPublicKey(r.Ed25519PublicKey); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid device key")
	}
	if _, err := s.deps.Devices.DeviceByKey(ctx, r.Ed25519PublicKey); err == nil {
		return unavailable()
	} else if !errors.Is(err, store.ErrDeviceNotFound) {
		return s.deviceFailure(ctx, client, err)
	}
	ip := remoteIP(client.Conn)
	now := time.Now()
	s.links.mu.Lock()
	s.links.sweepLocked(now)
	var link *deviceLink
	via := "qr"
	if r.Code != "" {
		via = "code"
		limiter := s.links.codeLimiterLocked(ip)
		if limiter.TokensAt(now) < 1 {
			s.links.mu.Unlock()
			return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "too many wrong codes, try again later")
		}
		if id, ok := s.links.byCode[NormalizeLinkCode(r.Code)]; ok {
			link = s.links.byID[id]
		}
		if link == nil {
			limiter.AllowN(now, 1)
			s.links.mu.Unlock()
			return unavailable()
		}
	} else {
		link = s.links.byID[r.LinkID]
		if link == nil {
			s.links.mu.Unlock()
			return unavailable()
		}
		transcript := netproto.LinkClaimTranscript(link.id, r.Ed25519PublicKey, r.X25519PublicKey)
		if !hmac.Equal(r.Proof, netproto.LinkProof(link.secret, transcript)) {
			link.proofFailures++
			if link.proofFailures >= deviceLinkMaxProofFailures {
				delete(s.links.byID, link.id)
				delete(s.links.byCode, link.code)
			}
			s.links.mu.Unlock()
			return unavailable()
		}
	}
	if link.state != netproto.LinkStatePending || now.After(link.expires) {
		s.links.mu.Unlock()
		return unavailable()
	}
	link.state = netproto.LinkStateClaimed
	link.claimKey = r.Ed25519PublicKey
	link.claimX25519 = r.X25519PublicKey
	link.claimPlatform = strings.TrimSpace(r.Platform)
	link.claimVia = via
	link.expires = now.Add(deviceLinkTTL)
	delete(s.links.byCode, link.code)
	event := netproto.DeviceEvent{
		Event:            netproto.DeviceEventLinkClaimed,
		LinkID:           link.id,
		Platform:         link.claimPlatform,
		Ed25519PublicKey: link.claimKey,
		X25519PublicKey:  link.claimX25519,
		Via:              via,
	}
	creatorClientID, creatorKey, expires := link.creatorClientID, link.creatorKey, link.expires
	linkID := link.id
	s.links.mu.Unlock()

	if creator, ok := s.clientByID(creatorClientID); ok {
		if err := s.writeMessage(creator, netproto.MsgDeviceEvent, event); err != nil {
			s.logger.Debug("link claim delivery failed", zap.Error(err))
		}
	}
	return s.writeMessage(client, netproto.MsgDeviceResult, netproto.DeviceResult{
		Action:              r.Action,
		LinkID:              linkID,
		State:               netproto.LinkStateClaimed,
		ExpiresAt:           expires.Unix(),
		ApproverFingerprint: netproto.KeyFingerprint(creatorKey),
	})
}

// handleLinkStatus lets the claiming device poll for the decision. It only
// answers for the key that claimed the link.
func (s *TCPServer) handleLinkStatus(client *Client, r netproto.DeviceRequest) error {
	s.links.mu.Lock()
	s.links.sweepLocked(time.Now())
	state := netproto.LinkStateExpired
	if link := s.links.byID[r.LinkID]; link != nil && link.claimKey == r.Ed25519PublicKey {
		state = link.state
	}
	s.links.mu.Unlock()
	return s.writeMessage(client, netproto.MsgDeviceResult, netproto.DeviceResult{Action: r.Action, LinkID: r.LinkID, State: state})
}

func (s *TCPServer) handleLinkDecide(ctx context.Context, client *Client, r netproto.DeviceRequest) error {
	s.links.mu.Lock()
	s.links.sweepLocked(time.Now())
	link := s.links.byID[r.LinkID]
	if link == nil || link.creatorClientID != client.ID || link.state != netproto.LinkStateClaimed {
		s.links.mu.Unlock()
		return s.writeMessage(client, netproto.MsgDeviceResult, netproto.DeviceResult{
			Action: r.Action, LinkID: r.LinkID, State: netproto.LinkStateExpired,
		})
	}
	claim := *link
	if !r.Approve {
		link.state = netproto.LinkStateDenied
		s.links.mu.Unlock()
		auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		s.audit(auditCtx, client.uniqueID(), "device_link_deny", client.uniqueID(), "platform="+claim.claimPlatform)
		return s.writeMessage(client, netproto.MsgDeviceResult, netproto.DeviceResult{Action: r.Action, LinkID: r.LinkID, State: netproto.LinkStateDenied})
	}
	s.links.mu.Unlock()

	transcript := netproto.DeviceApprovalTranscript(claim.principal, claim.creatorDeviceID, claim.claimKey, claim.claimX25519)
	if err := auth.VerifyChallenge(claim.creatorKey, transcript, r.Signature); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "approval signature does not match this device")
	}
	name := "Phone"
	if claim.claimPlatform != "" {
		name = claim.claimPlatform
	}
	d, err := s.deps.Devices.AddDevice(ctx, store.NewDevice{
		Principal:         claim.principal,
		UserID:            claim.userID,
		Kind:              store.DeviceKindWeb,
		Name:              name,
		Ed25519Public:     claim.claimKey,
		X25519Public:      claim.claimX25519,
		ApprovedBy:        claim.creatorDeviceID,
		ApprovalSignature: r.Signature,
	}, s.cfg.Web.DeviceLimit())
	if err != nil {
		return s.deviceFailure(ctx, client, err)
	}
	s.links.mu.Lock()
	if current := s.links.byID[claim.id]; current != nil {
		current.state = netproto.LinkStateApproved
		current.expires = time.Now().Add(deviceLinkTTL)
	}
	s.links.mu.Unlock()

	info := deviceInfo(d, "")
	s.notifyDevices(claim.principal, netproto.DeviceEvent{Event: netproto.DeviceEventLinked, Device: &info})
	s.publishDeviceChange("linked", claim.principal, "", info)
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	s.audit(auditCtx, client.uniqueID(), "device_link_approve", claim.principal,
		"device="+d.DeviceID+" approved_by="+claim.creatorDeviceID+" name="+d.Name)
	return s.writeMessage(client, netproto.MsgDeviceResult, netproto.DeviceResult{
		Action: r.Action, LinkID: r.LinkID, State: netproto.LinkStateApproved, Device: &info,
	})
}
