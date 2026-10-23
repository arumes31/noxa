package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/fxamacker/cbor/v2"

	"noxa/internal/config"
	"noxa/internal/netproto"
	"noxa/internal/store"
)

// fakePasskeys extends fakeDevices with an in-memory PasskeyStore.
type fakePasskeys struct {
	*fakeDevices
	mu   sync.Mutex
	rows []store.Passkey
}

func (f *fakePasskeys) PasskeyUserHandle(_ context.Context, principal string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, row := range f.rows {
		if row.Principal == principal {
			return row.UserHandle, nil
		}
	}
	handle := sha256.Sum256([]byte(principal))
	return handle[:], nil
}

func (f *fakePasskeys) AddPasskey(ctx context.Context, p store.Passkey) error {
	if d, err := f.DeviceByID(ctx, p.Principal, p.DeviceID); err != nil || !d.Active() {
		return store.ErrDeviceNotFound
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows = append(f.rows, p)
	return nil
}

func (f *fakePasskeys) Passkeys(_ context.Context, principal string) ([]store.Passkey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.Passkey
	for _, row := range f.rows {
		if row.Principal == principal {
			out = append(out, row)
		}
	}
	return out, nil
}

func (f *fakePasskeys) PasskeysByHandle(_ context.Context, handle []byte) ([]store.Passkey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.Passkey
	for _, row := range f.rows {
		if string(row.UserHandle) == string(handle) {
			out = append(out, row)
		}
	}
	return out, nil
}

func (f *fakePasskeys) RecordPasskeyUse(_ context.Context, id, credential []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, row := range f.rows {
		if string(row.CredentialID) == string(id) {
			f.rows[i].Credential = credential
		}
	}
	return nil
}

func (f *fakePasskeys) DeletePasskey(_ context.Context, principal string, id []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, row := range f.rows {
		if row.Principal == principal && string(row.CredentialID) == string(id) {
			f.rows = append(f.rows[:i], f.rows[i+1:]...)
			return nil
		}
	}
	return store.ErrPasskeyNotFound
}

// virtualAuthenticator is a minimal platform authenticator: one P-256
// resident credential, "none" attestation, UP+UV, backup eligible.
type virtualAuthenticator struct {
	key        *ecdsa.PrivateKey
	id         []byte
	userHandle []byte
	count      uint32
}

const passkeyTestOrigin = "https://chat.example"

func b64url(data []byte) string { return base64.RawURLEncoding.EncodeToString(data) }

func (a *virtualAuthenticator) authData(attested bool) []byte {
	rp := sha256.Sum256([]byte("chat.example"))
	flags := byte(0x01 | 0x04 | 0x08 | 0x10) // UP, UV, BE, BS
	if attested {
		flags |= 0x40
	}
	a.count++
	out := append([]byte{}, rp[:]...)
	out = append(out, flags)
	out = binary.BigEndian.AppendUint32(out, a.count)
	if attested {
		out = append(out, make([]byte, 16)...)
		out = binary.BigEndian.AppendUint16(out, uint16(len(a.id)))
		out = append(out, a.id...)
		cose, _ := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1,
			-2: a.key.PublicKey.X.FillBytes(make([]byte, 32)), -3: a.key.PublicKey.Y.FillBytes(make([]byte, 32))})
		out = append(out, cose...)
	}
	return out
}

func clientData(t *testing.T, kind, challenge string) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]any{"type": kind, "challenge": challenge, "origin": passkeyTestOrigin, "crossOrigin": false})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func (a *virtualAuthenticator) create(t *testing.T, options json.RawMessage) json.RawMessage {
	t.Helper()
	var creation struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
			User      struct {
				ID string `json:"id"`
			} `json:"user"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(options, &creation); err != nil {
		t.Fatal(err)
	}
	handle, err := base64.RawURLEncoding.DecodeString(creation.PublicKey.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	a.userHandle = handle
	attestation, err := cbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": a.authData(true)})
	if err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(map[string]any{
		"id": b64url(a.id), "rawId": b64url(a.id), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON":    b64url(clientData(t, "webauthn.create", creation.PublicKey.Challenge)),
			"attestationObject": b64url(attestation),
			"transports":        []string{"internal", "hybrid"},
		},
		"clientExtensionResults": map[string]any{},
	})
	return out
}

func (a *virtualAuthenticator) get(t *testing.T, options json.RawMessage) json.RawMessage {
	t.Helper()
	var request struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(options, &request); err != nil {
		t.Fatal(err)
	}
	data := clientData(t, "webauthn.get", request.PublicKey.Challenge)
	authData := a.authData(false)
	hash := sha256.Sum256(data)
	digest := sha256.Sum256(append(append([]byte{}, authData...), hash[:]...))
	signature, err := ecdsa.SignASN1(rand.Reader, a.key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(map[string]any{
		"id": b64url(a.id), "rawId": b64url(a.id), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON": b64url(data), "authenticatorData": b64url(authData),
			"signature": b64url(signature), "userHandle": b64url(a.userHandle),
		},
		"clientExtensionResults": map[string]any{},
	})
	return out
}

func TestPasskeyRegisteredOnPhoneSignsInANewPhone(t *testing.T) {
	devices := &fakeDevices{}
	keys := &fakePasskeys{fakeDevices: devices}
	env := startTestEnvDeps(t, nil, func(cfg *config.Config) {
		cfg.ServerName = "noXa test"
		cfg.Web = config.WebConfig{Enabled: true, PublicURL: passkeyTestOrigin}
	}, func(deps *Deps) { deps.Devices = keys })
	defer env.stop()

	// An already linked phone signs in. Registration requires the web
	// transport, which the test marks on the server-side session.
	phone := newLinkTestKeys(t)
	if _, err := devices.AddDevice(t.Context(), store.NewDevice{Principal: "user-uid", UserID: 2, Kind: store.DeviceKindWeb, Name: "Old phone", Ed25519Public: phone.public, X25519Public: phone.x25519}, 10); err != nil {
		t.Fatal(err)
	}
	conn, resp := keyLogin(t, env.addr, phone)
	defer func() { _ = conn.Close() }()
	if !resp.OK {
		t.Fatalf("phone login = %+v", resp)
	}
	session, ok := env.srv.clientByID(resp.ClientID)
	if !ok {
		t.Fatal("phone session not found")
	}
	session.webTransport = true

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authenticator := &virtualAuthenticator{key: key, id: []byte("credential-0001")}
	begin := deviceCall(t, conn, netproto.DeviceRequest{Action: netproto.DeviceActionPasskeyRegisterBegin})
	if !strings.Contains(string(begin.WebAuthn), `"residentKey":"required"`) {
		t.Fatalf("creation options = %s", begin.WebAuthn)
	}
	finish := deviceCall(t, conn, netproto.DeviceRequest{Action: netproto.DeviceActionPasskeyRegisterFinish, Name: "iCloud Keychain", WebAuthn: authenticator.create(t, begin.WebAuthn)})
	if len(finish.Passkeys) != 1 || finish.Passkeys[0].Name != "iCloud Keychain" || finish.Passkeys[0].PasskeyID != b64url(authenticator.id) {
		t.Fatalf("passkeys after registration = %+v", finish.Passkeys)
	}

	// A new phone with the synced passkey links itself before signing in.
	newPhone := newLinkTestKeys(t)
	fresh := dialRetry(t, env.addr)
	defer func() { _ = fresh.Close() }()
	loginBegin := deviceCall(t, fresh, netproto.DeviceRequest{Action: netproto.DeviceActionPasskeyLoginBegin})
	loginFinish := deviceCall(t, fresh, netproto.DeviceRequest{
		Action: netproto.DeviceActionPasskeyLoginFinish, WebAuthn: authenticator.get(t, loginBegin.WebAuthn),
		Ed25519PublicKey: newPhone.public, X25519PublicKey: newPhone.x25519, Platform: "Pixel · Chrome",
	})
	if loginFinish.State != netproto.LinkStateApproved || loginFinish.Device == nil || loginFinish.Device.Name != "Pixel · Chrome" {
		t.Fatalf("passkey login = %+v", loginFinish)
	}
	signedIn, newResp := keyLogin(t, env.addr, newPhone)
	defer func() { _ = signedIn.Close() }()
	if !newResp.OK || newResp.UniqueID != "user-uid" {
		t.Fatalf("new phone login = %+v", newResp)
	}
	linked, err := devices.DeviceByKey(t.Context(), newPhone.public)
	if err != nil || len(linked.ApprovalSignature) != 0 || linked.ApprovedBy == "" {
		t.Fatalf("passkey-linked device = %+v, %v", linked, err)
	}

	// A replayed assertion is rejected: the ceremony is single use.
	e := deviceCallError(t, fresh, netproto.DeviceRequest{
		Action: netproto.DeviceActionPasskeyLoginFinish, WebAuthn: authenticator.get(t, loginBegin.WebAuthn),
		Ed25519PublicKey: newLinkTestKeys(t).public, X25519PublicKey: "x",
	})
	if !strings.Contains(e.Message, "start the passkey sign-in again") {
		t.Fatalf("replayed assertion = %+v", e)
	}
}

func TestPasskeyRegistrationNeedsALinkedWebDevice(t *testing.T) {
	keys := &fakePasskeys{fakeDevices: &fakeDevices{}}
	env := startTestEnvDeps(t, nil, func(cfg *config.Config) {
		cfg.Web = config.WebConfig{Enabled: true, PublicURL: passkeyTestOrigin}
	}, func(deps *Deps) { deps.Devices = keys })
	defer env.stop()
	desktop := desktopLogin(t, env.addr, newLinkTestKeys(t))
	defer func() { _ = desktop.Close() }()
	e := deviceCallError(t, desktop, netproto.DeviceRequest{Action: netproto.DeviceActionPasskeyRegisterBegin})
	if !strings.Contains(e.Message, "linked phone or browser") {
		t.Fatalf("desktop registration = %+v", e)
	}
}
