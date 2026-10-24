// passkeys.go runs WebAuthn ceremonies over the control connection. A linked
// device registers a passkey for the account; a new phone with a synced
// passkey signs in by linking itself. A signed delegation lets a trusted
// desktop automatically verify and endorse its direct-message keys.
package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/protocol/webauthncose"
	"github.com/go-webauthn/webauthn/webauthn"
	"go.uber.org/zap"

	"noxa/internal/auth"
	"noxa/internal/deviceproof"
	"noxa/internal/netproto"
	"noxa/internal/store"
)

// passkeyCeremonyTTL bounds the time between a begin and its finish.
const passkeyCeremonyTTL = 2 * time.Minute

// PasskeyStore persists WebAuthn credentials (implemented by *store.Store).
type PasskeyStore interface {
	PasskeyUserHandle(ctx context.Context, principal string) ([]byte, error)
	AddPasskey(ctx context.Context, p store.Passkey) error
	Passkeys(ctx context.Context, principal string) ([]store.Passkey, error)
	PasskeysByHandle(ctx context.Context, handle []byte) ([]store.Passkey, error)
	RecordPasskeyUse(ctx context.Context, credentialID, credential []byte) error
	DeletePasskey(ctx context.Context, principal string, credentialID []byte) error
}

// passkeyCeremony is the pending WebAuthn session of one connection.
type passkeyCeremony struct {
	action   string
	session  webauthn.SessionData
	nonce    []byte
	edPublic string
	xPublic  string
}

// passkeyUser adapts an account to the WebAuthn library.
type passkeyUser struct {
	principal   string
	handle      []byte
	name        string
	credentials []webauthn.Credential
}

func (u *passkeyUser) WebAuthnID() []byte                         { return u.handle }
func (u *passkeyUser) WebAuthnName() string                       { return u.name }
func (u *passkeyUser) WebAuthnDisplayName() string                { return u.name }
func (u *passkeyUser) WebAuthnCredentials() []webauthn.Credential { return u.credentials }

func (s *TCPServer) passkeyStore() (PasskeyStore, bool) {
	if s.deps == nil || s.deps.Devices == nil {
		return nil, false
	}
	p, ok := s.deps.Devices.(PasskeyStore)
	return p, ok
}

func (s *TCPServer) relyingParty() (*webauthn.WebAuthn, error) {
	if !s.cfg.Web.Enabled || s.cfg.Web.RPID() == "" {
		return nil, errors.New("passkeys need the mobile web app on a domain name")
	}
	timeout := webauthn.TimeoutConfig{Enforce: true, Timeout: passkeyCeremonyTTL, TimeoutUVD: passkeyCeremonyTTL}
	return webauthn.New(&webauthn.Config{
		RPID:          s.cfg.Web.RPID(),
		RPDisplayName: s.cfg.ServerName,
		RPOrigins:     s.cfg.Web.Origins(),
		Timeouts:      webauthn.TimeoutsConfig{Login: timeout, Registration: timeout},
	})
}

func passkeyUserFor(principal, name string, handle []byte, rows []store.Passkey) (*passkeyUser, error) {
	user := &passkeyUser{principal: principal, handle: handle, name: name}
	for _, row := range rows {
		var credential webauthn.Credential
		if err := json.Unmarshal(row.Credential, &credential); err != nil {
			return nil, err
		}
		user.credentials = append(user.credentials, credential)
	}
	return user, nil
}

func passkeyInfos(rows []store.Passkey) []netproto.PasskeyInfo {
	out := make([]netproto.PasskeyInfo, 0, len(rows))
	for _, row := range rows {
		info := netproto.PasskeyInfo{
			PasskeyID: base64.RawURLEncoding.EncodeToString(row.CredentialID),
			Name:      row.Name, DeviceID: row.DeviceID, CreatedAt: row.CreatedAt.Unix(),
		}
		if !row.LastUsedAt.IsZero() {
			info.LastUsedAt = row.LastUsedAt.Unix()
		}
		out = append(out, info)
	}
	return out
}

func (c *Client) setPasskeyCeremony(ceremony *passkeyCeremony) {
	c.mu.Lock()
	c.passkey = ceremony
	c.mu.Unlock()
}

// passkeyLoginDeadline is set only after issuing a discoverable-login challenge.
func (c *Client) passkeyLoginDeadline() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.passkey == nil || c.passkey.action != netproto.DeviceActionPasskeyLoginFinish {
		return time.Time{}
	}
	return c.passkey.session.Expires
}

// takePasskeyCeremony returns and clears the pending ceremony of action.
func (c *Client) takePasskeyCeremony(action string) (*passkeyCeremony, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ceremony := c.passkey
	c.passkey = nil
	if ceremony == nil || ceremony.action != action {
		return nil, false
	}
	return ceremony, true
}

func (s *TCPServer) handlePasskeyRequest(ctx context.Context, client *Client, r netproto.DeviceRequest) error {
	keys, ok := s.passkeyStore()
	if !ok {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "passkeys are unavailable")
	}
	rp, err := s.relyingParty()
	if err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, err.Error())
	}
	switch r.Action {
	case netproto.DeviceActionPasskeyLoginBegin:
		options := []webauthn.LoginOption{webauthn.WithUserVerification(protocol.VerificationRequired)}
		var nonce []byte
		if r.Ed25519PublicKey != "" || r.X25519PublicKey != "" {
			if _, err := auth.LoadPublicKey(r.Ed25519PublicKey); err != nil {
				return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid device key")
			}
			x, err := base64.StdEncoding.DecodeString(r.X25519PublicKey)
			if err != nil || len(x) != 32 || bytes.Equal(x, make([]byte, 32)) {
				return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid encryption key")
			}
			nonce = make([]byte, 32)
			if _, err := rand.Read(nonce); err != nil {
				return s.deviceFailure(ctx, client, err)
			}
			options = append(options, webauthn.WithChallenge(netproto.PasskeyDeviceChallenge(nonce, r.Ed25519PublicKey, r.X25519PublicKey)))
		}
		assertion, session, err := rp.BeginDiscoverableLogin(options...)
		if err != nil {
			return s.deviceFailure(ctx, client, err)
		}
		client.setPasskeyCeremony(&passkeyCeremony{action: netproto.DeviceActionPasskeyLoginFinish, session: *session, nonce: nonce, edPublic: r.Ed25519PublicKey, xPublic: r.X25519PublicKey})
		return s.writePasskeyOptions(client, r.Action, assertion)
	case netproto.DeviceActionPasskeyLoginFinish:
		return s.finishPasskeyLogin(ctx, client, rp, keys, r)
	}

	principal, deviceID := client.uniqueID(), client.currentDeviceID()
	switch r.Action {
	case netproto.DeviceActionPasskeyRegisterBegin:
		if deviceID == "" || !client.isWebTransport() {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "passkeys are saved from a linked phone or browser")
		}
		user, err := s.passkeyAccount(ctx, keys, principal, client.nickname())
		if err != nil {
			return s.deviceFailure(ctx, client, err)
		}
		exclusions := make([]protocol.CredentialDescriptor, 0, len(user.credentials))
		for _, credential := range user.credentials {
			exclusions = append(exclusions, credential.Descriptor())
		}
		creation, session, err := rp.BeginRegistration(user,
			webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired),
			webauthn.WithExclusions(exclusions),
			webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
				ResidentKey: protocol.ResidentKeyRequirementRequired, UserVerification: protocol.VerificationRequired,
			}),
		)
		if err != nil {
			return s.deviceFailure(ctx, client, err)
		}
		client.setPasskeyCeremony(&passkeyCeremony{action: netproto.DeviceActionPasskeyRegisterFinish, session: *session})
		data, err := json.Marshal(creation)
		if err != nil {
			return err
		}
		chain, _ := s.dmDevices(ctx, principal)
		var certificate *netproto.PasskeyCertificate
		for _, d := range chain {
			if d.DeviceID == deviceID {
				certificate = &netproto.PasskeyCertificate{Principal: principal, Issuer: deviceID, RPID: s.cfg.Web.RPID(), Chain: chain}
				break
			}
		}
		return s.writeMessage(client, netproto.MsgDeviceResult, netproto.DeviceResult{Action: r.Action, WebAuthn: data, DMCertificate: certificate})
	case netproto.DeviceActionPasskeyRegisterFinish:
		ceremony, ok := client.takePasskeyCeremony(r.Action)
		if !ok || deviceID == "" {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "start saving the passkey again")
		}
		session := ceremony.session
		parsed, err := protocol.ParseCredentialCreationResponseBytes(r.WebAuthn)
		if err != nil {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid passkey response")
		}
		rows, err := keys.Passkeys(ctx, principal)
		if err != nil {
			return s.deviceFailure(ctx, client, err)
		}
		// The first handle is not persisted until this ceremony succeeds.
		// Reuse the handle issued at begin; asking the store again would
		// generate another random handle and invalidate the first passkey.
		user, err := passkeyUserFor(principal, client.nickname(), session.UserID, rows)
		if err != nil {
			return s.deviceFailure(ctx, client, err)
		}
		credential, err := rp.CreateCredential(user, session, parsed)
		if err != nil {
			s.logger.Info("passkey registration rejected", zap.String("client_id", client.ID), zap.Error(err))
			return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "the passkey could not be verified")
		}
		certificate, err := s.validatePasskeyCertificate(ctx, principal, deviceID, r.DMCertificate, credential, rp)
		if err != nil {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "invalid passkey device authorization")
		}
		record, err := json.Marshal(credential)
		if err != nil {
			return err
		}
		name := r.Name
		if name == "" {
			name = "Passkey"
		}
		if err := keys.AddPasskey(ctx, store.Passkey{
			CredentialID: credential.ID, Principal: principal, UserHandle: user.handle,
			DeviceID: deviceID, Name: name, Credential: record, DMCertificate: certificate,
		}); err != nil {
			return s.deviceFailure(ctx, client, err)
		}
		if d, err := s.deps.Devices.DeviceByID(ctx, principal, deviceID); err == nil {
			s.publishDeviceChange("passkey_added", principal, "", deviceInfo(d, ""))
		}
		auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		s.audit(auditCtx, principal, "passkey_add", principal, "device="+deviceID)
		return s.writePasskeyList(ctx, client, keys, r.Action)
	case netproto.DeviceActionPasskeyDelete:
		id, err := base64.RawURLEncoding.DecodeString(r.PasskeyID)
		if err != nil {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid passkey")
		}
		if err := keys.DeletePasskey(ctx, principal, id); err != nil {
			if errors.Is(err, store.ErrPasskeyNotFound) {
				return s.sendErrorFor(client, requestOrigin(ctx), errCodeNotFound, "passkey not found")
			}
			return s.deviceFailure(ctx, client, err)
		}
		return s.writePasskeyList(ctx, client, keys, r.Action)
	}
	return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid device request")
}

func (s *TCPServer) passkeyAccount(ctx context.Context, keys PasskeyStore, principal, name string) (*passkeyUser, error) {
	handle, err := keys.PasskeyUserHandle(ctx, principal)
	if err != nil {
		return nil, err
	}
	rows, err := keys.Passkeys(ctx, principal)
	if err != nil {
		return nil, err
	}
	if name == "" {
		name = principal
	}
	return passkeyUserFor(principal, name, handle, rows)
}

func (s *TCPServer) writePasskeyOptions(client *Client, action string, options any) error {
	data, err := json.Marshal(options)
	if err != nil {
		return err
	}
	return s.writeMessage(client, netproto.MsgDeviceResult, netproto.DeviceResult{Action: action, WebAuthn: data})
}

func (s *TCPServer) writePasskeyList(ctx context.Context, client *Client, keys PasskeyStore, action string) error {
	rows, err := keys.Passkeys(ctx, client.uniqueID())
	if err != nil {
		return s.deviceFailure(ctx, client, err)
	}
	return s.writeMessage(client, netproto.MsgDeviceResult, netproto.DeviceResult{Action: action, Passkeys: passkeyInfos(rows)})
}

// finishPasskeyLogin verifies a discoverable assertion and links the calling
// device's keys to the passkey's account. The device then signs in with its
// own key like any linked device. Its key-bound proof lets a trusted desktop
// issue the normal approval signature without another user prompt.
func (s *TCPServer) finishPasskeyLogin(ctx context.Context, client *Client, rp *webauthn.WebAuthn, keys PasskeyStore, r netproto.DeviceRequest) error {
	ceremony, ok := client.takePasskeyCeremony(r.Action)
	if !ok {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "start the passkey sign-in again")
	}
	session := ceremony.session
	if len(ceremony.nonce) != 0 && (r.Ed25519PublicKey != ceremony.edPublic || r.X25519PublicKey != ceremony.xPublic) {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "device keys changed during passkey sign-in")
	}
	if _, err := auth.LoadPublicKey(r.Ed25519PublicKey); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid device key")
	}
	ip := remoteIP(client.Conn)
	sourceScope := auth.LoginFailureScope("passkey:"+ip, "")
	attempt, allowed := s.loginLimiter.Reserve(sourceScope)
	if !allowed {
		s.metricsSink().IncAuthFailure("tcp", "locked_out")
		return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "too many failed sign-ins, try again later")
	}
	defer attempt.Cancel()
	parsed, err := protocol.ParseCredentialRequestResponseBytes(r.WebAuthn)
	if err != nil {
		attempt.Fail()
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid passkey response")
	}
	var account *passkeyUser
	handler := func(_, userHandle []byte) (webauthn.User, error) {
		rows, err := keys.PasskeysByHandle(ctx, userHandle)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			return nil, store.ErrPasskeyNotFound
		}
		account, err = passkeyUserFor(rows[0].Principal, rows[0].Principal, userHandle, rows)
		return account, err
	}
	_, credential, err := rp.ValidatePasskeyLogin(handler, session, parsed)
	if err != nil || account == nil {
		attempt.Fail()
		s.metricsSink().IncAuthFailure("tcp", "passkey")
		return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "the passkey was not accepted")
	}
	attempt.Succeed(sourceScope)
	if record, err := json.Marshal(credential); err == nil {
		if err := keys.RecordPasskeyUse(ctx, credential.ID, record); err != nil {
			s.logger.Warn("recording passkey use failed", zap.Error(err))
		}
	}
	if reason, err := s.banRejectReason(ctx, client, account.principal, ip); err != nil || reason != "" {
		if reason == "" {
			reason = "internal error"
		}
		return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, reason)
	}
	var registeringDevice string
	var loginProof []byte
	rows, _ := keys.PasskeysByHandle(ctx, account.handle)
	for _, row := range rows {
		if string(row.CredentialID) == string(credential.ID) {
			registeringDevice = row.DeviceID
			if len(ceremony.nonce) != 0 && len(row.DMCertificate) != 0 {
				var cert netproto.PasskeyCertificate
				if json.Unmarshal(row.DMCertificate, &cert) == nil {
					loginProof, _ = json.Marshal(netproto.PasskeyLoginProof{Certificate: cert, Nonce: ceremony.nonce, Assertion: r.WebAuthn})
					if len(loginProof) > netproto.MaxPasskeyProof {
						loginProof = nil
					}
				}
			}
		}
	}
	var userID int64
	if user, err := s.deps.Auth.LookupUser(ctx, account.principal); err == nil {
		userID = user.ID
	}
	name := r.Platform
	if name == "" {
		name = "Phone"
	}
	d, err := s.deps.Devices.AddDevice(ctx, store.NewDevice{
		Principal: account.principal, UserID: userID, Kind: store.DeviceKindWeb, Name: name,
		Ed25519Public: r.Ed25519PublicKey, X25519Public: r.X25519PublicKey, ApprovedBy: registeringDevice, PasskeyProof: loginProof,
	}, s.cfg.Web.DeviceLimit())
	if err != nil {
		return s.deviceFailure(ctx, client, err)
	}
	info := deviceInfo(d, "")
	s.notifyDevices(account.principal, netproto.DeviceEvent{Event: netproto.DeviceEventLinked, Device: &info})
	s.publishDeviceChange("linked", account.principal, "", info)
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	s.audit(auditCtx, account.principal, "passkey_login", account.principal, "device="+d.DeviceID+" passkey_device="+registeringDevice)
	return s.writeMessage(client, netproto.MsgDeviceResult, netproto.DeviceResult{Action: r.Action, State: netproto.LinkStateApproved, Device: &info})
}

// Check the delegation against the authenticated registering device and the
// credential verified by WebAuthn. Clients independently check the same chain.
func (s *TCPServer) validatePasskeyCertificate(ctx context.Context, principal, deviceID string, raw []byte, credential *webauthn.Credential, rp *webauthn.WebAuthn) ([]byte, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var cert netproto.PasskeyCertificate
	if len(raw) > netproto.MaxPasskeyProof || json.Unmarshal(raw, &cert) != nil || cert.Principal != principal || cert.Issuer != deviceID || cert.RPID != rp.Config.RPID || cert.CredentialID != base64.RawURLEncoding.EncodeToString(credential.ID) {
		return nil, deviceproof.ErrProof
	}
	originOK := false
	for _, origin := range rp.Config.RPOrigins {
		originOK = originOK || origin == cert.Origin
	}
	parsedKey, err := webauthncose.ParsePublicKey(credential.PublicKey)
	if err != nil {
		return nil, deviceproof.ErrProof
	}
	var algorithm int64
	switch key := parsedKey.(type) {
	case webauthncose.EC2PublicKeyData:
		algorithm = key.Algorithm
	case webauthncose.RSAPublicKeyData:
		algorithm = key.Algorithm
	case webauthncose.OKPPublicKeyData:
		algorithm = key.Algorithm
	default:
		return nil, deviceproof.ErrProof
	}
	if cert.Algorithm != algorithm {
		return nil, deviceproof.ErrProof
	}
	block, _ := pem.Decode([]byte(webauthncose.DisplayPublicKey(credential.PublicKey)))
	if !originOK || block == nil || !bytes.Equal(block.Bytes, cert.PublicKey) {
		return nil, deviceproof.ErrProof
	}
	devices, err := s.deps.Devices.ListDevices(ctx, principal, false)
	if err != nil {
		return nil, err
	}
	var root, issuer *store.Device
	for i := range devices {
		if devices[i].FirstDevice {
			root = &devices[i]
		}
		if devices[i].DeviceID == deviceID {
			issuer = &devices[i]
		}
	}
	if root == nil || issuer == nil || (!issuer.FirstDevice && len(issuer.ApprovalSignature) == 0) {
		return nil, deviceproof.ErrProof
	}
	pinned, err := auth.LoadPublicKey(root.Ed25519Public)
	if err != nil {
		return nil, err
	}
	signing, err := deviceproof.CertificateIssuer(cert, principal, pinned)
	expected, keyErr := auth.LoadPublicKey(issuer.Ed25519Public)
	if err != nil || keyErr != nil || !bytes.Equal(signing, expected) {
		return nil, deviceproof.ErrProof
	}
	return json.Marshal(cert)
}
