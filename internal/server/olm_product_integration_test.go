//go:build integration

package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"noxa/internal/authorization"
	"noxa/internal/config"
	"noxa/internal/e2ee"
	"noxa/internal/netproto"
	"noxa/internal/store"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"go.uber.org/zap"
	"golang.org/x/crypto/curve25519"
)

// productKeyAuth serves registered noxa-dm-key-v1 records to the REAL
// handleKeyRequest; every other AuthBackend method is unused by these frames.
type productKeyAuth struct {
	AuthBackend
	keys map[string]string
}

func (a productKeyAuth) GetE2EPublicKey(_ context.Context, uid string) (string, error) {
	return a.keys[uid], nil
}

type productDevice struct {
	Name      string `json:"name"`
	Principal string `json:"principal"`
	Device    string `json:"device"`
	EdSecret  string `json:"edSecret"`
	client    *Client
}

// TestIntegrationRatchetProductPWA drives the actual PWA DirectMessages,
// Groups and RatchetConsumer product classes in packaged Chromium (WASM core +
// IndexedDB) through the real framed key/directory/admission handlers and a
// fresh migrated PostgreSQL database. Only frame transport is bridged.
func TestIntegrationRatchetProductPWA(t *testing.T) {
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
	keys := productKeyAuth{keys: map[string]string{}}
	srv := New(&config.Config{}, zap.NewNop(), &Deps{Authority: authority, Devices: db, Auth: keys})
	srv.tlsFingerprint = strings.TrimSuffix(strings.Repeat("ab:", 32), ":")
	srv.chatRate = nil
	srv.olmAdmissionEnabled = true
	binding := "tls-sha256:" + strings.Repeat("ab", 32)

	pemOf := func(pub ed25519.PublicKey) string {
		return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub}))
	}
	x25519Of := func() string {
		var secret [32]byte
		rand.Read(secret[:])
		pub, _ := curve25519.X25519(secret[:], curve25519.Basepoint)
		return base64.StdEncoding.EncodeToString(pub)
	}
	devices := map[string]*productDevice{}
	roots := map[string]struct {
		key ed25519.PrivateKey
		id  string
	}{}
	var mu sync.Mutex
	link := func(name, principal string) *productDevice {
		pub, priv, _ := ed25519.GenerateKey(rand.Reader)
		var encryption [32]byte
		x := x25519Of()
		raw, _ := base64.StdEncoding.DecodeString(x)
		copy(encryption[:], raw)
		nd := store.NewDevice{Principal: principal, Kind: store.DeviceKindWeb, Name: name, Ed25519Public: pemOf(pub), X25519Public: x}
		root, linked := roots[principal]
		if linked {
			nd.ApprovedBy = root.id
			nd.ApprovalSignature = ed25519.Sign(root.key, netproto.DeviceApprovalTranscript(principal, root.id, nd.Ed25519Public, x))
		}
		d, e := db.AddDevice(ctx, nd, 64)
		if e != nil {
			t.Fatal(e)
		}
		if !linked {
			roots[principal] = struct {
				key ed25519.PrivateKey
				id  string
			}{priv, d.DeviceID}
			record, e := e2ee.SignKeyRecord(principal, encryption, priv)
			if e != nil {
				t.Fatal(e)
			}
			keys.keys[principal] = record
		}
		pd := &productDevice{Name: name, Principal: principal, Device: d.DeviceID, EdSecret: base64.StdEncoding.EncodeToString(priv),
			client: &Client{UniqueID: principal, UserID: 1, authed: true, deviceID: d.DeviceID, authSigningKey: nd.Ed25519Public}}
		devices[name] = pd
		return pd
	}
	link("alice-phone", "alice")
	link("alice-tablet", "alice")
	link("bob-phone", "bob")
	const group = "00000000-0000-4000-8000-0000000000b1"
	if _, e = db.DB().ExecContext(ctx, `INSERT INTO private_conversations(id,name,owner_uid) VALUES($1,'ratchet product group','alice')`, group); e != nil {
		t.Fatal(e)
	}
	for _, p := range []string{"alice", "bob"} {
		if _, e = db.DB().ExecContext(ctx, `INSERT INTO users(unique_id,nickname) VALUES($1,$1)`, p); e != nil {
			t.Fatal(e)
		}
		if _, e = db.DB().ExecContext(ctx, `INSERT INTO private_conversation_members(conversation_id,unique_id,pending,joined_epoch) VALUES($1,$2,FALSE,1)`, group, p); e != nil {
			t.Fatal(e)
		}
	}

	drop := map[string]int{} // device/action -> handled-but-lost responses
	fail := map[string]int{} // device/action -> refused before the handler
	counts := map[string]int{}
	dispatch := func(c *Client, typ uint16, payload json.RawMessage) (map[string]any, error) {
		conn := &sessionResponseConn{blockingTCPConn: newBlockingTCPConn()}
		c.Conn = conn
		f := &netproto.Frame{Type: typ, Payload: payload}
		var e error
		switch netproto.MessageType(typ) {
		case netproto.MsgKeyRequest:
			e = srv.handleKeyRequest(ctx, c, f)
		case netproto.MsgOlmDirectoryRequest:
			e = srv.handleOlmDirectory(ctx, c, f)
		case netproto.MsgOlmAdmissionRequest:
			e = srv.handleOlmAdmission(ctx, c, f)
		default:
			return nil, fmt.Errorf("frame %d is outside this product slice", typ)
		}
		if e != nil {
			return nil, e
		}
		out, e := netproto.ReadFrame(&conn.output)
		if e != nil {
			return nil, e
		}
		return map[string]any{"type": out.Type, "payload": json.RawMessage(out.Payload)}, nil
	}
	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var req struct {
			Op      string          `json:"op"`
			Device  string          `json:"device"`
			Action  string          `json:"action"`
			Type    uint16          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if e := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&req); e != nil {
			http.Error(w, e.Error(), 400)
			return
		}
		run := func() (any, error) {
			switch req.Op {
			case "fixture":
				return map[string]any{"binding": binding, "group": group, "devices": devices}, nil
			case "drop":
				drop[req.Device+"/"+req.Action]++
				return true, nil
			case "fail":
				fail[req.Device+"/"+req.Action]++
				return true, nil
			case "counts":
				return counts, nil
			case "link_unpublished":
				return link(req.Device, req.Action), nil
			case "revoke":
				d := devices[req.Device]
				_, e := db.RevokeDevice(ctx, d.Principal, d.Device, "user")
				return true, e
			case "request":
				d := devices[req.Device]
				if d == nil {
					return nil, fmt.Errorf("unknown device")
				}
				var probe struct {
					Action string `json:"action"`
				}
				json.Unmarshal(req.Payload, &probe)
				k := req.Device + "/" + probe.Action
				counts[k]++
				if fail[k] > 0 {
					fail[k]--
					return map[string]any{"lost": true}, nil
				}
				out, e := dispatch(d.client, req.Type, req.Payload)
				if e == nil && drop[k] > 0 {
					drop[k]--
					return map[string]any{"lost": true}, nil
				}
				return out, e
			}
			return nil, fmt.Errorf("unknown op")
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
	cmd := exec.CommandContext(ctx, "node", filepath.Join(root, "mobile/tests/ratchet-product-browser.mjs"))
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "NOXA_PRODUCT_BRIDGE="+bridge.URL)
	out, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("Chromium product/PG bridge: %v\n%s", e, out)
	}
	t.Log(string(out))
}
