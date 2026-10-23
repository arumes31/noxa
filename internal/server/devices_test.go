package server

import (
	"context"
	"encoding/base64"
	"net"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/broadcast"
	"noxa/internal/config"
	"noxa/internal/eventbus"
	"noxa/internal/netproto"
	"noxa/internal/store"
)

// fakeDevices is an in-memory DeviceStore with the store's semantics.
type fakeDevices struct {
	mu      sync.Mutex
	devices []store.Device
	next    int
}

func (f *fakeDevices) AddDevice(_ context.Context, d store.NewDevice, limit int) (store.Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	active := 0
	for _, existing := range f.devices {
		if existing.Ed25519Public == d.Ed25519Public {
			return store.Device{}, store.ErrDeviceKeyInUse
		}
		if existing.Principal == d.Principal && existing.Active() {
			active++
		}
	}
	if limit > 0 && active >= limit {
		return store.Device{}, store.ErrDeviceLimit
	}
	f.next++
	now := time.Now()
	device := store.Device{
		DeviceID: "dev-" + string(rune('a'+f.next)), Principal: d.Principal, UserID: d.UserID, Kind: d.Kind,
		Name: store.NormalizeDeviceName(d.Name), Ed25519Public: d.Ed25519Public, X25519Public: d.X25519Public,
		FirstDevice: active == 0, ApprovedBy: d.ApprovedBy, ApprovalSignature: d.ApprovalSignature,
		CreatedAt: now, LastSeenAt: now,
	}
	f.devices = append(f.devices, device)
	return device, nil
}

func (f *fakeDevices) find(match func(store.Device) bool) (int, bool) {
	for i, d := range f.devices {
		if match(d) {
			return i, true
		}
	}
	return 0, false
}

func (f *fakeDevices) DeviceByKey(_ context.Context, key string) (store.Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i, ok := f.find(func(d store.Device) bool { return d.Ed25519Public == key }); ok {
		return f.devices[i], nil
	}
	return store.Device{}, store.ErrDeviceNotFound
}

func (f *fakeDevices) DeviceByID(_ context.Context, principal, id string) (store.Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i, ok := f.find(func(d store.Device) bool { return d.Principal == principal && d.DeviceID == id }); ok {
		return f.devices[i], nil
	}
	return store.Device{}, store.ErrDeviceNotFound
}

func (f *fakeDevices) ListDevices(_ context.Context, principal string, includeRevoked bool) ([]store.Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.Device
	for _, d := range f.devices {
		if d.Principal == principal && (includeRevoked || d.Active()) {
			out = append(out, d)
		}
	}
	return out, nil
}

func (f *fakeDevices) TouchDevice(_ context.Context, id string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i, ok := f.find(func(d store.Device) bool { return d.DeviceID == id }); ok {
		f.devices[i].LastSeenAt = at
	}
	return nil
}

func (f *fakeDevices) RenameDevice(_ context.Context, principal, id, name string) (store.Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i, ok := f.find(func(d store.Device) bool { return d.Principal == principal && d.DeviceID == id && d.Active() }); ok {
		f.devices[i].Name = store.NormalizeDeviceName(name)
		return f.devices[i], nil
	}
	return store.Device{}, store.ErrDeviceNotFound
}

func (f *fakeDevices) RevokeDevice(_ context.Context, principal, id, reason string) (store.Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i, ok := f.find(func(d store.Device) bool { return d.Principal == principal && d.DeviceID == id && d.Active() }); ok {
		f.devices[i].RevokedAt, f.devices[i].RevokedReason = time.Now(), reason
		return f.devices[i], nil
	}
	return store.Device{}, store.ErrDeviceNotFound
}

func (f *fakeDevices) ResetDevices(_ context.Context, principal, keep, reason string) ([]store.Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.Device
	for i, d := range f.devices {
		if d.Principal == principal && d.DeviceID != keep && d.Active() {
			f.devices[i].RevokedAt, f.devices[i].RevokedReason = time.Now(), reason
			out = append(out, f.devices[i])
		}
	}
	return out, nil
}

type linkTestKeys struct {
	public, private, uniqueID, x25519 string
}

func newLinkTestKeys(t *testing.T) linkTestKeys {
	t.Helper()
	public, private, err := auth.GenerateIdentityKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	uniqueID, err := auth.UniqueIDFromPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	return linkTestKeys{public: public, private: private, uniqueID: uniqueID, x25519: base64.StdEncoding.EncodeToString(make([]byte, 32))}
}

func startDeviceTestEnv(t *testing.T) (*testEnv, *fakeDevices) {
	t.Helper()
	devices := &fakeDevices{}
	env := startTestEnvDeps(t, nil, func(cfg *config.Config) {
		cfg.Web = config.WebConfig{Enabled: true, PublicURL: "https://chat.example", MaxDevicesPerAccount: 3}
	}, func(deps *Deps) { deps.Devices = devices })
	t.Cleanup(env.stop)
	return env, devices
}

func deviceCall(t *testing.T, conn net.Conn, req netproto.DeviceRequest) netproto.DeviceResult {
	t.Helper()
	send(t, conn, netproto.MsgDeviceRequest, req)
	for {
		f := readFrame(t, conn)
		switch netproto.MessageType(f.Type) {
		case netproto.MsgDeviceResult:
			var out netproto.DeviceResult
			if err := netproto.Decode(f, &out); err != nil {
				t.Fatal(err)
			}
			return out
		case netproto.MsgError:
			var e netproto.Error
			_ = netproto.Decode(f, &e)
			t.Fatalf("%s failed: %+v", req.Action, e)
		}
	}
}

func deviceCallError(t *testing.T, conn net.Conn, req netproto.DeviceRequest) netproto.Error {
	t.Helper()
	send(t, conn, netproto.MsgDeviceRequest, req)
	f := readOfType(t, conn, netproto.MsgError)
	var e netproto.Error
	if err := netproto.Decode(f, &e); err != nil {
		t.Fatal(err)
	}
	return e
}

// keyLogin signs in with an identity key through the key-proof path the web
// app uses.
func keyLogin(t *testing.T, addr string, keys linkTestKeys) (net.Conn, netproto.AuthResponse) {
	t.Helper()
	conn := dialRetry(t, addr)
	send(t, conn, netproto.MsgAuthenticate, netproto.Authenticate{Username: keys.uniqueID, Anonymous: true, Nickname: "phone"})
	var challenge netproto.AuthChallenge
	if err := netproto.Decode(readOfType(t, conn, netproto.MsgAuthChallenge), &challenge); err != nil {
		t.Fatal(err)
	}
	signature, err := auth.SignChallenge(keys.private, challenge.Challenge)
	if err != nil {
		t.Fatal(err)
	}
	send(t, conn, netproto.MsgAuthSignature, netproto.AuthSignature{PublicKey: keys.public, Signature: signature, X25519PublicKey: keys.x25519})
	var resp netproto.AuthResponse
	if err := netproto.Decode(readOfType(t, conn, netproto.MsgAuthResponse), &resp); err != nil {
		t.Fatal(err)
	}
	return conn, resp
}

func desktopLogin(t *testing.T, addr string, keys linkTestKeys) net.Conn {
	t.Helper()
	conn := dialRetry(t, addr)
	send(t, conn, netproto.MsgAuthenticate, netproto.Authenticate{Username: "user-uid", Password: "pw", PublicKey: keys.public})
	var resp netproto.AuthResponse
	if err := netproto.Decode(readOfType(t, conn, netproto.MsgAuthResponse), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK {
		t.Fatalf("desktop login: %s", resp.Reason)
	}
	return conn
}

func linkSecret(t *testing.T, result netproto.DeviceResult) (id string, secret []byte) {
	t.Helper()
	u, err := url.Parse(result.URL)
	if err != nil || !strings.HasPrefix(result.URL, "https://chat.example/link#") {
		t.Fatalf("link URL = %q", result.URL)
	}
	fragment, err := url.ParseQuery(u.Fragment)
	if err != nil {
		t.Fatal(err)
	}
	secret, err = base64.RawURLEncoding.DecodeString(fragment.Get("k"))
	if err != nil || fragment.Get("id") != result.LinkID {
		t.Fatalf("link fragment = %q", u.Fragment)
	}
	return result.LinkID, secret
}

func claimRequest(linkID string, secret []byte, phone linkTestKeys) netproto.DeviceRequest {
	transcript := netproto.LinkClaimTranscript(linkID, phone.public, phone.x25519)
	return netproto.DeviceRequest{
		Action: netproto.DeviceActionLinkClaim, LinkID: linkID, Proof: netproto.LinkProof(secret, transcript),
		Ed25519PublicKey: phone.public, X25519PublicKey: phone.x25519, Platform: "iPhone · Safari",
	}
}

func TestDeviceLinkQRFlowSignsPhoneInAsAccount(t *testing.T) {
	env, devices := startDeviceTestEnv(t)
	desktopKeys, phone := newLinkTestKeys(t), newLinkTestKeys(t)
	desktop := desktopLogin(t, env.addr, desktopKeys)
	defer func() { _ = desktop.Close() }()

	created := deviceCall(t, desktop, netproto.DeviceRequest{Action: netproto.DeviceActionLinkCreate})
	linkID, secret := linkSecret(t, created)
	if len(created.Code) != 9 || created.Code[4] != '-' || created.State != netproto.LinkStatePending {
		t.Fatalf("link create = %+v", created)
	}

	phoneConn := dialRetry(t, env.addr)
	defer func() { _ = phoneConn.Close() }()
	claimed := deviceCall(t, phoneConn, claimRequest(linkID, secret, phone))
	if claimed.State != netproto.LinkStateClaimed || claimed.ApproverFingerprint != netproto.KeyFingerprint(desktopKeys.public) {
		t.Fatalf("claim = %+v", claimed)
	}

	var event netproto.DeviceEvent
	if err := netproto.Decode(readOfType(t, desktop, netproto.MsgDeviceEvent), &event); err != nil {
		t.Fatal(err)
	}
	if event.Event != netproto.DeviceEventLinkClaimed || event.Ed25519PublicKey != phone.public || event.Via != "qr" {
		t.Fatalf("claim event = %+v", event)
	}
	transcript := netproto.LinkClaimTranscript(linkID, phone.public, phone.x25519)
	if netproto.LinkSAS(secret, transcript) != netproto.LinkSAS(secret, netproto.LinkClaimTranscript(event.LinkID, event.Ed25519PublicKey, event.X25519PublicKey)) {
		t.Fatal("desktop and phone derive different SAS")
	}

	// A second claim of the used link fails without revealing why.
	if again := deviceCall(t, dialRetry(t, env.addr), claimRequest(linkID, secret, newLinkTestKeys(t))); again.State != netproto.LinkStateExpired {
		t.Fatalf("second claim = %+v", again)
	}
	if status := deviceCall(t, phoneConn, netproto.DeviceRequest{Action: netproto.DeviceActionLinkStatus, LinkID: linkID, Ed25519PublicKey: phone.public}); status.State != netproto.LinkStateClaimed {
		t.Fatalf("status before decision = %+v", status)
	}

	creator, err := devices.DeviceByKey(t.Context(), desktopKeys.public)
	if err != nil || !creator.FirstDevice || creator.Kind != store.DeviceKindDesktop {
		t.Fatalf("desktop device = %+v, %v", creator, err)
	}
	approval, err := auth.SignChallenge(desktopKeys.private, netproto.DeviceApprovalTranscript("user-uid", creator.DeviceID, phone.public, phone.x25519))
	if err != nil {
		t.Fatal(err)
	}
	decided := deviceCall(t, desktop, netproto.DeviceRequest{Action: netproto.DeviceActionLinkDecide, LinkID: linkID, Approve: true, Signature: approval})
	if decided.State != netproto.LinkStateApproved || decided.Device == nil || decided.Device.Kind != store.DeviceKindWeb {
		t.Fatalf("decide = %+v", decided)
	}
	if status := deviceCall(t, phoneConn, netproto.DeviceRequest{Action: netproto.DeviceActionLinkStatus, LinkID: linkID, Ed25519PublicKey: phone.public}); status.State != netproto.LinkStateApproved {
		t.Fatalf("status after approval = %+v", status)
	}

	signedIn, resp := keyLogin(t, env.addr, phone)
	defer func() { _ = signedIn.Close() }()
	if !resp.OK || resp.UniqueID != "user-uid" || resp.DeviceID != decided.Device.DeviceID {
		t.Fatalf("phone login = %+v", resp)
	}

	listed := deviceCall(t, signedIn, netproto.DeviceRequest{Action: netproto.DeviceActionList})
	if len(listed.Devices) != 2 || !listed.Devices[1].Current || listed.Devices[0].Current {
		t.Fatalf("device list = %+v", listed.Devices)
	}

	// Revoking the phone from the desktop ends its session and its key.
	deviceCall(t, desktop, netproto.DeviceRequest{Action: netproto.DeviceActionRevoke, DeviceID: decided.Device.DeviceID})
	_ = signedIn.SetReadDeadline(time.Now().Add(waitDeadline))
	for {
		if _, err := netproto.ReadFrame(signedIn); err != nil {
			break
		}
	}
	again, resp := keyLogin(t, env.addr, phone)
	defer func() { _ = again.Close() }()
	if resp.OK || !strings.Contains(resp.Reason, "signed out") {
		t.Fatalf("revoked phone login = %+v", resp)
	}
}

func TestDeviceLinkCodeClaimAndDenial(t *testing.T) {
	env, _ := startDeviceTestEnv(t)
	desktopKeys, phone := newLinkTestKeys(t), newLinkTestKeys(t)
	desktop := desktopLogin(t, env.addr, desktopKeys)
	defer func() { _ = desktop.Close() }()
	created := deviceCall(t, desktop, netproto.DeviceRequest{Action: netproto.DeviceActionLinkCreate})

	phoneConn := dialRetry(t, env.addr)
	defer func() { _ = phoneConn.Close() }()
	typed := strings.ToLower(strings.ReplaceAll(created.Code, "-", " "))
	claimed := deviceCall(t, phoneConn, netproto.DeviceRequest{
		Action: netproto.DeviceActionLinkClaim, Code: typed, Ed25519PublicKey: phone.public, X25519PublicKey: phone.x25519,
	})
	if claimed.State != netproto.LinkStateClaimed || claimed.LinkID != created.LinkID {
		t.Fatalf("code claim = %+v", claimed)
	}
	var event netproto.DeviceEvent
	if err := netproto.Decode(readOfType(t, desktop, netproto.MsgDeviceEvent), &event); err != nil || event.Via != "code" {
		t.Fatalf("claim event = %+v, %v", event, err)
	}
	denied := deviceCall(t, desktop, netproto.DeviceRequest{Action: netproto.DeviceActionLinkDecide, LinkID: created.LinkID})
	if denied.State != netproto.LinkStateDenied {
		t.Fatalf("deny = %+v", denied)
	}
	status := deviceCall(t, phoneConn, netproto.DeviceRequest{Action: netproto.DeviceActionLinkStatus, LinkID: created.LinkID, Ed25519PublicKey: phone.public})
	if status.State != netproto.LinkStateDenied {
		t.Fatalf("status after denial = %+v", status)
	}
}

func TestDeviceLinkRejectsWrongProofsAndCodes(t *testing.T) {
	env, _ := startDeviceTestEnv(t)
	desktopKeys := newLinkTestKeys(t)
	desktop := desktopLogin(t, env.addr, desktopKeys)
	defer func() { _ = desktop.Close() }()
	created := deviceCall(t, desktop, netproto.DeviceRequest{Action: netproto.DeviceActionLinkCreate})
	linkID, secret := linkSecret(t, created)

	wrong := make([]byte, len(secret))
	for i := 0; i < deviceLinkMaxProofFailures; i++ {
		conn := dialRetry(t, env.addr)
		if out := deviceCall(t, conn, claimRequest(linkID, wrong, newLinkTestKeys(t))); out.State != netproto.LinkStateExpired {
			t.Fatalf("wrong proof %d = %+v", i, out)
		}
		_ = conn.Close()
	}
	// The link is gone after too many wrong proofs, even with the right one.
	conn := dialRetry(t, env.addr)
	defer func() { _ = conn.Close() }()
	if out := deviceCall(t, conn, claimRequest(linkID, secret, newLinkTestKeys(t))); out.State != netproto.LinkStateExpired {
		t.Fatalf("claim after lockout = %+v", out)
	}

	// Wrong codes are limited per IP.
	phone := newLinkTestKeys(t)
	for i := 0; i < 5; i++ {
		out := deviceCall(t, conn, netproto.DeviceRequest{Action: netproto.DeviceActionLinkClaim, Code: "ZZZZ-ZZZZ", Ed25519PublicKey: phone.public, X25519PublicKey: phone.x25519})
		if out.State != netproto.LinkStateExpired {
			t.Fatalf("wrong code %d = %+v", i, out)
		}
	}
	e := deviceCallError(t, conn, netproto.DeviceRequest{Action: netproto.DeviceActionLinkClaim, Code: "ZZZZ-ZZZZ", Ed25519PublicKey: phone.public, X25519PublicKey: phone.x25519})
	if !strings.Contains(e.Message, "too many wrong codes") {
		t.Fatalf("code limiter error = %+v", e)
	}
}

func TestDeviceRequestsBeforeAuthAreLimitedToLinking(t *testing.T) {
	env, _ := startDeviceTestEnv(t)
	conn := dialRetry(t, env.addr)
	defer func() { _ = conn.Close() }()
	e := deviceCallError(t, conn, netproto.DeviceRequest{Action: netproto.DeviceActionList})
	if e.Code != errCodeNotAuthenticated {
		t.Fatalf("pre-auth list = %+v", e)
	}
}

func TestDeviceLimitBlocksLinkCreate(t *testing.T) {
	env, devices := startDeviceTestEnv(t)
	desktopKeys := newLinkTestKeys(t)
	desktop := desktopLogin(t, env.addr, desktopKeys)
	defer func() { _ = desktop.Close() }()
	deviceCall(t, desktop, netproto.DeviceRequest{Action: netproto.DeviceActionList})
	for i := 0; i < 2; i++ {
		if _, err := devices.AddDevice(t.Context(), store.NewDevice{Principal: "user-uid", Kind: store.DeviceKindWeb, Name: "Phone", Ed25519Public: newLinkTestKeys(t).public}, 3); err != nil {
			t.Fatal(err)
		}
	}
	e := deviceCallError(t, desktop, netproto.DeviceRequest{Action: netproto.DeviceActionLinkCreate})
	if !strings.Contains(e.Message, "maximum number of devices") {
		t.Fatalf("link create at limit = %+v", e)
	}
}

func TestNormalizeAndFormatLinkCode(t *testing.T) {
	if got := NormalizeLinkCode(" k7qm-4xpa "); got != "K7QM4XPA" {
		t.Fatalf("NormalizeLinkCode = %q", got)
	}
	if got := FormatLinkCode("K7QM4XPA"); got != "K7QM-4XPA" {
		t.Fatalf("FormatLinkCode = %q", got)
	}
	code, err := randomLinkCode()
	if err != nil || len(code) != linkCodeLength || strings.ContainsAny(code, "01ILO") {
		t.Fatalf("randomLinkCode = %q, %v", code, err)
	}
}

type fakeExpiringDevices struct {
	*fakeDevices
	warned, revoked []store.Device
}

func (f *fakeExpiringDevices) ExpireIdleDevices(context.Context, time.Time, time.Time) ([]store.Device, []store.Device, error) {
	return f.warned, f.revoked, nil
}

func TestIdleDeviceSweepWarnsAndSignsOut(t *testing.T) {
	devices := &fakeDevices{}
	expiring := &fakeExpiringDevices{fakeDevices: devices}
	env := startTestEnvDeps(t, nil, func(cfg *config.Config) {
		cfg.Web = config.WebConfig{Enabled: true, PublicURL: "https://chat.example"}
	}, func(deps *Deps) { deps.Devices = expiring })
	defer env.stop()

	desktopKeys, phone := newLinkTestKeys(t), newLinkTestKeys(t)
	desktop := desktopLogin(t, env.addr, desktopKeys)
	defer func() { _ = desktop.Close() }()
	deviceCall(t, desktop, netproto.DeviceRequest{Action: netproto.DeviceActionList})
	phoneDevice, err := devices.AddDevice(t.Context(), store.NewDevice{Principal: "user-uid", Kind: store.DeviceKindWeb, Name: "Phone", Ed25519Public: phone.public, X25519Public: phone.x25519}, 10)
	if err != nil {
		t.Fatal(err)
	}
	phoneConn, resp := keyLogin(t, env.addr, phone)
	defer func() { _ = phoneConn.Close() }()
	if !resp.OK || resp.DeviceID != phoneDevice.DeviceID {
		t.Fatalf("phone login = %+v", resp)
	}

	expiring.warned = []store.Device{phoneDevice}
	env.srv.expireIdleDevices(t.Context(), time.Now())
	var warning netproto.DeviceEvent
	if err := netproto.Decode(readOfType(t, desktop, netproto.MsgDeviceEvent), &warning); err != nil {
		t.Fatal(err)
	}
	if warning.Event != netproto.DeviceEventExpiryWarning || warning.Device.DeviceID != phoneDevice.DeviceID || warning.ExpiresAt == 0 {
		t.Fatalf("expiry warning = %+v", warning)
	}

	revoked, err := devices.RevokeDevice(t.Context(), "user-uid", phoneDevice.DeviceID, store.DeviceRevokedIdle)
	if err != nil {
		t.Fatal(err)
	}
	expiring.warned, expiring.revoked = nil, []store.Device{revoked}
	env.srv.expireIdleDevices(t.Context(), time.Now())
	var event netproto.DeviceEvent
	if err := netproto.Decode(readOfType(t, desktop, netproto.MsgDeviceEvent), &event); err != nil {
		t.Fatal(err)
	}
	if event.Event != netproto.DeviceEventRevoked || event.Reason != store.DeviceRevokedIdle {
		t.Fatalf("revocation event = %+v", event)
	}
	_ = phoneConn.SetReadDeadline(time.Now().Add(waitDeadline))
	for {
		if _, err := netproto.ReadFrame(phoneConn); err != nil {
			break
		}
	}
}

func TestDeviceChangeProjectionIsAdministratorOnly(t *testing.T) {
	var published []eventbus.Event
	env := startTestEnvDeps(t, nil, func(cfg *config.Config) {
		cfg.Web = config.WebConfig{Enabled: true, PublicURL: "https://chat.example"}
	}, func(deps *Deps) {
		deps.Devices = &fakeDevices{}
		deps.PublishIntegrationEvent = func(eventType string, data []byte) {
			published = append(published, eventbus.Event{Type: eventType, Data: data})
		}
	})
	defer env.stop()

	device, err := env.deps.Devices.AddDevice(t.Context(), store.NewDevice{Principal: "user-uid", Kind: store.DeviceKindWeb, Name: "Phone", Ed25519Public: newLinkTestKeys(t).public, X25519Public: "secret-x25519"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.srv.revokeDevice(t.Context(), "user-uid", device.DeviceID, store.DeviceRevokedByUser, "user-uid"); err != nil {
		t.Fatal(err)
	}
	if len(published) != 1 || published[0].Type != eventbus.DeviceChangedType || strings.Contains(string(published[0].Data), "secret-x25519") {
		t.Fatalf("published = %+v", published)
	}

	project := func(viewer int64) broadcast.IntegrationEvent {
		var out broadcast.IntegrationEvent
		err := env.deps.Authority.WithPolicy(t.Context(), func(e *authorization.RoleEvaluator) error {
			var err error
			out, err = env.srv.projectIntegrationEvent(t.Context(), e, viewer, "viewer", published[0])
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := project(1); got.Device == nil || got.Device.DeviceID != device.DeviceID || got.Device.Action != "revoked" || got.Device.Reason != store.DeviceRevokedByUser {
		t.Fatalf("owner projection = %+v", got.Device)
	}
	if got := project(2); got.Device != nil {
		t.Fatalf("member saw device change: %+v", got.Device)
	}
}
