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
	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/config"
	"noxa/internal/e2ee"
	"noxa/internal/e2ee/olmdir"
	"noxa/internal/netproto"
	"noxa/internal/store"
	"noxa/internal/web"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"golang.org/x/crypto/curve25519"
)

// nativeProductDevice is one fixture device. Native desktops carry their
// identity key pair (the desktop's account root key, as in the product);
// PWA devices carry their noXa device secret.
type nativeProductDevice struct {
	Name          string `json:"name"`
	Principal     string `json:"principal"`
	Device        string `json:"device"`
	Kind          string `json:"kind"`
	EdSecret      string `json:"edSecret,omitempty"`
	PublicKey     string `json:"public_key,omitempty"`
	PrivateKey    string `json:"private_key,omitempty"`
	X25519Public  string `json:"x25519_public,omitempty"`
	X25519Private string `json:"x25519_private,omitempty"`
	client        *Client
}

// TestIntegrationRatchetProductNative drives the actual native desktop product
// entries (client package: SendChatForTab, the outbox, SendConversationForTab,
// Edit/DeleteDirectMessageForTab, background drain) with the real Go/WASM core
// and bbolt, and the actual PWA product classes in packaged Chromium, against
// the real framed key/directory/admission handlers on a fresh migrated
// PostgreSQL database. The native client test process is the orchestrator; it
// evaluates PWA steps through the bridge. Production rate limits stay ON.
func TestIntegrationRatchetProductNative(t *testing.T) {
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
	root, e := filepath.Abs("../..")
	if e != nil {
		t.Fatal(e)
	}
	// A REAL control server (TLS with its own certificate, full framed auth
	// and dispatch) plus the real web listener serving the built PWA, over the
	// same database: production-path clients (step 16e) connect to it
	// directly, with no bridge and no test override. The bridge server below
	// shares its certificate binding, so every party uses one namespace.
	var fake *fakeAuth
	live := startTestEnvDeps(t, nil, func(cfg *config.Config) { cfg.AllowAnonymous = true }, func(deps *Deps) {
		fake = deps.Auth.(*fakeAuth)
		deps.Devices, deps.Authority = db, authority
		deps.Auth = productKeyAuth{AuthBackend: fake, keys: keys.keys}
	})
	defer live.stop()
	webSrv, e := web.New(config.WebConfig{Enabled: true, AppDir: filepath.Join(root, "web", "app"), WSConnectionsPerIP: 16, WSAttemptsPerMinute: 120}, "noxa", live.srv, zap.NewNop())
	if e != nil {
		t.Fatal(e)
	}
	webSrv.AllowLoopbackOrigins()
	webHTTP := httptest.NewServer(webSrv.Handler())
	defer webHTTP.Close()
	srv := New(&config.Config{}, zap.NewNop(), &Deps{Authority: authority, Devices: db, Auth: keys})
	srv.tlsFingerprint = live.srv.TLSFingerprint()
	srv.olmAdmissionEnabled = true
	binding, e := olmdir.ServerBinding(srv.tlsFingerprint)
	if e != nil {
		t.Fatalf("real server fingerprint %q: %v", srv.tlsFingerprint, e)
	}

	pemOf := func(pub ed25519.PublicKey) string {
		return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub}))
	}
	x25519Pair := func() (pub, priv [32]byte) {
		rand.Read(priv[:])
		p, _ := curve25519.X25519(priv[:], curve25519.Basepoint)
		copy(pub[:], p)
		return
	}
	devices := map[string]*nativeProductDevice{}
	roots := map[string]struct {
		key ed25519.PrivateKey
		id  string
	}{}
	var mu sync.Mutex
	register := func(name, principal, kind string, pub ed25519.PublicKey, priv ed25519.PrivateKey, x [32]byte) *nativeProductDevice {
		xs := base64.StdEncoding.EncodeToString(x[:])
		nd := store.NewDevice{Principal: principal, Kind: kind, Name: name, Ed25519Public: pemOf(pub), X25519Public: xs}
		root, linked := roots[principal]
		if linked {
			nd.ApprovedBy = root.id
			nd.ApprovalSignature = ed25519.Sign(root.key, netproto.DeviceApprovalTranscript(principal, root.id, nd.Ed25519Public, xs))
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
			record, e := e2ee.SignKeyRecord(principal, x, priv)
			if e != nil {
				t.Fatal(e)
			}
			keys.keys[principal] = record
		}
		pd := &nativeProductDevice{Name: name, Principal: principal, Device: d.DeviceID, Kind: kind,
			client: &Client{UniqueID: principal, UserID: 1, authed: true, deviceID: d.DeviceID, authSigningKey: nd.Ed25519Public}}
		devices[name] = pd
		return pd
	}
	// A native desktop: its identity key is the account root, its X25519 key
	// the account encryption key (exactly the desktop's own key material).
	native := func(name, principal string) {
		pubPEM, privPEM, e := auth.GenerateIdentityKeyPair()
		if e != nil {
			t.Fatal(e)
		}
		priv, _ := auth.LoadPrivateKey(privPEM)
		xpub, xpriv := x25519Pair()
		d := register(name, principal, store.DeviceKindDesktop, priv.Public().(ed25519.PublicKey), priv, xpub)
		d.PublicKey, d.PrivateKey = pubPEM, privPEM
		d.X25519Public, d.X25519Private = base64.StdEncoding.EncodeToString(xpub[:]), base64.StdEncoding.EncodeToString(xpriv[:])
	}
	pwa := func(name, principal string) *nativeProductDevice {
		pub, priv, _ := ed25519.GenerateKey(rand.Reader)
		xpub, xpriv := x25519Pair()
		d := register(name, principal, store.DeviceKindWeb, pub, priv, xpub)
		d.EdSecret = base64.StdEncoding.EncodeToString(priv)
		// The PWA's device X25519 secret opens history copies sealed to it.
		d.X25519Private = base64.StdEncoding.EncodeToString(xpriv[:])
		return d
	}
	// Key identities that sign in WITHOUT any pre-registered device, exactly
	// like the production desktop: a key-derived guest (no users row) or a
	// member, both registering their key through the real device handler on
	// first use (ruling 2: guests use secure messaging too).
	keyIdentity := func(name string, member bool) {
		pubPEM, privPEM, e := auth.GenerateIdentityKeyPair()
		if e != nil {
			t.Fatal(e)
		}
		priv, _ := auth.LoadPrivateKey(privPEM)
		principal, e := auth.UniqueIDFromPublicKey(pubPEM)
		if e != nil {
			t.Fatal(e)
		}
		xpub, xpriv := x25519Pair()
		record, e := e2ee.SignKeyRecord(principal, xpub, priv)
		if e != nil {
			t.Fatal(e)
		}
		keys.keys[principal] = record
		var userID int64
		if member {
			if e = db.DB().QueryRowContext(ctx, `INSERT INTO users(unique_id,nickname) VALUES($1,$2) RETURNING id`, principal, name).Scan(&userID); e != nil {
				t.Fatal(e)
			}
			// The real server's account lookup knows the member too.
			fake.mu.Lock()
			fake.users[principal] = &auth.User{ID: userID, UniqueID: principal, Nickname: name}
			fake.mu.Unlock()
		}
		xs := base64.StdEncoding.EncodeToString(xpub[:])
		devices[name] = &nativeProductDevice{Name: name, Principal: principal, Kind: store.DeviceKindDesktop, PublicKey: pubPEM, PrivateKey: privPEM,
			X25519Public: xs, X25519Private: base64.StdEncoding.EncodeToString(xpriv[:]),
			client: &Client{UniqueID: principal, UserID: userID, authed: true, authSigningKey: pubPEM, x25519Key: xs}}
	}
	keyIdentity("gguest1-desk", false)
	keyIdentity("gguest2-desk", false)
	keyIdentity("nmia-desk", true)
	native("nalice-desk", "nalice")
	native("nbob-desk", "nbob")
	native("ncarol-desk", "ncarol")
	native("ndave-desk", "ndave")
	pwa("nalice-phone", "nalice") // PWA device linked under the native root
	pwa("pbob-phone", "pbob")     // PWA-only account
	groups := map[string][]string{
		"g1": {"nalice", "nbob", "ncarol"}, "g2": {"nalice", "nbob"}, "g3": {"nalice", "nbob"}, "gx": {"nalice", "nbob", "pbob"},
	}
	groupIDs := map[string]string{}
	for _, p := range []string{"nalice", "nbob", "ncarol", "ndave", "pbob"} {
		var id int64
		if e = db.DB().QueryRowContext(ctx, `INSERT INTO users(unique_id,nickname) VALUES($1,$1) RETURNING id`, p).Scan(&id); e != nil {
			t.Fatal(e)
		}
		// The live server's account lookup knows every member.
		fake.mu.Lock()
		fake.users[p] = &auth.User{ID: id, UniqueID: p, Nickname: p}
		fake.mu.Unlock()
	}
	n := 0
	for name, members := range groups {
		n++
		id := fmt.Sprintf("00000000-0000-4000-8000-0000000000c%d", n)
		groupIDs[name] = id
		if _, e = db.DB().ExecContext(ctx, `INSERT INTO private_conversations(id,name,owner_uid) VALUES($1,$2,'nalice')`, id, name); e != nil {
			t.Fatal(e)
		}
		for _, p := range members {
			if _, e = db.DB().ExecContext(ctx, `INSERT INTO private_conversation_members(conversation_id,unique_id,pending,joined_epoch) VALUES($1,$2,FALSE,1)`, id, p); e != nil {
				t.Fatal(e)
			}
		}
	}

	// history copy rows the (hostile) server captured, per device name
	historySnap := map[string][]store.HistoryChunk{}
	drop := map[string]int{} // device/action -> handled-but-lost responses
	fail := map[string]int{} // device/action -> refused before the handler
	counts := map[string]int{}
	dispatch := func(c *Client, typ uint16, payload json.RawMessage) (map[string]any, error) {
		conn := &sessionResponseConn{blockingTCPConn: newBlockingTCPConn()}
		c.Conn = conn
		f := &netproto.Frame{Type: typ, Payload: payload}
		// As central dispatch: errors carry their request origin.
		rctx := context.WithValue(ctx, requestOriginContextKey{}, netproto.MessageType(typ))
		var e error
		switch netproto.MessageType(typ) {
		case netproto.MsgKeyRequest:
			e = srv.handleKeyRequest(rctx, c, f)
		case netproto.MsgOlmDirectoryRequest:
			e = srv.handleOlmDirectory(rctx, c, f)
		case netproto.MsgOlmAdmissionRequest:
			e = srv.handleOlmAdmission(rctx, c, f)
		case netproto.MsgDeviceRequest:
			// History copy (history_put/get/ack) through the real handler.
			e = srv.handleDeviceRequest(rctx, c, f)
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

	// PWA command channel: the native orchestrator posts "pwa_eval"; the
	// Chromium script long-polls "pwa_next" and answers "pwa_result".
	type pwaCommand struct {
		ID     int    `json:"id"`
		Device string `json:"device"`
		JS     string `json:"js"`
		Done   bool   `json:"done,omitempty"`
		result chan json.RawMessage
	}
	commands := make(chan *pwaCommand)
	var pending sync.Map
	nextID := 0
	var idMu sync.Mutex

	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Op      string          `json:"op"`
			Device  string          `json:"device"`
			Action  string          `json:"action"`
			Type    uint16          `json:"type"`
			JS      string          `json:"js"`
			ID      int             `json:"id"`
			Payload json.RawMessage `json:"payload"`
			Result  json.RawMessage `json:"result"`
		}
		if e := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&req); e != nil {
			http.Error(w, e.Error(), 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// Command relay ops never hold the frame mutex.
		switch req.Op {
		case "pwa_eval":
			idMu.Lock()
			nextID++
			cmd := &pwaCommand{ID: nextID, Device: req.Device, JS: req.JS, result: make(chan json.RawMessage, 1)}
			idMu.Unlock()
			pending.Store(cmd.ID, cmd)
			select {
			case commands <- cmd:
			case <-time.After(60 * time.Second):
				json.NewEncoder(w).Encode(map[string]string{"error": "PWA side not polling"})
				return
			}
			select {
			case res := <-cmd.result:
				w.Write(res)
			case <-time.After(10 * time.Minute):
				json.NewEncoder(w).Encode(map[string]string{"error": "PWA step timed out"})
			}
			return
		case "pwa_next":
			select {
			case cmd := <-commands:
				json.NewEncoder(w).Encode(cmd)
			case <-time.After(20 * time.Second):
				json.NewEncoder(w).Encode(map[string]bool{"idle": true})
			}
			return
		case "pwa_result":
			if v, ok := pending.LoadAndDelete(req.ID); ok {
				v.(*pwaCommand).result <- req.Result
			}
			json.NewEncoder(w).Encode(true)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		run := func() (any, error) {
			switch req.Op {
			case "fixture":
				return map[string]any{"binding": binding, "groups": groupIDs, "devices": devices, "real_addr": live.addr, "web_url": webHTTP.URL}, nil
			case "app_guest_principal":
				// The PWA guest's key-derived principal: its self-registered
				// first web device (no other web device is first).
				var p string
				e := db.DB().QueryRowContext(ctx, `SELECT principal FROM user_devices WHERE kind='web' AND first_device AND revoked_at IS NULL ORDER BY created_at DESC LIMIT 1`).Scan(&p)
				return p, e
			case "drop":
				drop[req.Device+"/"+req.Action]++
				return true, nil
			case "fail":
				fail[req.Device+"/"+req.Action]++
				return true, nil
			case "counts":
				return counts, nil
			case "link_unpublished":
				return pwa(req.Device, req.Action), nil
			case "pending_deliveries":
				var n int
				e := db.DB().QueryRowContext(ctx, `SELECT count(*) FROM olm_deliveries WHERE device_id=$1 AND state='pending'`, devices[req.Device].Device).Scan(&n)
				return n, e
			case "history_rows":
				d := devices[req.Device]
				rows, e := db.HistoryChunks(ctx, d.Principal, d.Device, 0, 1000)
				return len(rows), e
			case "history_snapshot":
				d := devices[req.Device]
				rows, e := db.HistoryChunks(ctx, d.Principal, d.Device, 0, 1000)
				historySnap[req.Device] = rows
				return len(rows), e
			case "history_replay":
				// A hostile server re-serves exactly the chunks it served before.
				d := devices[req.Device]
				for _, c := range historySnap[req.Device] {
					if e := db.PutHistoryChunk(ctx, d.Principal, c, netproto.MaxHistoryBytes); e != nil {
						return nil, e
					}
				}
				return len(historySnap[req.Device]), nil
			case "history_reposition":
				// A hostile server serves genuine chunks at a position other
				// than the one the source device signed.
				d := devices[req.Device]
				r, e := db.DB().ExecContext(ctx, `UPDATE device_history_chunks SET total=total+1 WHERE device_id=$1`, d.Device)
				if e != nil {
					return nil, e
				}
				n, _ := r.RowsAffected()
				return n, nil
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
				if d.Device == "" {
					// Registered on first use through the real handler.
					d.Device = d.client.currentDeviceID()
				}
				if e == nil && drop[k] > 0 {
					drop[k]--
					return map[string]any{"lost": true}, nil
				}
				return out, e
			}
			return nil, fmt.Errorf("unknown op")
		}
		data, e := run()
		if e != nil {
			json.NewEncoder(w).Encode(map[string]string{"error": e.Error()})
			return
		}
		json.NewEncoder(w).Encode(data)
	}))
	defer bridge.Close()
	env := append(os.Environ(), "NOXA_PRODUCT_BRIDGE="+bridge.URL)
	browser := exec.CommandContext(ctx, "node", filepath.Join(root, "mobile/tests/ratchet-interop-browser.mjs"))
	browser.Dir = root
	browser.Env = env
	var browserOut strings.Builder
	browser.Stdout, browser.Stderr = &browserOut, &browserOut
	if e = browser.Start(); e != nil {
		t.Fatal(e)
	}
	started := time.Now()
	nativeTest := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go"), "test", "-count=1", "-run", "^TestRatchetProductBridge$", "-v", "-timeout", "30m", ".")
	nativeTest.Dir = filepath.Join(root, "client")
	nativeTest.Env = env
	out, nativeErr := nativeTest.CombinedOutput()
	// Release the PWA side whatever the outcome.
	select {
	case commands <- &pwaCommand{Done: true}:
	case <-time.After(30 * time.Second):
	}
	browserErr := browser.Wait()
	t.Logf("native orchestrator (%s):\n%s", time.Since(started).Round(time.Millisecond), out)
	t.Logf("Chromium PWA side:\n%s", browserOut.String())
	if nativeErr != nil {
		t.Fatalf("native product/PG bridge: %v", nativeErr)
	}
	if browserErr != nil {
		t.Fatalf("Chromium PWA side: %v", browserErr)
	}
}
