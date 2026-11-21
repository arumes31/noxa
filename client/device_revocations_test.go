package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"noxa/internal/auth"
	"noxa/internal/netproto"
)

// A revocation made on this desktop is recorded locally, independently of a
// secure-messaging session, and filters this account's device list (cached or
// freshly verified) for every send path.
func TestOwnDeviceRevocationFiltersDeviceListAndReachesRatchet(t *testing.T) {
	cm, pub, _ := deviceCopyManager(t)
	phone := newTestDevice(t, "own-phone", "web")
	tablet := newTestDevice(t, "own-tablet", "web")
	_, epoch, _ := cm.dmDevices.get("me", time.Now())
	cm.dmDevices.put("me", []dmTrustedDevice{{DeviceID: "self", X25519: pub, First: true}, {DeviceID: phone.id, X25519: phone.xPublic}, {DeviceID: tablet.id, X25519: tablet.xPublic}}, epoch, time.Now())

	if err := cm.persistOwnDeviceRevocation(phone.id, nil); err != nil {
		t.Fatal(err)
	}
	// persist forgets the cache; a replayed list still naming the phone is filtered.
	_, epoch, _ = cm.dmDevices.get("me", time.Now())
	cm.dmDevices.put("me", []dmTrustedDevice{{DeviceID: "self", X25519: pub, First: true}, {DeviceID: phone.id, X25519: phone.xPublic}, {DeviceID: tablet.id, X25519: tablet.xPublic}}, epoch, time.Now())
	devices, err := cm.dmDeviceList("me", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range devices {
		if d.DeviceID == phone.id {
			t.Fatal("a locally revoked device stayed a send target")
		}
	}
	if len(devices) != 2 {
		t.Fatalf("devices = %+v", devices)
	}
	// Own device and empty IDs are never recorded.
	if err := cm.persistOwnDeviceRevocation("self", nil); err != nil {
		t.Fatal(err)
	}
	if revoked, err := cm.ownRevokedDevices(); err != nil || len(revoked.Devices) != 1 || revoked.Devices[0] != phone.id {
		t.Fatalf("revoked = %v, %v", revoked, err)
	}

	// A session started later receives the recorded revocation.
	noxaPub, _, _ := ed25519.GenerateKey(rand.Reader)
	consumer, err := newTestRatchetConsumer(t, func(netproto.OlmAdmissionRequest) (json.RawMessage, error) { return json.RawMessage("[]"), nil }, 300)
	if err != nil {
		t.Fatal(err)
	}
	consumer.d.DeviceKey = func(context.Context, string, string) (ed25519.PublicKey, error) { return noxaPub, nil }
	if err := cm.syncOwnRevocationsToRatchet(consumer); err != nil {
		t.Fatal(err)
	}
	if _, err := consumer.trust("me", phone.id); ratchetCode(err) != "revoked" {
		t.Fatalf("ratchet trust of a recorded revocation: %v", err)
	}
	if _, err := consumer.trust("me", tablet.id); err != nil {
		t.Fatalf("unrevoked device: %v", err)
	}
}

// relistedDevice is an account whose desktop approved a phone, and a server
// whose answers the test sets: the key directory, the device list, and a
// count of the endorsements this desktop signed for target.
type relistedDevice struct {
	cm        *connManager
	first     netproto.DMDevice
	phone     *testDevice
	approved  netproto.DMDevice // the phone as the directory listed it when approved
	directory netproto.KeyResponse
	listing   []netproto.DeviceInfo
	target    netproto.DeviceInfo
	endorsed  atomic.Int32
}

func newRelistedDevice(t *testing.T) *relistedDevice {
	t.Helper()
	s := &relistedDevice{}
	_, cm := newPipedApp(t, func(f *netproto.Frame) (netproto.MessageType, any, bool) {
		if f.Type == uint16(netproto.MsgKeyRequest) {
			return netproto.MsgKeyResponse, s.directory, true
		}
		var req netproto.DeviceRequest
		if err := netproto.Decode(f, &req); err != nil {
			t.Error(err)
		}
		switch req.Action {
		case netproto.DeviceActionList:
			return netproto.MsgDeviceResult, netproto.DeviceResult{Action: req.Action, Devices: s.listing}, true
		case netproto.DeviceActionRevoke:
			return netproto.MsgDeviceResult, netproto.DeviceResult{Action: req.Action}, true
		case netproto.DeviceActionEndorse:
			id, _ := s.cm.identity()
			root, _ := auth.LoadPublicKey(id.PublicKey)
			if req.DeviceID == s.target.DeviceID && ed25519.Verify(root, netproto.DeviceApprovalTranscript("account", "self", s.target.Ed25519Public, s.target.X25519Public), req.Signature) {
				s.endorsed.Add(1)
			}
			return netproto.MsgDeviceResult, netproto.DeviceResult{Action: req.Action, Device: &s.target}, true
		}
		return 0, nil, false
	})
	cm.mu.Lock()
	cm.uniqueID, cm.deviceID, cm.supportsDevices, cm.supportsDMDevices = "account", "self", true, true
	cm.addr = "chat.example.test:9987"
	cm.mu.Unlock()
	s.cm = cm
	id, err := cm.identity()
	if err != nil {
		t.Fatal(err)
	}
	x, _, err := id.x25519()
	if err != nil {
		t.Fatal(err)
	}
	s.first = netproto.DMDevice{DeviceID: "self", Kind: "desktop", FirstDevice: true, Ed25519Public: id.PublicKey, X25519Public: base64.StdEncoding.EncodeToString(x[:])}
	s.phone = newTestDevice(t, "phone", "web")
	approval, err := auth.SignChallenge(id.PrivateKey, netproto.DeviceApprovalTranscript("account", "self", s.phone.deviceInfo.Ed25519Public, s.phone.deviceInfo.X25519Public))
	if err != nil {
		t.Fatal(err)
	}
	s.approved = s.phone.deviceInfo
	s.approved.ApprovedBy, s.approved.ApprovalSignature = "self", approval
	return s
}

// revoke signs the phone out the way the device dialog does: the dialog has
// verified the account's device list (verified is false for a desktop that
// has no such list), then the user revokes.
func (s *relistedDevice) revoke(t *testing.T, verified bool) {
	t.Helper()
	if verified {
		s.directory = netproto.KeyResponse{UniqueID: "account", Devices: []netproto.DMDevice{s.first, s.approved}}
		if devices, err := s.cm.dmDeviceList("account", true); err != nil || len(devices) != 2 {
			t.Fatalf("verified device list before the revocation = %+v, %v", devices, err)
		}
	}
	s.listing = []netproto.DeviceInfo{{DeviceID: "phone", Kind: "web", Ed25519Public: s.phone.deviceInfo.Ed25519Public, X25519Public: s.phone.deviceInfo.X25519Public}}
	if err := s.cm.revokeOwnDevice("phone"); err != nil {
		t.Fatal(err)
	}
}

// relisted is the phone's unchanged keys and approval under deviceID.
func (s *relistedDevice) relisted(deviceID string) netproto.DMDevice {
	d := s.approved
	d.DeviceID = deviceID
	return d
}

func (s *relistedDevice) phoneIsSendTarget(t *testing.T, listedID string) bool {
	t.Helper()
	s.directory = netproto.KeyResponse{UniqueID: "account", Devices: []netproto.DMDevice{s.first, s.relisted(listedID)}}
	devices, err := s.cm.dmDeviceList("account", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range devices {
		if d.X25519 == s.phone.xPublic {
			return true
		}
	}
	return false
}

// A device revoked on this desktop stays out of the account's device list
// when the directory lists its keys and its earlier approval again, under the
// same device ID or under another one.
func TestOwnDeviceRevocationHoldsWhenTheKeysAreListedUnderAnotherID(t *testing.T) {
	for _, listedID := range []string{"phone", "phone-2"} {
		t.Run("listed as "+listedID, func(t *testing.T) {
			s := newRelistedDevice(t)
			s.revoke(t, true)
			revoked, err := s.cm.ownRevokedDevices()
			if err != nil || len(revoked.Devices) != 1 || !bytes.Equal(revoked.SigningKeys["phone"], s.phone.signingKey(t)) {
				t.Fatalf("revocation record = %+v, %v; want the phone's ID and signing key", revoked, err)
			}
			if s.phoneIsSendTarget(t, listedID) {
				t.Fatalf("the revoked device's key is a send target again as %q", listedID)
			}
		})
	}
}

// Records written before signing keys were stored, and revocations made
// without a verified device list, name the device ID only: they load and
// still revoke that ID.
func TestOwnDeviceRevocationWithoutKeyStillRevokesTheID(t *testing.T) {
	t.Run("record of an older version", func(t *testing.T) {
		s := newRelistedDevice(t)
		path, err := s.cm.ownRevocationsPath()
		if err != nil {
			t.Fatal(err)
		}
		if err := writePrivateFileAtomic(path, []byte(`{"version":1,"devices":["phone"]}`)); err != nil {
			t.Fatal(err)
		}
		if s.phoneIsSendTarget(t, "phone") {
			t.Fatal("an ID-only record no longer revokes the device")
		}
	})
	t.Run("no verified list", func(t *testing.T) {
		s := newRelistedDevice(t)
		s.revoke(t, false)
		revoked, err := s.cm.ownRevokedDevices()
		if err != nil || len(revoked.Devices) != 1 || len(revoked.SigningKeys) != 0 {
			t.Fatalf("revocation record = %+v, %v; want the ID only", revoked, err)
		}
		if s.phoneIsSendTarget(t, "phone") {
			t.Fatal("an ID-only record does not revoke the device")
		}
	})
}

// A passkey certificate issued by a device revoked on this desktop is not
// endorsed, whatever device ID the certificate's chain lists the issuer under.
func TestOwnRevokedDeviceCannotIssuePasskeyCertificates(t *testing.T) {
	for _, tc := range []struct {
		name     string
		revoke   bool
		issuer   string
		endorsed int32
	}{
		{"issuer not revoked", false, "phone", 1},
		{"issuer revoked", true, "phone", 0},
		{"issuer revoked, listed under another ID", true, "phone-2", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newRelistedDevice(t)
			if tc.revoke {
				s.revoke(t, true)
			}
			s.directory = netproto.KeyResponse{UniqueID: "account", Devices: []netproto.DMDevice{s.first}}
			passkey, passkeyPrivate, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			spki, err := x509.MarshalPKIXPublicKey(passkey)
			if err != nil {
				t.Fatal(err)
			}
			cert := netproto.PasskeyCertificate{Algorithm: -8, Principal: "account", Issuer: tc.issuer, CredentialID: "Y3JlZA", PublicKey: spki,
				RPID: "example.test", Origin: "https://example.test", Chain: []netproto.DMDevice{s.first, s.relisted(tc.issuer)}}
			if cert.Signature, err = auth.SignChallenge(s.phone.edPrivate, cert.Transcript()); err != nil {
				t.Fatal(err)
			}
			fresh := newTestDevice(t, "new-web-device", "web")
			s.target = netproto.DeviceInfo{DeviceID: fresh.id, Kind: "web", Ed25519Public: fresh.deviceInfo.Ed25519Public, X25519Public: fresh.deviceInfo.X25519Public}
			nonce := make([]byte, 32)
			b64 := base64.RawURLEncoding.EncodeToString
			cd, _ := json.Marshal(map[string]any{"type": "webauthn.get", "origin": cert.Origin, "challenge": b64(netproto.PasskeyDeviceChallenge(nonce, s.target.Ed25519Public, s.target.X25519Public))})
			rp := sha256.Sum256([]byte(cert.RPID))
			ad := append(append([]byte{}, rp[:]...), 5, 0, 0, 0, 1)
			hash := sha256.Sum256(cd)
			sig := ed25519.Sign(passkeyPrivate, append(append([]byte{}, ad...), hash[:]...))
			assertion, _ := json.Marshal(map[string]any{"type": "public-key", "rawId": cert.CredentialID, "response": map[string]string{"clientDataJSON": b64(cd), "authenticatorData": b64(ad), "signature": b64(sig)}})
			s.target.PasskeyProof, _ = json.Marshal(netproto.PasskeyLoginProof{Certificate: cert, Nonce: nonce, Assertion: assertion})
			s.listing = []netproto.DeviceInfo{s.target}
			s.cm.mu.Lock()
			conn := s.cm.conn
			s.cm.mu.Unlock()
			s.cm.approvePasskeyDevices(conn)
			if got := s.endorsed.Load(); got != tc.endorsed {
				t.Fatalf("endorsements signed by this desktop = %d, want %d", got, tc.endorsed)
			}
		})
	}
}
