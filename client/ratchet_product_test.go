package main

// Native ratchet product vertical + native<->PWA interop, native side and
// orchestrator. Runs only under TestIntegrationRatchetProductNative
// (internal/server), which provides the bridge to the REAL framed
// key/directory/admission handlers on PostgreSQL and the Chromium PWA side.
// Each native device is an actual App + connManager whose control connection
// is an in-memory pipe: every frame goes through requestOn/readLoop/dispatch
// on the client and through the real server handlers on the other end.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"noxa/internal/e2ee/admission"
	"noxa/internal/e2ee/durable"
	"noxa/internal/e2ee/ratchet"
	"noxa/internal/netproto"
)

type productFixtureDevice struct {
	Name          string `json:"name"`
	Principal     string `json:"principal"`
	Device        string `json:"device"`
	Kind          string `json:"kind"`
	PublicKey     string `json:"public_key"`
	PrivateKey    string `json:"private_key"`
	X25519Public  string `json:"x25519_public"`
	X25519Private string `json:"x25519_private"`
}

type productBridge struct {
	t   *testing.T
	url string
}

func (b productBridge) post(body map[string]any, out any) {
	b.t.Helper()
	raw, _ := json.Marshal(body)
	resp, err := http.Post(b.url, "application/json", bytes.NewReader(raw))
	if err != nil {
		b.t.Fatalf("bridge %v: %v", body["op"], err)
	}
	defer resp.Body.Close()
	var data bytes.Buffer
	_, _ = data.ReadFrom(resp.Body)
	if out != nil {
		if err := json.Unmarshal(data.Bytes(), out); err != nil {
			b.t.Fatalf("bridge %v: %v: %s", body["op"], err, data.String())
		}
	}
}

func (b productBridge) counts() map[string]int {
	out := map[string]int{}
	b.post(map[string]any{"op": "counts"}, &out)
	return out
}

func productTransport(c map[string]int, device string) int {
	return c[device+"/begin"] + c[device+"/put_part"] + c[device+"/finalize"]
}

// pwa evaluates an async function body in a PWA device page (T is that
// device's product objects) and returns its JSON result.
func (b productBridge) pwa(device, js string) json.RawMessage {
	b.t.Helper()
	var out struct {
		OK        json.RawMessage `json:"ok"`
		Error     string          `json:"error"`
		Code      string          `json:"code"`
		Committed json.RawMessage `json:"committed"`
	}
	b.post(map[string]any{"op": "pwa_eval", "device": device, "js": js}, &out)
	if out.Error != "" {
		b.t.Fatalf("PWA %s: %s (%s)\n%s", device, out.Error, out.Code, js)
	}
	return out.OK
}

type productEvents struct {
	mu      sync.Mutex
	records []RatchetMessageEvent
	quar    []RatchetQuarantine
	trusts  [][2]string
	history []historyCopyProgress
}

func (r *productEvents) Emit(name string, payload any) {
	if p, ok := payload.(historyCopyProgress); ok && name == "device_history" {
		r.mu.Lock()
		r.history = append(r.history, p)
		r.mu.Unlock()
		return
	}
	s, ok := payload.(string)
	if name != "event" || !ok {
		return
	}
	var env struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal([]byte(s), &env) != nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	switch env.Type {
	case "ratchet_message":
		var e RatchetMessageEvent
		if json.Unmarshal(env.Data, &e) == nil {
			r.records = append(r.records, e)
		}
	case "ratchet_trust":
		var t struct {
			Contact string `json:"contact"`
			State   string `json:"state"`
		}
		if json.Unmarshal(env.Data, &t) == nil {
			r.trusts = append(r.trusts, [2]string{t.Contact, t.State})
		}
	case "ratchet_quarantine":
		var q struct {
			Placeholders []RatchetQuarantine `json:"placeholders"`
		}
		if json.Unmarshal(env.Data, &q) == nil {
			r.quar = q.Placeholders
		}
	}
}

// newCount counts new (unread-eligible) projections of key.
func (r *productEvents) newCount(key string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, e := range r.records {
		if e.Key == key && e.IsNew && !e.Self && !e.Copied {
			n++
		}
	}
	return n
}

type countingCore struct {
	inner ratchetCore
	mu    sync.Mutex
	ops   map[string]int
}

func (c *countingCore) Apply(ctx context.Context, r ratchet.Request) (ratchet.Response, error) {
	c.mu.Lock()
	c.ops[r.Op]++
	c.mu.Unlock()
	return c.inner.Apply(ctx, r)
}

func (c *countingCore) count(op string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ops[op]
}

var productCores sync.Map // device ID -> *countingCore (current process instance)

type nativeNode struct {
	t        *testing.T
	b        productBridge
	dev      productFixtureDevice
	binding  string
	base     string // persistent local storage (survives a simulated restart)
	app      *App
	cm       *connManager
	tab      string
	events   *productEvents
	ttl      int64
	pipeMu   sync.Mutex
	pipeDone chan struct{}
}

func newNativeNode(t *testing.T, b productBridge, dev productFixtureDevice, binding, base string) *nativeNode {
	n := &nativeNode{t: t, b: b, dev: dev, binding: binding, base: base, tab: "tab-" + dev.Name}
	n.boot()
	return n
}

// boot builds a fresh App + manager over the same local storage: a new
// process for every in-memory purpose.
func (n *nativeNode) boot() {
	id := &identity{PublicKey: n.dev.PublicKey, PrivateKey: n.dev.PrivateKey, X25519Public: n.dev.X25519Public, X25519Private: n.dev.X25519Private}
	n.app = &App{tabs: map[string]*tabState{}, hotkeys: map[string]*hotkeyReg{}, settingsPath: filepath.Join(n.base, "settings.json")}
	n.cm = n.app.newTabManager(n.tab, id)
	n.events = &productEvents{}
	n.cm.sink = n.events
	n.cm.confirmPeerChange = nil
	n.app.tabs[n.tab] = &tabState{cm: n.cm}
	n.app.activeID = n.tab
	n.app.cmStore(n.cm)
}

func (n *nativeNode) identity() *identity {
	id, _ := n.cm.identity()
	return id
}

func (n *nativeNode) protection() *durable.Protection {
	p := durable.IdentityProtection([]byte(strings.Repeat("k", 16) + n.dev.Name + strings.Repeat("p", 32)))
	return &p
}

// connect installs a fresh authenticated control connection (in-memory pipe
// pumped through the bridge to the real handlers) and starts the session.
func (n *nativeNode) connect() {
	n.t.Helper()
	client, server := net.Pipe()
	n.cm.mu.Lock()
	n.cm.ratchetTest = &ratchetTestConfig{Dir: filepath.Join(n.base, "ratchet"), Binding: n.binding, Poll: time.Hour, Protection: n.protection(), AttemptTTL: n.ttl}
	n.cm.conn = client
	n.cm.connEpoch++
	n.cm.closed = false
	n.cm.clientID = "native-" + n.dev.Name
	n.cm.uniqueID = n.dev.Principal
	n.cm.deviceID = n.dev.Device
	n.cm.addr = "ratchet-product.test:12333"
	n.cm.supportsDevices = true
	n.cm.supportsDMDevices = true
	n.cm.readTimeout = 0
	n.cm.mu.Unlock()
	done := make(chan struct{})
	n.pipeMu.Lock()
	n.pipeDone = done
	n.pipeMu.Unlock()
	go n.cm.readLoop(client)
	go n.pump(server, done)
	n.startSession()
}

// connectTransport installs the authenticated connection only: the ratchet
// session is still starting (device registration, store, core warm-up).
func (n *nativeNode) connectTransport() {
	n.t.Helper()
	client, server := net.Pipe()
	n.cm.mu.Lock()
	n.cm.ratchetTest = &ratchetTestConfig{Dir: filepath.Join(n.base, "ratchet"), Binding: n.binding, Poll: time.Hour, Protection: n.protection(), AttemptTTL: n.ttl}
	n.cm.conn = client
	n.cm.connEpoch++
	n.cm.closed = false
	n.cm.clientID = "native-" + n.dev.Name
	n.cm.uniqueID = n.dev.Principal
	n.cm.deviceID = n.dev.Device
	n.cm.addr = "ratchet-product.test:12333"
	n.cm.supportsDevices = true
	n.cm.supportsDMDevices = true
	n.cm.readTimeout = 0
	n.cm.mu.Unlock()
	done := make(chan struct{})
	n.pipeMu.Lock()
	n.pipeDone = done
	n.pipeMu.Unlock()
	go n.cm.readLoop(client)
	go n.pump(server, done)
}

func (n *nativeNode) startSession() {
	n.t.Helper()
	n.cm.mu.Lock()
	conn := n.cm.conn
	n.cm.mu.Unlock()
	if err := n.cm.startRatchet(conn); err != nil {
		n.t.Fatalf("%s: start ratchet session: %v", n.dev.Name, err)
	}
}

// pump is the server end of the pipe: each frame goes to the real handler;
// a "lost" response models a dropped connection after (or before) handling.
func (n *nativeNode) pump(server net.Conn, done chan struct{}) {
	defer close(done)
	defer server.Close()
	for {
		f, err := netproto.ReadFrame(server)
		if err != nil {
			return
		}
		raw, _ := json.Marshal(map[string]any{"op": "request", "device": n.dev.Name, "type": f.Type, "payload": json.RawMessage(f.Payload)})
		resp, err := http.Post(n.b.url, "application/json", bytes.NewReader(raw))
		if err != nil {
			return
		}
		var out struct {
			Lost    bool            `json:"lost"`
			Error   string          `json:"error"`
			Type    uint16          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		err = json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		if err != nil || out.Lost || out.Error != "" {
			return
		}
		if netproto.WriteFrame(server, &netproto.Frame{Type: out.Type, Payload: out.Payload}) != nil {
			return
		}
	}
}

func (n *nativeNode) disconnect() {
	n.cm.disconnect()
	n.pipeMu.Lock()
	done := n.pipeDone
	n.pipeMu.Unlock()
	if done != nil {
		<-done
	}
	// The detached session stops asynchronously; await its store release.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := n.cm.ratchetConsumerOn(nil); err != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// reconnect models a dropped connection that the client re-establishes.
func (n *nativeNode) reconnect() {
	n.disconnect()
	time.Sleep(50 * time.Millisecond)
	n.connect()
}

func (n *nativeNode) consumer() *ratchetConsumer {
	n.t.Helper()
	c, err := n.cm.ratchetConsumerOn(nil)
	if err != nil {
		n.t.Fatalf("%s: %v", n.dev.Name, err)
	}
	return c
}

func (n *nativeNode) drain() []RatchetQuarantine {
	n.t.Helper()
	q, err := n.consumer().drain()
	if err != nil {
		n.t.Fatalf("%s: drain: %v", n.dev.Name, err)
	}
	return q
}

func (n *nativeNode) log(peer string) []DMEntry {
	n.t.Helper()
	dir, err := n.app.dmHistoryDir()
	if err != nil {
		n.t.Fatal(err)
	}
	id, _ := n.cm.identity()
	store, err := dmHistoryStoreForIdentity(dir, id)
	if err != nil {
		n.t.Fatal(err)
	}
	entries, err := store.messages(peer)
	if err != nil {
		n.t.Fatal(err)
	}
	return entries
}

func (n *nativeNode) entry(peer, from, logical string) (DMEntry, bool) {
	for _, e := range n.log(peer) {
		if e.Protocol == ratchetProtocol && e.FromUniqueID == from && e.ClientMsgID == logical {
			return e, true
		}
	}
	return DMEntry{}, false
}

func (n *nativeNode) entryByBody(peer, body string) (DMEntry, int) {
	var found DMEntry
	count := 0
	for _, e := range n.log(peer) {
		if e.Body == body {
			found = e
			count++
		}
	}
	return found, count
}

func (n *nativeNode) core() *countingCore {
	v, _ := productCores.Load(n.dev.Device)
	c, _ := v.(*countingCore)
	return c
}

func (n *nativeNode) groupHistory(group string) []RatchetRecord {
	n.t.Helper()
	records, err := n.app.RatchetConversationHistoryForTab(n.tab, group)
	if err != nil {
		n.t.Fatalf("%s: group history: %v", n.dev.Name, err)
	}
	return records
}

func groupTexts(records []RatchetRecord) []string {
	var out []string
	for _, r := range records {
		if r.Purpose == "chat" {
			out = append(out, r.Sender+":"+ratchetBodyText(r.Body))
		}
	}
	return out
}

func (n *nativeNode) stageOutbox(peer, text string) string {
	n.t.Helper()
	res, err := n.app.ChatOutboxForTab(n.tab, ChatOutboxRequest{Action: "stage", Scope: "direct", Target: peer, Text: text})
	if err != nil {
		n.t.Fatalf("%s: stage: %v", n.dev.Name, err)
	}
	return res.Entries[len(res.Entries)-1].ID
}

func (n *nativeNode) outboxEntry(id string) (ChatOutboxEntry, bool) {
	res, err := n.app.ChatOutboxForTab(n.tab, ChatOutboxRequest{Action: "list"})
	if err != nil {
		n.t.Fatal(err)
	}
	for _, e := range res.Entries {
		if e.ID == id {
			return e, true
		}
	}
	return ChatOutboxEntry{}, false
}

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func TestRatchetProductBridge(t *testing.T) {
	url := os.Getenv("NOXA_PRODUCT_BRIDGE")
	if url == "" {
		t.Skip("run from TestIntegrationRatchetProductNative (internal/server, integration)")
	}
	started := time.Now()
	b := productBridge{t: t, url: url}
	var fixture struct {
		Binding string                          `json:"binding"`
		Groups  map[string]string               `json:"groups"`
		Devices map[string]productFixtureDevice `json:"devices"`
	}
	b.post(map[string]any{"op": "fixture"}, &fixture)
	root := t.TempDir()
	oldTrust, oldIdentity := dmTrustRootOverride, identityRootOverride
	dmTrustRootOverride, identityRootOverride = filepath.Join(root, "trust"), filepath.Join(root, "identities")
	ratchetCoreFor = func(ns durable.Namespace, inner ratchetCore) ratchetCore {
		c := &countingCore{inner: inner, ops: map[string]int{}}
		productCores.Store(ns.Device, c)
		return c
	}
	t.Cleanup(func() {
		dmTrustRootOverride, identityRootOverride, ratchetCoreFor, ratchetBeforeCommit = oldTrust, oldIdentity, nil, nil
	})
	node := func(name string) *nativeNode {
		n := newNativeNode(t, b, fixture.Devices[name], fixture.Binding, filepath.Join(root, name))
		n.connect()
		return n
	}
	A, B, C, D := node("nalice-desk"), node("nbob-desk"), node("ncarol-desk"), node("ndave-desk")
	defer func() {
		for _, n := range []*nativeNode{A, B, C, D} {
			n.disconnect()
		}
	}()
	g1, g2, g3, gx := fixture.Groups["g1"], fixture.Groups["g2"], fixture.Groups["g3"], fixture.Groups["gx"]
	step := func(name string) { t.Logf("[%6.1fs] %s", time.Since(started).Seconds(), name) }

	// 1. Direct send through SendChatForTab; typed archive; receive projection.
	step("1 direct send")
	if e := A.app.SendChatForTab(A.tab, "direct", "nbob", "hello nbob"); e != "" {
		t.Fatalf("send: %s", e)
	}
	own, n := A.entryByBody("nbob", "hello nbob")
	if n != 1 || own.Protocol != ratchetProtocol || !own.Self || own.Status != "accepted" || own.Key != ratchetSemanticKey("nalice", own.ClientMsgID) {
		t.Fatalf("sender projection: %+v (%d)", own, n)
	}
	m1 := own.ClientMsgID
	B.drain()
	got, ok := B.entry("nalice", "nalice", m1)
	if !ok || got.Body != "hello nbob" || got.Self || got.Status != "received" || !got.EncVerified {
		t.Fatalf("receiver projection: %+v", got)
	}
	if B.events.newCount(ratchetSemanticKey("nalice", m1)) != 1 {
		t.Fatal("exactly one new (unread-eligible) projection expected")
	}

	// 2. Reply.
	step("2 reply")
	if e := B.app.SendChatForTab(B.tab, "direct", "nalice", "hi nalice"); e != "" {
		t.Fatal(e)
	}
	A.drain()
	if _, n := A.entryByBody("nbob", "hi nalice"); n != 1 {
		t.Fatal("reply missing")
	}

	// 3. Uncertain accepted response via the outbox: the queued entry ID is the
	//    logical ID; after reconnect exact status resolution, zero transport.
	step("3 uncertain accepted + outbox reuse")
	b.post(map[string]any{"op": "drop", "device": "nalice-desk", "action": "finalize"}, nil)
	qid := A.stageOutbox("nbob", "queued original")
	A.app.pumpChatOutbox(A.cm)
	if e, ok := A.outboxEntry(qid); !ok || e.State != "uncertain" || !strings.Contains(e.Error, "not confirmed") {
		t.Fatalf("uncertain outbox entry: %+v %v", e, ok)
	}
	// P3-6: an uncertain committed entry cannot be cancelled (undo + resend
	// would duplicate it); it must be resolved first.
	if _, err := A.app.ChatOutboxForTab(A.tab, ChatOutboxRequest{Action: "cancel", ID: qid}); err == nil {
		t.Fatal("cancel of an uncertain committed entry must be refused")
	}
	// Fix2 P3-E: Retry puts it back to queued (the pump waits for the session);
	// cancel must still be refused.
	if _, err := A.app.ChatOutboxForTab(A.tab, ChatOutboxRequest{Action: "retry", ID: qid}); err != nil {
		t.Fatal(err)
	}
	if _, err := A.app.ChatOutboxForTab(A.tab, ChatOutboxRequest{Action: "cancel", ID: qid}); err == nil {
		t.Fatal("cancel bypass through retry -> queued")
	}
	A.reconnect()
	A.drain()
	if e, ok := A.entry("nbob", "nalice", qid); !ok || e.Status != "accepted" || e.Body != "queued original" {
		t.Fatalf("resolved projection: %+v", e)
	}
	before := b.counts()
	if _, err := A.app.ChatOutboxForTab(A.tab, ChatOutboxRequest{Action: "retry", ID: qid}); err != nil {
		t.Fatal(err)
	}
	A.app.pumpChatOutbox(A.cm)
	if e, ok := A.outboxEntry(qid); ok {
		t.Fatalf("resolved entry still queued: %+v", e)
	}
	if d := productTransport(b.counts(), "nalice-desk") - productTransport(before, "nalice-desk"); d != 0 {
		t.Fatalf("accepted status needs no exact retry, transport=%d", d)
	}
	B.drain()
	if _, n := B.entryByBody("nalice", "queued original"); n != 1 {
		t.Fatal("receiver must see the original exactly once")
	}

	// 3b. P2-1: an immediate attachment/voice send whose finalize is lost is
	//     retried with the composer's SAME operation ID: the original resolves,
	//     the recipient sees exactly one message.
	step("3b immediate send retry with a stable operation ID")
	voice := "[file:storage-v#" + strings.Repeat("B", 43) + "=#nx1;s=9#voice.webm]"
	op := newClientMsgID()
	b.post(map[string]any{"op": "drop", "device": "nalice-desk", "action": "finalize"}, nil)
	if e := A.app.SendChatOperationForTab(A.tab, "direct", "nbob", voice, 0, op); !strings.Contains(e, "not confirmed") {
		t.Fatalf("lost finalize must be uncertain: %q", e)
	}
	A.reconnect()
	if e := A.app.SendChatOperationForTab(A.tab, "direct", "nbob", voice, 0, op); e != "" {
		t.Fatalf("retry of the same operation: %q", e)
	}
	B.drain()
	if _, n := B.entryByBody("nalice", voice); n != 1 {
		t.Fatalf("recipient must see the retried operation exactly once, got %d", n)
	}
	// Fix2 P3-A: the same operation ID with different content never reports
	// the original as this message being sent.
	if e := A.app.SendChatOperationForTab(A.tab, "direct", "nbob", "different content", 0, op); !strings.Contains(e, "different message") {
		t.Fatalf("reused operation with different content: %q", e)
	}

	// 3c. P2-2: entries queued offline stay queued while the session starts
	//     and are sent automatically once it is ready.
	step("3c offline queue drains automatically on reconnect")
	A.disconnect()
	staged, err := A.app.ChatOutboxForTab(A.tab, ChatOutboxRequest{Action: "stage", Scope: "direct", Target: "nbob", Text: "queued offline", QueueOffline: true})
	if err != nil {
		t.Fatal(err)
	}
	oid := staged.Entries[len(staged.Entries)-1].ID
	A.connectTransport()
	A.app.pumpChatOutbox(A.cm)
	if e, ok := A.outboxEntry(oid); !ok || e.State != "queued" {
		t.Fatalf("entry must wait for the session, not fail: %+v", e)
	}
	// Fix2 P3-D: another process holds the store, so the session cannot
	// attach: the waiting entry says so truthfully instead of waiting silently.
	held, err := durable.Open(ratchetStoreDir(filepath.Join(A.base, "ratchet"), A.identity()), durable.Namespace{Server: fixture.Binding, Principal: "nalice", Device: A.dev.Device}, *A.protection(), 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	A.cm.mu.Lock()
	heldConn := A.cm.conn
	A.cm.mu.Unlock()
	A.cm.startRatchetRecorded(heldConn)
	A.app.pumpChatOutbox(A.cm)
	if e, ok := A.outboxEntry(oid); !ok || e.State != "queued" || !strings.Contains(e.Error, "secure messaging unavailable") {
		t.Fatalf("blocked session must be surfaced: %+v", e)
	}
	held.Close()
	A.startSession()
	A.app.pumpChatOutbox(A.cm)
	if e, ok := A.outboxEntry(oid); ok {
		t.Fatalf("queued entry not sent automatically: %+v", e)
	}
	B.drain()
	if _, n := B.entryByBody("nalice", "queued offline"); n != 1 {
		t.Fatal("offline-queued message delivered once")
	}

	// 4. Local commit failure: zero transport; the next send commits.
	step("4 local commit failure")
	before = b.counts()
	ratchetBeforeCommit = func(ns durable.Namespace) error {
		ratchetBeforeCommit = nil
		return durable.ErrExhausted
	}
	if e := A.app.SendChatForTab(A.tab, "direct", "nbob", "never sent"); !strings.Contains(e, "exhausted") {
		t.Fatalf("commit failure: %q", e)
	}
	if d := productTransport(b.counts(), "nalice-desk") - productTransport(before, "nalice-desk"); d != 0 {
		t.Fatalf("transport before durable commit: %d", d)
	}
	if _, n := A.entryByBody("nbob", "never sent"); n != 0 {
		t.Fatal("uncommitted draft must not be projected as sent")
	}
	if e := A.app.SendChatForTab(A.tab, "direct", "nbob", "now sent"); e != "" {
		t.Fatal(e)
	}

	// 5. Receive committed, ACK lost, process restart: stored outcome + exact
	//    ACK without re-decrypt or a duplicate new projection.
	step("5 crash after receive commit")
	B.drain()
	if e := A.app.SendChatForTab(A.tab, "direct", "nbob", "survives crash"); e != "" {
		t.Fatal(e)
	}
	b.post(map[string]any{"op": "fail", "device": "nbob-desk", "action": "receive_ack"}, nil)
	if _, err := B.consumer().drain(); err == nil {
		t.Fatal("lost ACK must stop the drain")
	}
	crash, n := B.entryByBody("nalice", "survives crash")
	if n != 1 || B.events.newCount(ratchetSemanticKey("nalice", crash.ClientMsgID)) != 1 {
		t.Fatal("committed receive projected once before the crash")
	}
	acks := b.counts()["nbob-desk/receive_ack"]
	B.disconnect()
	B.boot()
	B.connect()
	B.drain()
	if _, n := B.entryByBody("nalice", "survives crash"); n != 1 {
		t.Fatal("restored from the protected archive exactly once")
	}
	if B.events.newCount(ratchetSemanticKey("nalice", crash.ClientMsgID)) != 0 {
		t.Fatal("replay after restart must not be new (no duplicate unread/notification)")
	}
	if c := B.core(); c.count(ratchet.Decrypt)+c.count(ratchet.Inbound) != 0 {
		t.Fatalf("re-decrypted after commit: %v", c.ops)
	}
	if b.counts()["nbob-desk/receive_ack"] <= acks {
		t.Fatal("exact ACK resent")
	}

	// 6. Authorship: edits/deletes address (sender, logical ID).
	step("6 authorship, edit, delete")
	if err := A.app.EditDirectMessageForTab(A.tab, "nbob", m1, "hello nbob (edited)"); err != nil {
		t.Fatal(err)
	}
	B.drain()
	if e, _ := B.entry("nalice", "nalice", m1); !e.Edited || e.Body != "hello nbob (edited)" {
		t.Fatalf("edit not applied: %+v", e)
	}
	// B reuses alice's logical string for its own message and edits it.
	if _, err := B.cm.sendRatchetDirect(nil, m1, "nalice", "nbob reuses the id"); err != nil {
		t.Fatal(err)
	}
	if err := B.app.EditDirectMessageForTab(B.tab, "nalice", m1, "nbob edited his own"); err != nil {
		t.Fatal(err)
	}
	// Client refusal: B can never edit alice's record.
	if err := B.app.EditDirectMessageForTab(B.tab, "nalice", qid, "forged"); !errors.Is(err, errDMChangeNotOwn) {
		t.Fatalf("client refusal: %v", err)
	}
	A.drain()
	ea, _ := A.entry("nbob", "nalice", m1)
	eb, okb := A.entry("nbob", "nbob", m1)
	if ea.Body != "hello nbob (edited)" || !okb || eb.Body != "nbob edited his own" || !eb.Edited {
		t.Fatalf("collision across senders: %+v / %+v", ea, eb)
	}
	if err := A.app.DeleteDirectMessageForTab(A.tab, "nbob", m1); err != nil {
		t.Fatal(err)
	}
	B.drain()
	da, _ := B.entry("nalice", "nalice", m1)
	db, _ := B.entry("nalice", "nbob", m1)
	if !da.Deleted || da.Body != "" || db.Deleted || db.Body != "nbob edited his own" {
		t.Fatalf("delete: %+v / %+v", da, db)
	}

	// 7. Existing encrypted attachment token inside the authenticated body.
	step("7 attachment token")
	token := "[file:storage-1#" + strings.Repeat("A", 43) + "=#nx1;s=12#report.pdf]"
	if e := A.app.SendChatForTab(A.tab, "direct", "nbob", "see "+token); e != "" {
		t.Fatal(e)
	}
	B.drain()
	if _, n := B.entryByBody("nalice", "see "+token); n != 1 {
		t.Fatal("attachment token not byte-exact")
	}

	// 8. Group: typed opaque result; changed draft on the same reference
	//    resolves the ORIGINAL committed content.
	step("8 group typed result + draft reuse")
	res, err := A.app.SendConversationForTab(A.tab, g1, "group hello", "gref-1")
	if err != nil || res.Protocol != ratchetProtocol || res.MessageID != 0 || !uuidPattern.MatchString(res.Transport) || res.Key != ratchetSemanticKey("nalice", "gref-1") {
		t.Fatalf("typed group result: %+v %v", res, err)
	}
	B.drain()
	C.drain()
	for _, n := range []*nativeNode{B, C} {
		if got := groupTexts(n.groupHistory(g1)); len(got) != 1 || got[0] != "nalice:group hello" {
			t.Fatalf("%s group view: %v", n.dev.Name, got)
		}
	}
	b.post(map[string]any{"op": "drop", "device": "nalice-desk", "action": "finalize"}, nil)
	if _, err := A.app.SendConversationForTab(A.tab, g1, "group original", "gref-2"); err == nil {
		t.Fatal("lost finalize must be uncertain")
	}
	A.reconnect()
	// Fix3 P3-4: a changed draft on the committed reference is NOT reported
	// as sent; the original stays authoritative and resolves exactly.
	if _, err := A.app.SendConversationForTab(A.tab, g1, "GROUP CHANGED", "gref-2"); err == nil || !strings.Contains(err.Error(), "different message") {
		t.Fatalf("changed draft on a committed reference: %v", err)
	}
	reuse, err := A.app.SendConversationForTab(A.tab, g1, "group original", "gref-2")
	if err != nil || !reuse.Reused || ratchetBodyText(reuse.Record.Body) != "group original" || reuse.Status != "accepted" {
		t.Fatalf("draft reuse: %+v %v", reuse, err)
	}
	B.drain()
	if got := groupTexts(B.groupHistory(g1)); strings.Join(got, "|") != "nalice:group hello|nalice:group original" {
		t.Fatalf("group original only: %v", got)
	}

	// 9. Device-pair sessions: DM + three groups with the same devices need no
	//    further claims (budget stays 2/pair/day).
	step("9 device-pair reuse")
	claims := b.counts()["nalice-desk/claim"]
	for _, g := range []string{g2, g3} {
		if r, err := A.app.SendConversationForTab(A.tab, g, "more groups", "more-"+g); err != nil || r.Status != "accepted" {
			t.Fatal(r, err)
		}
	}
	if d := b.counts()["nalice-desk/claim"] - claims; d != 0 {
		t.Fatalf("claims for further conversations: %d", d)
	}
	B.drain()
	if got := groupTexts(B.groupHistory(g3)); len(got) != 1 {
		t.Fatalf("g3: %v", got)
	}

	// 10. First contact (fresh device pair) whose begin is lost and whose
	//     deadline passes: abort, then the resend reuses the original reserved
	//     claim and is accepted with the original text.
	step("10 first-contact abort + resend")
	A.ttl = 36
	A.reconnect()
	claims = b.counts()["nalice-desk/claim"]
	b.post(map[string]any{"op": "fail", "device": "nalice-desk", "action": "begin"}, nil)
	fid := A.stageOutbox("ndave", "first contact")
	A.app.pumpChatOutbox(A.cm)
	if e, _ := A.outboxEntry(fid); e.State != "uncertain" {
		t.Fatalf("first contact: %+v", e)
	}
	// The connection dropped with the lost begin; come back only after the
	// deadline margin passed (the session drain then fences the attempt).
	A.disconnect()
	time.Sleep(7 * time.Second)
	A.connect()
	A.drain()
	if e, ok := A.entry("ndave", "nalice", fid); !ok || e.Status != "aborted" {
		t.Fatalf("expired never-begun attempt aborted: %+v", e)
	}
	if _, err := A.app.ChatOutboxForTab(A.tab, ChatOutboxRequest{Action: "retry", ID: fid}); err != nil {
		t.Fatal(err)
	}
	A.app.pumpChatOutbox(A.cm)
	if e, ok := A.outboxEntry(fid); ok {
		t.Fatalf("resend: %+v", e)
	}
	if d := b.counts()["nalice-desk/claim"] - claims; d != 1 {
		t.Fatalf("the first contact's claim must be reused: %d claims", d)
	}
	D.drain()
	if e, ok := D.entry("nalice", "nalice", fid); !ok || e.Body != "first contact" {
		t.Fatalf("ndave: %+v", e)
	}
	A.ttl = 0
	A.reconnect()

	// 11. Second process: the namespace store is exclusively locked.
	step("11 second process")
	ns := durable.Namespace{Server: fixture.Binding, Principal: "nalice", Device: A.dev.Device}
	if s, err := durable.Open(ratchetStoreDir(filepath.Join(A.base, "ratchet"), A.identity()), ns, *A.protection(), 100*time.Millisecond); !errors.Is(err, durable.ErrInUse) {
		if s != nil {
			s.Close()
		}
		t.Fatalf("independent open must fail explicitly: %v", err)
	}

	// 12. Stale roster: an approved device without an Olm publication fails
	//     the whole send (no silent omission, zero transport); after its
	//     revocation the send is accepted.
	step("12 stale roster + revoke")
	b.post(map[string]any{"op": "link_unpublished", "device": "nbob-tablet", "action": "nbob"}, nil)
	before = b.counts()
	if e := A.app.SendChatForTab(A.tab, "direct", "nbob", "to a stale roster"); e == "" {
		t.Fatal("stale roster accepted")
	}
	if d := productTransport(b.counts(), "nalice-desk") - productTransport(before, "nalice-desk"); d != 0 {
		t.Fatalf("stale roster transport: %d", d)
	}
	b.post(map[string]any{"op": "revoke", "device": "nbob-tablet"}, nil)
	A.cm.forgetDMDevices("nbob")
	if e := A.app.SendChatForTab(A.tab, "direct", "nbob", "after revoke"); e != "" {
		t.Fatal(e)
	}

	// 13. Native <-> PWA interop through the product entries.
	step("13 interop: native -> PWA direct + own linked PWA copy")
	if e := A.app.SendChatForTab(A.tab, "direct", "pbob", "native to pwa"); e != "" {
		t.Fatal(e)
	}
	got13 := string(b.pwa("pbob-phone", `await T.consumer.drain(); return T.view("nalice").map(m => [m.from, m.body, m.protocol, m.self]);`))
	if got13 != `[["nalice","native to pwa","noxa-olm-v1",false]]` {
		t.Fatalf("PWA receive: %s", got13)
	}
	if got := string(b.pwa("nalice-phone", `await T.consumer.drain(); return T.view("pbob").map(m => [m.from, m.body, m.self, m.copied]);`)); got != `[["nalice","native to pwa",true,true]]` {
		t.Fatalf("own linked PWA copy: %s", got)
	}
	step("14 interop: PWA -> native direct + linked copies")
	if s := string(b.pwa("pbob-phone", `return (await T.dm.send("nalice", "pwa to native", "", { id: "q1" })).receipt.state;`)); s != `"accepted"` {
		t.Fatalf("PWA send: %s", s)
	}
	A.drain()
	if e, ok := A.entry("pbob", "pbob", "q1"); !ok || e.Body != "pwa to native" || e.Self {
		t.Fatalf("native receive from PWA: %+v", e)
	}
	if s := string(b.pwa("nalice-phone", `return (await T.dm.send("nbob", "from my phone", "", { id: "p1" })).receipt.state;`)); s != `"accepted"` {
		t.Fatalf("linked PWA send: %s", s)
	}
	B.drain()
	if e, ok := B.entry("nalice", "nalice", "p1"); !ok || e.Body != "from my phone" {
		t.Fatalf("native receive from linked PWA device: %+v", e)
	}
	A.drain()
	if e, ok := A.entry("nbob", "nalice", "p1"); !ok || !e.Self || e.Body != "from my phone" {
		t.Fatalf("native own linked copy of a PWA send: %+v", e)
	}
	if A.events.newCount(ratchetSemanticKey("nalice", "p1")) != 0 {
		t.Fatal("own linked copy is not unread")
	}

	step("15 interop: groups both directions")
	if r, err := A.app.SendConversationForTab(A.tab, gx, "native group", "gx-1"); err != nil || r.Status != "accepted" {
		t.Fatal(r, err)
	}
	js := fmt.Sprintf(`await T.consumer.drain(); return T.groupView(%q).map(m => [m.from, m.text, m.protocol]);`, gx)
	if got := string(b.pwa("pbob-phone", js)); got != `[["nalice","native group","noxa-olm-v1"]]` {
		t.Fatalf("PWA group receive: %s", got)
	}
	if got := string(b.pwa("nalice-phone", js)); !strings.Contains(got, `"native group"`) {
		t.Fatalf("linked PWA group copy: %s", got)
	}
	if s := string(b.pwa("pbob-phone", fmt.Sprintf(`return (await T.groups.send(%q, "pwa group", { reference: "gx-2" })).receipt.state;`, gx))); s != `"accepted"` {
		t.Fatalf("PWA group send: %s", s)
	}
	A.drain()
	B.drain()
	for _, n := range []*nativeNode{A, B} {
		if got := groupTexts(n.groupHistory(gx)); !strings.Contains(strings.Join(got, "|"), "pbob:pwa group") {
			t.Fatalf("%s native group receive from PWA: %v", n.dev.Name, got)
		}
	}

	step("16 interop: edit/delete across clients")
	i1, _ := A.entryByBody("pbob", "native to pwa")
	if err := A.app.EditDirectMessageForTab(A.tab, "pbob", i1.ClientMsgID, "native to pwa (edited)"); err != nil {
		t.Fatal(err)
	}
	if got := string(b.pwa("pbob-phone", `await T.consumer.drain(); return T.view("nalice").filter(m => m.from === "nalice").map(m => [m.body, m.edited]);`)); got != `[["native to pwa (edited)",true]]` {
		t.Fatalf("PWA applies native edit: %s", got)
	}
	b.pwa("pbob-phone", `await T.dm.deleteMessage("nalice", "q1"); return true;`)
	A.drain()
	if e, _ := A.entry("pbob", "pbob", "q1"); !e.Deleted {
		t.Fatalf("native applies PWA delete: %+v", e)
	}

	// 16c. History transfer end to end: the desktop exporter seals signed
	//      chunks to a newly linked PWA device; the real server stores them
	//      on PostgreSQL; the PWA in Chromium imports readable records only
	//      (transferred provenance, no ratchet state) and ACKs after its
	//      durable write. A hostile server replaying or repositioning chunks
	//      imports nothing.
	step("16c history transfer desktop -> server -> PWA + hostile replay")
	historyTransferProduct(t, b, A)

	// 16d. Media controls: one explicitly targeted device, both directions,
	//      never in chat views, no fanout to other devices, expired controls
	//      acknowledged without being handed out.
	step("16d media controls native <-> PWA")
	controlProduct(t, b, A, fixture.Devices)

	// 16b. P2-3: the new-protocol path reaches the identity confirmation, and an
	//      explicit confirmation is a DURABLE ratchet reset (applied by the next
	//      session even when none is live at confirmation time).
	step("16b identity confirmation reachability + durable reset")
	pinPath, _, err := A.cm.peerPinPath("nbob")
	if err != nil {
		t.Fatal(err)
	}
	pin, err := readDMPin(pinPath)
	if err != nil || pin.Version != 1 {
		t.Fatal(pin, err)
	}
	changed := pin
	changed.Fingerprint = strings.Repeat("e", 64) // the pinned identity no longer matches nbob's record
	if err = writeDMPin(pinPath, changed); err != nil {
		t.Fatal(err)
	}
	A.cm.forgetDMDevices("nbob")
	if e := A.app.SendChatForTab(A.tab, "direct", "nbob", "to a changed identity"); !strings.Contains(e, "identity_changed") {
		t.Fatalf("changed identity: %q", e)
	}
	if !A.events.trust("nbob", "confirmation_required") {
		t.Fatal("the ratchet path must surface the identity confirmation")
	}
	// The user confirms (the pin holds the confirmed fingerprint and the
	// reset intent) while the ratchet trust record still pins an older noXa
	// key, and no session is live.
	trustID := ratchetTrustID("nbob", B.dev.Device)
	c := A.consumer()
	ts, err := c.d.Store.Read(durable.Ref{Kind: durable.Trust, ID: trustID})
	if err != nil {
		t.Fatal(err)
	}
	stale := base64OfKey(bytes.Repeat([]byte{1}, 32))
	if err = c.d.Store.SetTrust(ts, trustID, ratchetJSON(ratchetTrustValue{Principal: "nbob", Device: B.dev.Device, Noxa: &stale}), false); err != nil {
		t.Fatal(err)
	}
	A.disconnect()
	confirmed := pin
	confirmed.RatchetReset = pin.Fingerprint
	if err = writeDMPin(pinPath, confirmed); err != nil {
		t.Fatal(err)
	}
	A.connect()
	// Fix2 P2-1: prime the 5-minute device-list cache with a STALE (pre-change)
	// key for nbob's device. Applying the reset must fetch fresh and pin the
	// newly confirmed key, never the cached old one.
	_, cacheEpoch, _ := A.cm.dmDevices.get("nbob", time.Now())
	staleKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{9}, 32)).Public().(ed25519.PublicKey)
	A.cm.dmDevices.put("nbob", []dmTrustedDevice{{DeviceID: B.dev.Device, Signing: staleKey, First: true}}, cacheEpoch, time.Now())
	if e := A.app.SendChatForTab(A.tab, "direct", "nbob", "after confirmed reset"); e != "" {
		t.Fatalf("durable reset not applied: %q", e)
	}
	if after, _ := readDMPin(pinPath); after.RatchetReset != "" {
		t.Fatal("applied reset intent must be cleared")
	}
	B.drain()
	if _, n := B.entryByBody("nalice", "after confirmed reset"); n != 1 {
		t.Fatal("message after reset")
	}
	// Without the explicit intent a changed noXa key still blocks.
	c = A.consumer()
	ts, _ = c.d.Store.Read(durable.Ref{Kind: durable.Trust, ID: trustID})
	if err = c.d.Store.SetTrust(ts, trustID, ratchetJSON(ratchetTrustValue{Principal: "nbob", Device: B.dev.Device, Noxa: &stale}), false); err != nil {
		t.Fatal(err)
	}
	if e := A.app.SendChatForTab(A.tab, "direct", "nbob", "never adopted silently"); !strings.Contains(e, "account_changed") {
		t.Fatalf("changed key without confirmation: %q", e)
	}

	// 17. Logout/session replacement fences the consumer before any transport.
	step("17 logout fencing")
	old := A.consumer()
	before = b.counts()
	A.disconnect()
	if _, err := old.send(ratchetIntent{LogicalID: "after-logout", Scope: "direct", Peer: "nbob", Purpose: "chat", Body: ratchetChatBody("x")}); ratchetCode(err) != "fenced" {
		t.Fatalf("old consumer: %v", err)
	}
	if e := A.app.SendChatForTab(A.tab, "direct", "nbob", "after logout"); e == "" {
		t.Fatal("send after logout")
	}
	if d := productTransport(b.counts(), "nalice-desk") - productTransport(before, "nalice-desk"); d != 0 {
		t.Fatalf("transport after logout: %d", d)
	}
	// A normal sign-out/disconnect keeps the namespace and readable archive.
	if _, err := os.Stat(ratchetStoreDir(filepath.Join(A.base, "ratchet"), A.identity())); err != nil {
		t.Fatal("sign-out must keep the ratchet namespace", err)
	}

	// 18. Identity deletion destroys the identity's namespaces, fenced against
	//     its live session.
	step("18 identity deletion")
	dc := D.consumer()
	if err := D.app.destroyRatchetNamespaces(D.identity()); err != nil {
		t.Fatal(err)
	}
	if _, err := dc.drain(); ratchetCode(err) != "fenced" {
		t.Fatalf("live session must be fenced: %v", err)
	}
	if _, err := os.Stat(ratchetStoreDir(filepath.Join(D.base, "ratchet"), D.identity())); !os.IsNotExist(err) {
		t.Fatal("namespaces of a deleted identity must be destroyed", err)
	}
	D.cm.mu.Lock()
	dconn := D.cm.conn
	D.cm.mu.Unlock()
	if err := D.cm.startRatchet(dconn); err == nil {
		t.Fatal("a deleted identity must not reopen a session")
	}
	t.Logf("PASS native product vertical + native<->PWA interop with production rate limits in %s", time.Since(started).Round(time.Millisecond))
}

func base64OfKey(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// trust reports whether a ratchet_trust event with state was emitted for uid.
func (r *productEvents) trust(uid, state string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.trusts {
		if e[0] == uid && e[1] == state {
			return true
		}
	}
	return false
}

// waitHistory waits for the copy to device to finish (the progress events of
// the run started after the seen earlier events).
func (r *productEvents) waitHistory(t *testing.T, device string, seen int) historyCopyProgress {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		events := append([]historyCopyProgress(nil), r.history...)
		r.mu.Unlock()
		for _, p := range events[min(seen, len(events)):] {
			if p.DeviceID != device {
				continue
			}
			switch p.State {
			case "done":
				return p
			case "failed":
				t.Fatalf("history copy failed: %s", p.Error)
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("history copy did not finish")
	return historyCopyProgress{}
}

func (r *productEvents) historyCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.history)
}

type importedHistory struct {
	N         int  `json:"n"`
	OpsSame   bool `json:"opsSame"`
	Persisted int  `json:"persisted"`
	Unread    int  `json:"unread"`
	Records   []struct {
		Peer       string  `json:"peer"`
		ID         string  `json:"id"`
		From       string  `json:"from"`
		Body       *string `json:"body"`
		Protocol   string  `json:"protocol"`
		Provenance string  `json:"provenance"`
		Origin     string  `json:"origin"`
		Source     string  `json:"source"`
		Copied     bool    `json:"copied"`
	} `json:"records"`
}

// importJS runs DirectMessages.importHistory in the PWA page and reports the
// view, whether the ratchet core ran at all, and what a fresh instance loads
// back from IndexedDB (the durable write the ACK waited for).
const importJS = `
const ops = JSON.stringify(T.stats.ops);
const n = await T.dm.importHistory();
const records = [...T.dm.conversations.values()].flatMap(c => c.messages.map(m => ({ peer: c.peer, id: m.id, from: m.from, body: m.body ?? null,
    protocol: m.protocol || "", provenance: m.provenance || "", origin: m.origin?.provenance || "", source: m.source?.device || "", copied: !!m.copied })));
const fresh = new T.dm.constructor(T.dm.session, {});
await fresh.ready;
const persisted = [...fresh.conversations.values()].reduce((a, c) => a + c.messages.length, 0);
fresh.close();
return { n, records, opsSame: ops === JSON.stringify(T.stats.ops), persisted, unread: T.stats.unread };`

func historyTransferProduct(t *testing.T, b productBridge, A *nativeNode) {
	t.Helper()
	// A newly linked PWA device of nalice: it never received the earlier
	// traffic, so everything it shows afterwards came from the transfer.
	var tab productFixtureDevice
	b.post(map[string]any{"op": "link_unpublished", "device": "nalice-tab", "action": "nalice"}, &tab)
	if got := string(b.pwa("nalice-tab", `return (await T.consumer.ready()).epoch;`)); got != "1" {
		t.Fatalf("new PWA device: %s", got)
	}
	rows := func() int {
		var n int
		b.post(map[string]any{"op": "history_rows", "device": "nalice-tab"}, &n)
		return n
	}
	ratchetCalls := func(c map[string]int) int {
		n := 0
		for k, v := range c {
			// Directory/admission actions; the key lookup (no action) that
			// authorizes the source device is not ratchet traffic.
			if action, ok := strings.CutPrefix(k, "nalice-tab/"); ok && action != "" && !strings.HasPrefix(action, "history_") {
				n += v
			}
		}
		return n
	}
	runImport := func() importedHistory {
		before := b.counts()
		var out importedHistory
		if err := json.Unmarshal(b.pwa("nalice-tab", importJS), &out); err != nil {
			t.Fatal(err)
		}
		after := b.counts()
		if d := ratchetCalls(after) - ratchetCalls(before); d != 0 || !out.OpsSame {
			t.Fatalf("history import touched the ratchet (requests %d, core unchanged %v)", d, out.OpsSame)
		}
		if after["nalice-tab/history_ack"] <= before["nalice-tab/history_ack"] {
			t.Fatal("import must ACK what it consumed")
		}
		if n := rows(); n != 0 {
			t.Fatalf("ACKed chunks still stored: %d", n)
		}
		if out.Persisted != len(out.Records) || out.Unread != 0 {
			t.Fatalf("persisted %d of %d records, unread %d", out.Persisted, len(out.Records), out.Unread)
		}
		return out
	}
	copyTo := func() historyCopyProgress {
		seen := A.events.historyCount()
		if err := A.app.CopyHistoryToDeviceForTab(A.tab, tab.Device, -1); err != nil {
			t.Fatal(err)
		}
		return A.events.waitHistory(t, tab.Device, seen)
	}

	// What the desktop exporter may transfer: its readable archive records.
	dir, err := A.app.dmHistoryDir()
	if err != nil {
		t.Fatal(err)
	}
	hs, err := dmHistoryStoreForIdentity(dir, A.identity())
	if err != nil {
		t.Fatal(err)
	}
	want := historyMessagesSince(hs, 0, A.dev.Principal)
	ratchetWant := 0
	for _, m := range want {
		if m.Protocol == ratchetProtocol {
			ratchetWant++
		}
		if m.ClientMsgID == "q1" {
			t.Fatal("a deleted record must never be exported")
		}
	}
	if ratchetWant < 5 {
		t.Fatalf("expected a populated authenticated archive, got %d records", ratchetWant)
	}

	progress := copyTo()
	if progress.Messages != len(want) || progress.Total < 1 || rows() != progress.Total {
		t.Fatalf("export: %+v, stored %d, want %d messages", progress, rows(), len(want))
	}
	b.post(map[string]any{"op": "history_snapshot", "device": "nalice-tab"}, nil)
	first := runImport()
	if first.N != len(want) || len(first.Records) != len(want) {
		t.Fatalf("imported %d (%d shown), want %d", first.N, len(first.Records), len(want))
	}
	exported := map[string]netproto.HistoryMessage{}
	for _, m := range want {
		exported[m.Peer+"|"+m.From+"|"+m.ClientMsgID] = m
	}
	for _, r := range first.Records {
		m, ok := exported[r.Peer+"|"+r.From+"|"+r.ID]
		if !ok || r.Body == nil || *r.Body != m.Body || r.Protocol != m.Protocol {
			t.Fatalf("imported record not exported as-is: %+v", r)
		}
		if r.Protocol == ratchetProtocol && (r.Provenance != "transferred" || r.Origin != "authenticated" || r.Source != A.dev.Device || !r.Copied) {
			t.Fatalf("transferred record provenance: %+v", r)
		}
	}

	// Hostile replay 1: the server re-serves exactly the chunks already
	// imported and ACKed. Nothing is imported twice; the replay is ACKed.
	var replayed int
	b.post(map[string]any{"op": "history_replay", "device": "nalice-tab"}, &replayed)
	if replayed != progress.Total || rows() != progress.Total {
		t.Fatalf("replay staged %d of %d chunks", replayed, progress.Total)
	}
	if again := runImport(); again.N != 0 || len(again.Records) != len(want) {
		t.Fatalf("replayed chunks imported %d (now %d records)", again.N, len(again.Records))
	}

	// Hostile replay 2: a genuine new copy (with one new record) served at a
	// position other than the signed one imports nothing.
	if e := A.app.SendChatForTab(A.tab, "direct", "nbob", "after the transfer"); e != "" {
		t.Fatal(e)
	}
	copyTo()
	var moved int
	b.post(map[string]any{"op": "history_reposition", "device": "nalice-tab"}, &moved)
	if moved < 1 {
		t.Fatal("nothing repositioned")
	}
	shown := func(h importedHistory, body string) int {
		n := 0
		for _, r := range h.Records {
			if r.Body != nil && *r.Body == body {
				n++
			}
		}
		return n
	}
	if got := runImport(); got.N != 0 || shown(got, "after the transfer") != 0 {
		t.Fatalf("repositioned chunks imported %d", got.N)
	}
	// The honest copy then imports exactly the one record not yet held.
	copyTo()
	if honest := runImport(); honest.N != 1 || shown(honest, "after the transfer") != 1 || len(honest.Records) != len(want)+1 {
		t.Fatalf("honest copy after hostile ones: imported %d, %d records", honest.N, len(honest.Records))
	}
}

func controlProduct(t *testing.T, b productBridge, A *nativeNode, devices map[string]productFixtureDevice) {
	t.Helper()
	pending := func(name string) int {
		var n int
		b.post(map[string]any{"op": "pending_deliveries", "device": name}, &n)
		return n
	}
	var mu sync.Mutex
	var got []RatchetControl
	A.cm.OnRatchetControl(func(c RatchetControl) { mu.Lock(); got = append(got, c); mu.Unlock() })
	b.pwa("pbob-phone", `T.controls = []; T.consumer.onControl(c => T.controls.push(c)); return true;`)
	b.pwa("nalice-phone", `T.controls = []; T.consumer.onControl(c => T.controls.push(c)); return true;`)
	pbob := devices["pbob-phone"].Device
	body := func(id string, ttl int64) admission.ControlBody {
		return admission.ControlBody{Kind: "media-key", Scope: admission.ControlScope{Type: "channel", ID: "chan-1"}, Epoch: 1,
			ExpiresAt: time.Now().Unix() + ttl, Data: json.RawMessage(`{"key":"` + id + `"}`)}
	}
	aliceLinked := []string{"nalice-phone", "nalice-tab"}
	before := map[string]int{"pbob-phone": pending("pbob-phone")}
	for _, n := range aliceLinked {
		before[n] = pending(n)
	}
	viewBefore := string(b.pwa("pbob-phone", `return T.view("nalice").length;`))
	logBefore := len(A.log("pbob"))

	// Native -> PWA.
	out, err := A.cm.SendRatchetControl(RatchetControlIntent{LogicalID: "ctl-n2p", Principal: "pbob", Device: pbob, Body: body("n2p", 120)})
	if err != nil || out.Receipt == nil || out.Receipt.State != "accepted" {
		t.Fatalf("native control: %+v %v", out, err)
	}
	if d := pending("pbob-phone") - before["pbob-phone"]; d != 1 {
		t.Fatalf("target copies: %d", d)
	}
	for _, n := range aliceLinked {
		if pending(n) != before[n] {
			t.Fatalf("control fanned out to own linked device %s", n)
		}
	}
	var r1 struct {
		Controls []struct {
			Sender, LogicalID, Kind string
			Scope                   struct{ Type, ID string }
			Epoch                   int64
			Data                    map[string]string
		} `json:"controls"`
		View int `json:"view"`
	}
	read := `await T.consumer.drain(); return { controls: T.controls, view: T.view("nalice").length };`
	if err := json.Unmarshal(b.pwa("pbob-phone", read), &r1); err != nil {
		t.Fatal(err)
	}
	if len(r1.Controls) != 1 || r1.Controls[0].Sender != "nalice" || r1.Controls[0].LogicalID != "ctl-n2p" || r1.Controls[0].Kind != "media-key" ||
		r1.Controls[0].Scope.ID != "chan-1" || r1.Controls[0].Data["key"] != "n2p" || fmt.Sprint(r1.View) != viewBefore {
		t.Fatalf("PWA control receive: %+v (view before %s)", r1, viewBefore)
	}
	if pending("pbob-phone") != before["pbob-phone"] {
		t.Fatal("delivered control must be acknowledged")
	}
	// A second drain hands nothing out again; history never lists controls.
	if s := string(b.pwa("pbob-phone", `await T.consumer.drain(); return [T.controls.length, (await T.consumer.history()).filter(r => r.purpose === "control").length];`)); s != "[1,0]" {
		t.Fatalf("PWA replay/history: %s", s)
	}

	// PWA -> native.
	js := fmt.Sprintf(`return (await T.consumer.sendControl({ logicalID: "ctl-p2n", principal: "nalice", device: %q, body: { kind: "media-key", scope: { type: "call", id: "call-9" }, epoch: 4, expires_at: Math.floor(Date.now() / 1000) + 120, data: { key: "p2n" } } })).receipt.state;`, A.dev.Device)
	if s := string(b.pwa("pbob-phone", js)); s != `"accepted"` {
		t.Fatalf("PWA control send: %s", s)
	}
	for _, n := range aliceLinked {
		if pending(n) != before[n] {
			t.Fatalf("PWA control fanned out to %s", n)
		}
	}
	A.drain()
	mu.Lock()
	n := len(got)
	var first RatchetControl
	if n > 0 {
		first = got[0]
	}
	mu.Unlock()
	if n != 1 || first.Sender != "pbob" || first.LogicalID != "ctl-p2n" || first.Scope.Type != "call" || first.Epoch != 4 || string(first.Data) != `{"key":"p2n"}` {
		t.Fatalf("native control receive: %d %+v", n, first)
	}
	if len(A.log("pbob")) != logBefore {
		t.Fatal("a control reached the native DM log")
	}
	hist, err := A.consumer().history()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range hist {
		if r.Purpose == "control" {
			t.Fatal("history lists a control")
		}
	}

	// Expired on arrival: acknowledged, never handed out.
	if out, err := A.cm.SendRatchetControl(RatchetControlIntent{LogicalID: "ctl-expired", Principal: "pbob", Device: pbob, Body: body("old", 40)}); err != nil || out.Receipt.State != "accepted" {
		t.Fatalf("short control: %+v %v", out, err)
	}
	late := `const real = Date.now; Date.now = () => real() + 90000;
try { await T.consumer.drain(); } finally { Date.now = real; }
return T.controls.length;`
	if s := string(b.pwa("pbob-phone", late)); s != "1" {
		t.Fatalf("expired control handed out: %s", s)
	}
	if pending("pbob-phone") != before["pbob-phone"] {
		t.Fatal("expired control must be acknowledged")
	}
	if s := string(b.pwa("nalice-phone", `await T.consumer.drain(); return T.controls.length;`)); s != "0" {
		t.Fatalf("own linked device received a control: %s", s)
	}
}
