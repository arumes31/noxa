//go:build integration

package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"go.uber.org/zap"
	"net/http"
	"net/http/httptest"
	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/config"
	"noxa/internal/e2ee/admission"
	"noxa/internal/e2ee/durable"
	"noxa/internal/e2ee/olmdir"
	"noxa/internal/e2ee/ratchet"
	"noxa/internal/netproto"
	"noxa/internal/store"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestIntegrationOlmAdmissionBrowser runs the shipped Go/WASM core, bbolt,
// packaged Chromium worker/IndexedDB and the actual framed server handlers
// against a fresh migrated PostgreSQL database. The HTTP bridge is test-only.
func TestIntegrationOlmAdmissionBrowser(t *testing.T) {
	if os.Getenv("NOXA_BROWSER_INTEGRATION") != "1" {
		t.Skip("set NOXA_BROWSER_INTEGRATION=1 after building mobile and installing Chromium")
	}
	if os.Getenv("NOXA_TEST_DATABASE_URL") == "" {
		t.Fatal("real PostgreSQL required")
	}
	ctx := t.Context()
	db := integrationManagementStore(t)
	authority, e := authorization.NewAuthority(ctx, serverRoleFixture(), func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if e != nil {
		t.Fatal(e)
	}
	srv := New(&config.Config{}, zap.NewNop(), &Deps{Authority: authority, Devices: db})
	srv.tlsFingerprint = strings.TrimSuffix(strings.Repeat("aa:", 32), ":")
	srv.chatRate = nil
	srv.olmAdmissionEnabled = true
	binding := "tls-sha256:" + strings.Repeat("a", 64)
	makeClient := func(p string) (*Client, ed25519.PrivateKey) {
		pub, priv, e := auth.GenerateIdentityKeyPair()
		if e != nil {
			t.Fatal(e)
		}
		d, e := db.AddDevice(ctx, store.NewDevice{Principal: p, Ed25519Public: pub, Kind: store.DeviceKindDesktop}, 24)
		if e != nil {
			t.Fatal(e)
		}
		key, _ := auth.LoadPrivateKey(priv)
		return &Client{UniqueID: p, authed: true, deviceID: d.DeviceID, authSigningKey: pub}, key
	}
	alice, ak := makeClient("native")
	bob, bk := makeClient("browser")
	local, e := durable.Open(t.TempDir(), durable.Namespace{Server: binding, Principal: alice.UniqueID, Device: alice.deviceID}, durable.IdentityProtection(bytes.Repeat([]byte{42}, 32)), 0)
	if e != nil {
		t.Fatal(e)
	}
	defer local.Close()
	core, e := ratchet.New(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer core.Close(context.Background())
	apply := func(r ratchet.Request) (ratchet.Response, error) { r.Key = local.CoreKey(); return core.Apply(ctx, r) }
	native, e := apply(ratchet.Request{Op: ratchet.NewAccount, Count: 1})
	if e != nil {
		t.Fatal(e)
	}
	signed := func(c *Client, key ed25519.PrivateKey, b ratchet.Bundle) olmdir.Bundle {
		now := time.Now().Unix()
		out := olmdir.Bundle{Version: olmdir.Version, Protocol: olmdir.Protocol, Server: binding, Principal: c.UniqueID, DeviceID: c.deviceID, AccountEpoch: 1, Generation: 1, Identity: b.Identity, Signing: b.Signing, IssuedAt: now, ExpiresAt: now + 3600}
		for _, k := range b.OneTimeKeys {
			out.OneTimeKeys = append(out.OneTimeKeys, olmdir.Key{ID: k.ID, Key: k.Key})
		}
		tr, _ := out.Transcript()
		out.Signature = ed25519.Sign(key, tr)
		return out
	}
	nb := signed(alice, ak, *native.Bundle)
	var bb olmdir.Bundle
	encode := func(v any) []byte { b, _ := json.Marshal(v); return b }
	read := func(refs ...durable.Ref) (durable.Snapshot, error) { return local.Read(refs...) }
	snap, e := read(durable.Ref{Kind: durable.Account, ID: "local"})
	if e != nil {
		t.Fatal(e)
	}
	kid := nb.OneTimeKeys[0].ID
	if _, e = local.CommitPublication(snap, "pub", []byte(native.Account), encode(nb), map[string]durable.OTKObligation{kid: {PublicationID: "pub", KeyID: kid, PublicKey: nb.OneTimeKeys[0].Key, ExpiresAt: nb.ExpiresAt}}); e != nil {
		t.Fatal(e)
	}
	call := func(c *Client, r netproto.OlmAdmissionRequest) (netproto.OlmAdmissionResult, error) {
		conn := &sessionResponseConn{blockingTCPConn: newBlockingTCPConn()}
		c.Conn = conn
		r.RequestID = "harness"
		if r.AccountEpoch == 0 {
			r.AccountEpoch = 1
		}
		if e := srv.handleOlmAdmission(ctx, c, &netproto.Frame{Payload: encode(r)}); e != nil {
			return netproto.OlmAdmissionResult{}, e
		}
		frame, e := netproto.ReadFrame(&conn.output)
		if e != nil {
			return netproto.OlmAdmissionResult{}, e
		}
		var result netproto.OlmAdmissionResult
		if e = json.Unmarshal(frame.Payload, &result); e != nil {
			return result, e
		}
		if result.RequestID != r.RequestID {
			return result, fmt.Errorf("uncorrelated frame: %s", frame.Payload)
		}
		if result.Error != "" {
			return result, fmt.Errorf("admission %s: %s", r.Action, result.Error)
		}
		return result, nil
	}
	publish := func(c *Client, b olmdir.Bundle) error {
		conn := &sessionResponseConn{blockingTCPConn: newBlockingTCPConn()}
		c.Conn = conn
		r := netproto.OlmDirectoryRequest{RequestID: "publish", Action: "publish", Bundle: &b}
		if e := srv.handleOlmDirectory(ctx, c, &netproto.Frame{Payload: encode(r)}); e != nil {
			return e
		}
		f, e := netproto.ReadFrame(&conn.output)
		if e != nil {
			return e
		}
		var out netproto.OlmDirectoryResult
		json.Unmarshal(f.Payload, &out)
		if out.Error != "" {
			return fmt.Errorf("publication: %s", out.Error)
		}
		return nil
	}
	decode := func(v any, out any) error { return json.Unmarshal(encode(v), out) }
	var nativeSession string
	sessionContext := func() durable.SessionContext {
		return durable.SessionContext{LocalAccountID: "local", LocalEpoch: 1, PeerEpoch: 1, PeerPrincipal: bob.UniqueID, PeerDevice: bob.deviceID, PeerIdentity: bb.Identity, Scope: durable.DevicePairScope, Protocol: ratchet.Version}
	}
	var mu sync.Mutex
	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var req struct {
			Action  string                       `json:"action"`
			Bundle  ratchet.Bundle               `json:"bundle"`
			Request netproto.OlmAdmissionRequest `json:"request"`
		}
		if e := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); e != nil {
			http.Error(w, e.Error(), 400)
			return
		}
		run := func() (any, error) {
			switch req.Action {
			case "identity":
				return map[string]string{"server": binding, "principal": bob.UniqueID, "device": bob.deviceID}, nil
			case "browser_publish":
				return nil, publish(bob, bb)
			case "setup":
				bb = signed(bob, bk, req.Bundle)
				if e := publish(alice, nb); e != nil {
					return nil, e
				}
				sn, e := read(durable.Ref{Kind: durable.Trust, ID: "browser"})
				if e != nil {
					return nil, e
				}
				if e = local.SetTrust(sn, "browser", encode(bb), false); e != nil {
					return nil, e
				}
				return map[string]any{"native": nb, "browser": bb, "native_signing": ak.Public().(ed25519.PublicKey), "browser_signing": bk.Public().(ed25519.PublicKey)}, nil
			case "prepare_reset":
				next := signed(bob, bk, req.Bundle)
				next.AccountEpoch = 2
				next.Generation = 2
				tr, e := next.Transcript()
				if e != nil {
					return nil, e
				}
				next.Signature = ed25519.Sign(bk, tr)
				digest, e := olmdir.PublicationDigest(next)
				if e != nil {
					return nil, e
				}
				reset, e := (olmdir.Reset{Version: olmdir.ResetVersion, Server: binding, Principal: bob.UniqueID, Device: bob.deviceID, TransitionID: "browser-reset", OldEpoch: 1, OldIdentity: bb.Identity, OldSigning: bb.Signing, NewEpoch: 2, NewIdentity: next.Identity, NewSigning: next.Signing, Generation: 2, PublicationDigest: digest, Reason: "restored"}).Sign(bk)
				if e != nil {
					return nil, e
				}
				return map[string]any{"reset": reset, "bundle": next}, nil
			case "browser_request":
				return call(bob, req.Request)
			case "native_send":
				res, e := call(alice, netproto.OlmAdmissionRequest{Action: "snapshot", Scope: "direct", Peer: bob.UniqueID})
				if e != nil {
					return nil, e
				}
				var roster store.OlmRosterSnapshot
				if e = decode(res.Data, &roster); e != nil {
					return nil, e
				}
				deadline := time.Now().Unix() + 300
				claim, e := db.ClaimOlmKey(ctx, alice.UniqueID, alice.deviceID, bob.UniqueID, bob.deviceID, "native-claim", deadline, binding, 1)
				if e != nil {
					return nil, e
				}
				if _, e = claim.Bundle.VerifyReservedClaim(bk.Public().(ed25519.PublicKey), binding, bob.UniqueID, bob.deviceID, bb.Identity, claim.KeyID, deadline, time.Now()); e != nil {
					return nil, e
				}
				m := admission.Manifest{Version: admission.Version, Server: binding, Sender: alice.UniqueID, Device: alice.deviceID, AccountEpoch: 1, AttemptID: "1", LogicalID: "native-message", Purpose: "chat", Scope: "direct", Peer: bob.UniqueID, Deadline: deadline, Rosters: roster.Rosters, Targets: roster.Targets}
				target := admission.Target{Principal: bob.UniqueID, Device: bob.deviceID, AccountEpoch: 1}
				body, _ := admission.SealPayload(m, target, json.RawMessage(`{"text":"native to browser","mentions":{"User-1":{"é":1}}}`))
				bind := admission.ExpectedBinding(m, target)
				sent, e := apply(ratchet.Request{Op: ratchet.Outbound, Account: native.Account, PeerIdentity: bb.Identity, OneTimeKey: bb.OneTimeKeys[0].Key, Binding: &bind, Plaintext: body})
				if e != nil {
					return nil, e
				}
				nativeSession = sent.Session
				parts, e := admission.Freeze(&m, []admission.Copy{{Principal: bob.UniqueID, Device: bob.deviceID, AccountEpoch: 1, SessionID: sent.SessionID, ClaimID: "native-claim", Envelope: encode(sent.Envelope)}})
				if e != nil {
					return nil, e
				}
				sn, e := read(durable.Ref{Kind: durable.Trust, ID: "browser"}, durable.Ref{Kind: durable.Session, ID: "browser"}, durable.Ref{Kind: durable.Logical, ID: m.LogicalID})
				if e != nil {
					return nil, e
				}
				if _, e = local.CommitAttempt(sn, m, parts, []durable.SendCandidate{{SessionID: "browser", TrustID: "browser", State: []byte(sent.Session), Envelope: encode(sent.Envelope), Context: sessionContext()}}, []byte(body)); e != nil {
					return nil, e
				}
				if _, e = call(alice, netproto.OlmAdmissionRequest{Action: "begin", Manifest: &m}); e != nil {
					return nil, e
				}
				for i, p := range parts {
					if _, e = call(alice, netproto.OlmAdmissionRequest{Action: "put_part", AttemptID: "1", Part: i, Data: p}); e != nil {
						return nil, e
					}
				}
				res, e = call(alice, netproto.OlmAdmissionRequest{Action: "finalize", AttemptID: "1"})
				if e != nil {
					return nil, e
				}
				var receipt admission.Receipt
				decode(res.Data, &receipt)
				if receipt.State != "accepted" {
					return nil, fmt.Errorf("not accepted: %+v", receipt)
				}
				// Discarded response, reconnect/status and exact immutable retry.
				retry, e := call(alice, netproto.OlmAdmissionRequest{Action: "status", AttemptID: "1"})
				if e != nil || !bytes.Equal(encode(res.Data), encode(retry.Data)) {
					return nil, fmt.Errorf("uncertain retry changed: %v", e)
				}
				sn, e = read(durable.Ref{Kind: durable.Outbox, ID: "1:1"}, durable.Ref{Kind: durable.Logical, ID: m.LogicalID})
				if e != nil {
					return nil, e
				}
				if e = local.ApplyAttemptResult(sn, "1:1", receipt); e != nil {
					return nil, e
				}
				ack, e := local.AttemptRetirementRequest(sn.Fence, "1:1")
				if e != nil {
					return nil, e
				}
				if _, e = call(alice, netproto.OlmAdmissionRequest{Action: "retire_receipt", AttemptID: ack.AttemptID, Digest: ack.Digest}); e != nil {
					return nil, e
				}
				// Delete only expired public advertisements through the production sweep.
				if _, e = db.DB().ExecContext(ctx, `UPDATE olm_directory_publications SET expires_at=now()-interval '2 days'`); e != nil {
					return nil, e
				}
				if _, e = db.ExpireOlmDirectory(ctx); e != nil {
					return nil, e
				}
				return call(bob, netproto.OlmAdmissionRequest{Action: "deliveries"})
			case "native_receive":
				result, e := call(alice, netproto.OlmAdmissionRequest{Action: "deliveries"})
				if e != nil {
					return nil, e
				}
				var page store.OlmDeliveryPage
				if e = decode(result.Data, &page); e != nil || len(page.Deliveries) != 1 {
					return nil, fmt.Errorf("native queue: %v %+v", e, page)
				}
				d := page.Deliveries[0]
				if e = d.SourceBundle.VerifyEvidence(bk.Public().(ed25519.PublicKey), binding, bob.UniqueID, bob.deviceID); e != nil {
					return nil, e
				}
				if e = admission.CheckEnvelope(d.Manifest, d.Copy); e != nil {
					return nil, e
				}
				var env ratchet.Envelope
				json.Unmarshal(d.Copy.Envelope, &env)
				bind := admission.ExpectedBinding(d.Manifest, admission.Target{Principal: alice.UniqueID, Device: alice.deviceID, AccountEpoch: 1})
				received, e := apply(ratchet.Request{Op: ratchet.Decrypt, Session: nativeSession, Binding: &bind, Envelope: &env})
				if e != nil {
					return nil, e
				}
				plain, e := admission.OpenPayload(d.Manifest, admission.Target{Principal: alice.UniqueID, Device: alice.deviceID, AccountEpoch: 1}, received.Plaintext)
				if e != nil {
					return nil, e
				}
				sn, e := read(durable.Ref{Kind: durable.Trust, ID: "browser"}, durable.Ref{Kind: durable.Session, ID: "browser"})
				if e != nil {
					return nil, e
				}
				if _, e = local.CommitReceive(sn, d.ID, durable.ReceiveCandidate{SessionID: "browser", TrustID: "browser", Session: []byte(received.Session), Archive: plain, Envelope: d.Copy.Envelope, Context: sessionContext()}); e != nil {
					return nil, e
				}
				if _, e = call(alice, netproto.OlmAdmissionRequest{Action: "receive_ack", DeliveryID: d.ID, Digest: d.Digest}); e != nil {
					return nil, e
				}
				return json.RawMessage(plain), nil
			}
			return nil, fmt.Errorf("unknown harness action")
		}
		data, e := run()
		w.Header().Set("Content-Type", "application/json")
		if e != nil {
			json.NewEncoder(w).Encode(map[string]string{"error": e.Error()})
			return
		}
		json.NewEncoder(w).Encode(data)
	}))
	defer bridge.Close()
	root, e := filepath.Abs("../..")
	if e != nil {
		t.Fatal(e)
	}
	cmd := exec.CommandContext(ctx, "node", filepath.Join(root, "mobile/tests/olm-pg-browser.mjs"))
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "NOXA_ADMISSION_BRIDGE="+bridge.URL)
	out, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("Chromium/PG/native bridge: %v\n%s", e, out)
	}
	t.Log(string(out))
}
