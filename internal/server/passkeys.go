// passkeys.go runs WebAuthn ceremonies over the control connection. A linked
// device registers a passkey for the account; a new phone with a synced
// passkey signs in by linking itself, and must still be approved by another
// device before it receives direct-message keys.
package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"go.uber.org/zap"

	"noxa/internal/auth"
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
	action  string
	session webauthn.SessionData
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

// takePasskeyCeremony returns and clears the pending ceremony of action.
func (c *Client) takePasskeyCeremony(action string) (webauthn.SessionData, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ceremony := c.passkey
	c.passkey = nil
	if ceremony == nil || ceremony.action != action {
		return webauthn.SessionData{}, false
	}
	return ceremony.session, true
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
		assertion, session, err := rp.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
		if err != nil {
			return s.deviceFailure(ctx, client, err)
		}
		client.setPasskeyCeremony(&passkeyCeremony{action: netproto.DeviceActionPasskeyLoginFinish, session: *session})
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
		return s.writePasskeyOptions(client, r.Action, creation)
	case netproto.DeviceActionPasskeyRegisterFinish:
		session, ok := client.takePasskeyCeremony(r.Action)
		if !ok || deviceID == "" {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "start saving the passkey again")
		}
		parsed, err := protocol.ParseCredentialCreationResponseBytes(r.WebAuthn)
		if err != nil {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid passkey response")
		}
		user, err := s.passkeyAccount(ctx, keys, principal, client.nickname())
		if err != nil {
			return s.deviceFailure(ctx, client, err)
		}
		credential, err := rp.CreateCredential(user, session, parsed)
		if err != nil {
			s.logger.Info("passkey registration rejected", zap.String("client_id", client.ID), zap.Error(err))
			return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "the passkey could not be verified")
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
			DeviceID: deviceID, Name: name, Credential: record,
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
// own key like any linked device. It carries no approval signature, so
// direct-message keys still wait for approval from another device.
func (s *TCPServer) finishPasskeyLogin(ctx context.Context, client *Client, rp *webauthn.WebAuthn, keys PasskeyStore, r netproto.DeviceRequest) error {
	session, ok := client.takePasskeyCeremony(r.Action)
	if !ok {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "start the passkey sign-in again")
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
	rows, _ := keys.PasskeysByHandle(ctx, account.handle)
	for _, row := range rows {
		if string(row.CredentialID) == string(credential.ID) {
			registeringDevice = row.DeviceID
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
		Ed25519Public: r.Ed25519PublicKey, X25519Public: r.X25519PublicKey, ApprovedBy: registeringDevice,
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
