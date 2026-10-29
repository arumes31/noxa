package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"noxa/internal/auth"
	"noxa/internal/netproto"
	"sync/atomic"
	"testing"
)

func TestPasskeyAutoApprovalRequiresLocallyVerifiedProof(t *testing.T) {
	for _, name := range []string{"valid", "forged", "smuggled line"} {
		valid := name == "valid"
		t.Run(name, func(t *testing.T) {
			var directory netproto.KeyResponse
			var target netproto.DeviceInfo
			var approvals atomic.Int32
			var cm *connManager
			_, cm = newPipedApp(t, func(f *netproto.Frame) (netproto.MessageType, any, bool) {
				if f.Type == uint16(netproto.MsgKeyRequest) {
					return netproto.MsgKeyResponse, directory, true
				}
				var req netproto.DeviceRequest
				if err := netproto.Decode(f, &req); err != nil {
					t.Error(err)
				}
				switch req.Action {
				case netproto.DeviceActionList:
					return netproto.MsgDeviceResult, netproto.DeviceResult{Action: req.Action, Devices: []netproto.DeviceInfo{target}}, true
				case netproto.DeviceActionEndorse:
					id, _ := cm.identity()
					root, _ := auth.LoadPublicKey(id.PublicKey)
					if req.DeviceID != target.DeviceID || !ed25519.Verify(root, netproto.DeviceApprovalTranscript("account", "self", target.Ed25519Public, target.X25519Public), req.Signature) {
						t.Error("invalid automatic endorsement")
					}
					approvals.Add(1)
					return netproto.MsgDeviceResult, netproto.DeviceResult{Action: req.Action, Device: &target}, true
				}
				t.Errorf("unexpected request %s", req.Action)
				return 0, nil, false
			})
			cm.mu.Lock()
			cm.uniqueID, cm.deviceID, cm.supportsDMDevices = "account", "self", true
			conn := cm.conn
			cm.mu.Unlock()
			id, err := cm.identity()
			if err != nil {
				t.Fatal(err)
			}
			x, _, err := id.x25519()
			if err != nil {
				t.Fatal(err)
			}
			first := netproto.DMDevice{DeviceID: "self", Kind: "desktop", FirstDevice: true, Ed25519Public: id.PublicKey, X25519Public: base64.StdEncoding.EncodeToString(x[:])}
			directory = netproto.KeyResponse{UniqueID: "account", Devices: []netproto.DMDevice{first}}
			signing, private, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			spki, err := x509.MarshalPKIXPublicKey(signing)
			if err != nil {
				t.Fatal(err)
			}
			cert := netproto.PasskeyCertificate{Algorithm: -8, Principal: "account", Issuer: "self", CredentialID: "Y3JlZA", PublicKey: spki, RPID: "example.test", Origin: "https://example.test", Chain: []netproto.DMDevice{first}}
			cert.Signature, err = auth.SignChallenge(id.PrivateKey, cert.Transcript())
			if err != nil {
				t.Fatal(err)
			}
			targetKey, _, err := auth.GenerateIdentityKeyPair()
			if err != nil {
				t.Fatal(err)
			}
			target = netproto.DeviceInfo{DeviceID: "phone", Kind: "web", Ed25519Public: targetKey, X25519Public: first.X25519Public}
			if name == "smuggled line" {
				// P3-6: a correctly proven claim whose key carries a line
				// break (base64 decoding skips it) is never signed.
				target.X25519Public = first.X25519Public[:20] + "\r\n" + first.X25519Public[20:]
			}
			nonce := make([]byte, 32)
			b64 := base64.RawURLEncoding.EncodeToString
			cd, _ := json.Marshal(map[string]any{"type": "webauthn.get", "origin": cert.Origin, "challenge": b64(netproto.PasskeyDeviceChallenge(nonce, target.Ed25519Public, target.X25519Public))})
			rp := sha256.Sum256([]byte(cert.RPID))
			ad := append(append([]byte{}, rp[:]...), 5, 0, 0, 0, 1)
			hash := sha256.Sum256(cd)
			sig := ed25519.Sign(private, append(append([]byte{}, ad...), hash[:]...))
			if name == "forged" {
				sig[0] ^= 1
			}
			assertion, _ := json.Marshal(map[string]any{"type": "public-key", "rawId": cert.CredentialID, "response": map[string]string{"clientDataJSON": b64(cd), "authenticatorData": b64(ad), "signature": b64(sig)}})
			target.PasskeyProof, _ = json.Marshal(netproto.PasskeyLoginProof{Certificate: cert, Nonce: nonce, Assertion: assertion})
			cm.approvePasskeyDevices(conn)
			want := int32(0)
			if valid {
				want = 1
			}
			if approvals.Load() != want {
				t.Fatalf("approvals=%d want=%d", approvals.Load(), want)
			}
			cm.disconnect()
			cm.approvePasskeyDevices(conn)
			if approvals.Load() != want {
				t.Fatal("old connection authorized a device")
			}
		})
	}
}
