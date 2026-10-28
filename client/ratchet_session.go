// ratchet_session.go binds one native noxa-olm-v1 consumer to one
// authenticated control connection and projects its authoritative archive into
// the desktop's direct-message log and live events.
//
// Production activation is OFF: a session exists only when a test explicitly
// sets connManager.ratchetTest (the native counterpart of the PWA's
// enableRatchetTestSession). Once a session is configured there is no legacy
// fallback for direct/group text, edits or deletes of new records: a missing
// or failed consumer fails the operation explicitly.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"noxa/internal/e2ee"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"noxa/internal/auth"
	"noxa/internal/devicetrust"
	"noxa/internal/e2ee/durable"
	"noxa/internal/e2ee/olmdir"
	"noxa/internal/e2ee/ratchet"
	"noxa/internal/netproto"
)

// ratchetTestConfig explicitly enables the consumer for one connManager.
type ratchetTestConfig struct {
	Dir string // protected store directory (one bbolt file per namespace)
	// Binding overrides the server binding when the transport has no pinned
	// TLS fingerprint (in-memory test transports). With TLS the binding is
	// always derived from the verified, pinned certificate fingerprint.
	Binding string
	Poll    time.Duration // background drain period; 0 = 5 s
	// Protection replaces DPAPI/identity-derived local key protection (tests).
	Protection *durable.Protection
	AttemptTTL int64
}

// ratchetSession is the consumer of one (conn, epoch). stop cancels and awaits
// every operation before the shared store handle is released.
type ratchetSession struct {
	conn     net.Conn
	epoch    uint64
	handle   *durable.Handle
	consumer *ratchetConsumer
	quit     chan struct{}
	done     chan struct{}
	once     sync.Once
}

func (s *ratchetSession) stop() {
	s.once.Do(func() {
		close(s.quit)
		s.consumer.close()
		<-s.done
		_ = s.handle.Close()
	})
}

var (
	ratchetEngineMu sync.Mutex
	ratchetEngine   *ratchet.Engine
)

// sharedRatchetEngine compiles the pinned WASM core once per process; the
// engine caches code only, never accounts or sessions.
func sharedRatchetEngine() (*ratchet.Engine, error) {
	ratchetEngineMu.Lock()
	defer ratchetEngineMu.Unlock()
	if ratchetEngine != nil {
		return ratchetEngine, nil
	}
	e, err := ratchet.New(context.Background())
	if err != nil {
		return nil, err
	}
	ratchetEngine = e
	return e, nil
}

var errRatchetUnavailable = errors.New("secure messaging (noxa-olm-v1) is not ready for this session; nothing was sent")

var errRatchetIdentityDeleted = errors.New("secure messaging is unavailable: this identity's secure message store was deleted")

var ratchetIdentityLocks sync.Map // identity public key -> *sync.Mutex

// lockRatchetIdentity serialises opening and destroying one identity's stores,
// so a deletion cannot race a store recreation.
func lockRatchetIdentity(publicKey string) func() {
	v, _ := ratchetIdentityLocks.LoadOrStore(publicKey, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// ratchetConfigured reports whether this manager uses the new protocol.
func (m *connManager) ratchetConfigured() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ratchetTest != nil
}

// ratchetConsumerOn returns the consumer of the CURRENT session bound to conn
// (nil conn = whatever is current), or errRatchetUnavailable.
func (m *connManager) ratchetConsumerOn(conn net.Conn) (*ratchetConsumer, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.ratchetSess
	if s == nil || m.conn == nil || s.conn != m.conn || s.epoch != m.connEpoch || (conn != nil && conn != s.conn) {
		return nil, errRatchetUnavailable
	}
	return s.consumer, nil
}

// startRatchet opens the shared protected store for the authenticated
// namespace, attaches projections, replays the authoritative archive and
// starts the session-owned drain loop. Every await is fenced on (conn, epoch,
// identity); a session started for a replaced connection stops itself.
func (m *connManager) startRatchet(conn net.Conn) error {
	m.mu.Lock()
	cfg, epoch, identity := m.ratchetTest, m.connEpoch, m.id
	uid, device, fingerprint := m.uniqueID, m.deviceID, m.fingerprint
	current := conn != nil && m.conn == conn && !m.closed
	m.mu.Unlock()
	if cfg == nil {
		return nil
	}
	if !current || identity == nil || uid == "" || device == "" {
		return errRatchetUnavailable
	}
	unlock := lockRatchetIdentity(identity.PublicKey)
	defer unlock()
	if _, deleted := ratchetDeleted.Load(identity.PublicKey); deleted {
		return errRatchetIdentityDeleted
	}
	base := cfg.Dir
	if base == "" {
		var err error
		if base, err = defaultRatchetBase(); err != nil {
			return err
		}
	}
	storeDir := ratchetStoreDir(base, identity)
	binding := cfg.Binding
	if fingerprint != "" {
		fp := strings.ToLower(strings.ReplaceAll(fingerprint, ":", ""))
		b, err := olmdir.ServerBinding(fp)
		if err != nil {
			return err
		}
		binding = b
	}
	if binding == "" {
		return errRatchetUnavailable
	}
	ns := durable.Namespace{Server: binding, Principal: uid, Device: device}
	var handle *durable.Handle
	var err error
	if cfg.Protection != nil {
		handle, err = durable.OpenShared(storeDir, ns, *cfg.Protection)
	} else {
		_, secret, e := identity.x25519()
		if e != nil {
			return e
		}
		handle, err = openDurableRatchets(storeDir, ns, secret[:])
	}
	if err != nil {
		// A second process holding the namespace fails explicitly (ErrInUse).
		return fmt.Errorf("secure message store: %w", err)
	}
	engine, err := sharedRatchetEngine()
	if err != nil {
		_ = handle.Close()
		return err
	}
	signing, err := auth.LoadPrivateKey(identity.PrivateKey)
	if err != nil {
		_ = handle.Close()
		return err
	}
	assertCurrent := func() error {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.conn != conn || m.connEpoch != epoch || m.closed || m.id != identity {
			return errors.New("session replaced")
		}
		return nil
	}
	admissionReq, directoryReq := m.ratchetTransport(conn)
	var core ratchetCore = engine
	if wrap := ratchetCoreFor; wrap != nil {
		core = wrap(ns, core)
	}
	consumer, err := newRatchetConsumer(ratchetDeps{
		Store: handle.Store, Core: core, Namespace: ns, Admission: admissionReq, Directory: directoryReq,
		DeviceKey: m.ratchetDeviceKey,
		SignBundle: func(b olmdir.Bundle) (olmdir.Bundle, error) {
			if err := assertCurrent(); err != nil {
				return olmdir.Bundle{}, err
			}
			return b.Sign(signing)
		},
		AssertCurrent: assertCurrent, AttemptTTL: cfg.AttemptTTL,
		TrustReset: m.ratchetResetPending, TrustResetDone: m.clearRatchetReset,
	})
	if err != nil {
		_ = handle.Close()
		return err
	}
	s := &ratchetSession{conn: conn, epoch: epoch, handle: handle, consumer: consumer, quit: make(chan struct{}), done: make(chan struct{})}
	consumer.project(func(r RatchetRecord, isNew bool) { m.projectRatchet(r, isNew, assertCurrent) })
	m.mu.Lock()
	_, deleted := ratchetDeleted.Load(identity.PublicKey)
	if m.conn != conn || m.connEpoch != epoch || m.closed || deleted {
		m.mu.Unlock()
		close(s.done)
		s.stop()
		return errRatchetUnavailable
	}
	old := m.ratchetSess
	m.ratchetSess = s
	m.ratchetStartErr = nil
	m.mu.Unlock()
	if old != nil {
		go old.stop()
	}
	poll := cfg.Poll
	if poll <= 0 {
		poll = 5 * time.Second
	}
	go guardCrash("ratchet drain", func() {
		defer close(s.done)
		// The protected archive is authoritative: restore the persisted views
		// it holds (idempotent). Live views load the DM log/group history
		// themselves, so the replay emits no per-record frontend events.
		if records, err := consumer.history(); err == nil {
			for _, r := range records {
				if consumer.check() != nil {
					return
				}
				if hook := m.ratchetProject; hook != nil && r.Protocol == ratchetProtocol {
					hook(m, r, false)
				}
			}
		} else {
			log.Printf("ratchet: history replay: %v", err)
		}
		last := ""
		for {
			list, err := consumer.drain()
			if err == nil {
				// Placeholders are announced only when the list changes.
				if key := string(ratchetJSON(list)); key != last && consumer.check() == nil {
					last = key
					m.showRatchetQuarantine(list)
				}
			} else if ratchetCode(err) == "fenced" {
				return
			}
			select {
			case <-s.quit:
				return
			case <-time.After(poll):
			}
		}
	})
	return nil
}

// stopRatchetLocked detaches the session while m.mu is held; the caller stops
// it after unlocking (stop awaits in-flight operations that take m.mu).
func (m *connManager) takeRatchetLocked() *ratchetSession {
	s := m.ratchetSess
	m.ratchetSess = nil
	return s
}

// ratchetTransport correlates frame 200/202 requests on exactly conn.
func (m *connManager) ratchetTransport(conn net.Conn) (func(context.Context, netproto.OlmAdmissionRequest) (json.RawMessage, error), func(context.Context, netproto.OlmDirectoryRequest) (netproto.OlmDirectoryResult, error)) {
	admissionReq := func(_ context.Context, r netproto.OlmAdmissionRequest) (json.RawMessage, error) {
		if r.RequestID == "" {
			r.RequestID = newClientMsgID()
		}
		f, err := m.requestOn(conn, netproto.MsgOlmAdmissionRequest, netproto.MsgOlmAdmissionResult, r, 15*time.Second)
		if err != nil {
			var failure *requestFailure
			if errors.As(err, &failure) {
				return nil, &ratchetError{Code: "unavailable", Err: err}
			}
			return nil, err
		}
		var out struct {
			RequestID string          `json:"request_id"`
			Error     string          `json:"error"`
			Data      json.RawMessage `json:"data"`
		}
		if json.Unmarshal(f.Payload, &out) != nil || out.RequestID != r.RequestID {
			return nil, errors.New("uncorrelated Olm admission result")
		}
		if out.Error != "" {
			return nil, rfail(out.Error)
		}
		return out.Data, nil
	}
	directoryReq := func(_ context.Context, r netproto.OlmDirectoryRequest) (netproto.OlmDirectoryResult, error) {
		// Claims keep their reserved request ID: it is the admitted claim ID.
		if r.RequestID == "" {
			r.RequestID = newClientMsgID()
		}
		f, err := m.requestOn(conn, netproto.MsgOlmDirectoryRequest, netproto.MsgOlmDirectoryResult, r, 15*time.Second)
		if err != nil {
			var failure *requestFailure
			if errors.As(err, &failure) {
				return netproto.OlmDirectoryResult{}, &ratchetError{Code: "unavailable", Err: err}
			}
			return netproto.OlmDirectoryResult{}, err
		}
		var out netproto.OlmDirectoryResult
		if json.Unmarshal(f.Payload, &out) != nil || out.RequestID != r.RequestID {
			return out, errors.New("uncorrelated Olm directory result")
		}
		if out.Error != "" {
			return out, rfail(out.Error)
		}
		return out, nil
	}
	return admissionReq, directoryReq
}

// ratchetDeviceKey returns a device's noXa signing key from the pinned root
// chain (dmDeviceList: own identity, or the TOFU-pinned contact record plus
// approval signatures), refreshing once for a newly linked device.
func (m *connManager) ratchetDeviceKey(ctx context.Context, principal, device string) (ed25519.PublicKey, error) {
	order := []bool{false, true}
	if fresh, _ := ctx.Value(ratchetFreshKey{}).(bool); fresh {
		order = []bool{true}
	}
	for _, force := range order {
		devices, err := m.dmDeviceList(principal, force)
		if err != nil {
			if errors.Is(err, errDMIdentityChanged) {
				// Reach the explicit confirmation from the new-protocol path
				// too; nothing is adopted until the user confirms.
				m.promptRatchetIdentity(principal)
				return nil, &ratchetError{Code: "identity_changed", Err: err}
			}
			if errors.Is(err, errDMDevicesUnverified) || errors.Is(err, devicetrust.ErrUntrusted) {
				return nil, &ratchetError{Code: "untrusted_device", Err: err}
			}
			return nil, err
		}
		for _, d := range devices {
			if d.DeviceID == device {
				return d.Signing, nil
			}
		}
	}
	return nil, rfail("untrusted_device")
}

// --- Product send entries ----------------------------------------------------

type ratchetTextBody struct {
	Text   string  `json:"text"`
	SentAt float64 `json:"sentAt"`
}

func ratchetChatBody(text string) json.RawMessage {
	return ratchetJSON(ratchetTextBody{Text: text, SentAt: float64(time.Now().UnixMilli()) / 1000})
}

// ratchetSent reports whether an operation counts as sent: an accepted
// receipt is success even if later housekeeping failed.
func ratchetSent(out RatchetOutcome, err error) (RatchetOutcome, bool) {
	if err == nil {
		return out, true
	}
	var r *ratchetError
	if errors.As(err, &r) && r.Committed != nil && r.Committed.Receipt != nil && r.Committed.Receipt.State == "accepted" {
		return *r.Committed, true
	}
	return out, false
}

// ratchetFailure is the user-facing text of a failed new-protocol send. A
// committed attempt without a terminal receipt is still pending: its ORIGINAL
// text stays queued and later resolves exactly; it is never resent changed.
func ratchetFailure(err error) error {
	var r *ratchetError
	if ratchetCode(err) == "logical_mismatch" {
		return errors.New("a different message was already sent with this operation; nothing new was sent")
	}
	if errors.As(err, &r) && r.Committed != nil && r.Committed.Receipt == nil && r.Code != "clock_skew" {
		return fmt.Errorf("secure delivery is not confirmed yet (%s); the original message stays queued and is resolved exactly", r.Code)
	}
	if code := ratchetCode(err); code != "" {
		return fmt.Errorf("secure send failed: %s", code)
	}
	return fmt.Errorf("secure send failed: %w", err)
}

// sendRatchetDirect is the single durable path for direct text: immediate
// sends and queued outbox entries pass a stable logical ID captured at intent.
func (m *connManager) sendRatchetDirect(conn net.Conn, logicalID, peer, text string) (RatchetOutcome, error) {
	c, err := m.ratchetConsumerOn(conn)
	if err != nil {
		return RatchetOutcome{}, err
	}
	intent := ratchetIntent{LogicalID: logicalID, Scope: "direct", Peer: peer, Purpose: "chat", Body: ratchetChatBody(text)}
	out, err := c.send(intent)
	if sent, ok := ratchetSent(out, err); ok {
		// A reused operation ID with different content never reports the
		// original as this message being sent.
		if sent.Reused && !ratchetSameContent(sent.Record, intent) {
			return sent, &ratchetError{Code: "logical_mismatch", Committed: &sent}
		}
		return sent, nil
	}
	return out, err
}

// ConversationSendResult is the typed group send result. New-protocol
// transport IDs are opaque strings; MessageID is set only for legacy sends.
type ConversationSendResult struct {
	Protocol  string         `json:"protocol"`
	MessageID int64          `json:"message_id,omitempty"`
	Transport string         `json:"transport,omitempty"`
	Key       string         `json:"key,omitempty"`
	LogicalID string         `json:"logical_id,omitempty"`
	Status    string         `json:"status,omitempty"`
	Reused    bool           `json:"reused,omitempty"`
	Record    *RatchetRecord `json:"record,omitempty"`
}

func (m *connManager) sendRatchetGroup(groupID, reference, text string) (ConversationSendResult, error) {
	c, err := m.ratchetConsumerOn(nil)
	if err != nil {
		return ConversationSendResult{}, err
	}
	intent := ratchetIntent{LogicalID: reference, Scope: "group", Conversation: groupID, Purpose: "chat", Body: ratchetChatBody(text)}
	out, err := c.send(intent)
	out, ok := ratchetSent(out, err)
	if !ok {
		return ConversationSendResult{}, ratchetFailure(err)
	}
	if out.Reused && !ratchetSameContent(out.Record, intent) {
		// The committed original stays authoritative; the changed text was
		// NOT sent under this reference.
		return ConversationSendResult{}, ratchetFailure(&ratchetError{Code: "logical_mismatch", Committed: &out})
	}
	return ConversationSendResult{Protocol: ratchetProtocol, Transport: out.Receipt.MessageID, Key: out.Record.Key, LogicalID: out.Record.LogicalID,
		Status: out.Receipt.State, Reused: out.Reused, Record: &out.Record}, nil
}

// changeRatchetDirect sends an authenticated edit/delete of one of this
// account's new-protocol messages. reference is its sender logical ID.
func (m *connManager) changeRatchetDirect(peer, reference, action, text string) error {
	c, err := m.ratchetConsumerOn(nil)
	if err != nil {
		return err
	}
	body := json.RawMessage(`{}`)
	if action == netproto.DMActionEdit {
		body = ratchetJSON(struct {
			Text string `json:"text"`
		}{text})
	}
	// One logical ID per change operation: a delete of a message is one
	// operation forever (deterministic); an edit keeps its ID until it is
	// accepted, so a retry after uncertainty resolves the original change.
	id := "delete:" + reference
	key := ""
	if action == netproto.DMActionEdit {
		key = peer + "\x00" + reference + "\x00" + text
		minted, _ := m.ratchetEdits.LoadOrStore(key, "edit:"+newClientMsgID())
		id = minted.(string)
		// Reuse an edit ID only while that edit is unresolved (or resealable);
		// once accepted, a later identical edit is a new operation.
		if state, err := c.logicalState(id); err == nil && state == "accepted" {
			id = "edit:" + newClientMsgID()
			m.ratchetEdits.Store(key, id)
		}
	}
	out, err := c.send(ratchetIntent{LogicalID: id, Scope: "direct", Peer: peer, Purpose: action, Reference: reference, Body: body})
	if _, ok := ratchetSent(out, err); !ok {
		var r *ratchetError
		if key != "" && !(errors.As(err, &r) && r.Committed != nil && r.Committed.Receipt == nil) {
			m.ratchetEdits.Delete(key) // nothing uncertain left to resolve
		}
		return ratchetFailure(err)
	}
	if key != "" {
		m.ratchetEdits.Delete(key)
	}
	return nil
}

// --- Projection --------------------------------------------------------------

// RatchetMessageEvent is the "ratchet_message" event: one authoritative
// archive record. IsNew comes from the durable receive outcome only.
type RatchetMessageEvent struct {
	RatchetRecord
	IsNew bool `json:"is_new"`
}

func (m *connManager) projectRatchet(r RatchetRecord, isNew bool, current func() error) {
	if r.Protocol != ratchetProtocol {
		return
	}
	if hook := m.ratchetProject; hook != nil {
		hook(m, r, isNew)
	}
	// Ownership recheck after the projector's disk I/O (P3-3).
	if current != nil && current() != nil {
		return
	}
	data, err := json.Marshal(RatchetMessageEvent{RatchetRecord: r, IsNew: isNew})
	if err != nil {
		return
	}
	env, err := json.Marshal(map[string]any{"type": "ratchet_message", "data": json.RawMessage(data)})
	if err != nil {
		return
	}
	m.emit("event", string(env))
}

func (m *connManager) showRatchetQuarantine(list []RatchetQuarantine) {
	if list == nil {
		list = []RatchetQuarantine{}
	}
	data, err := json.Marshal(map[string]any{"type": "ratchet_quarantine", "data": map[string]any{"placeholders": list}})
	if err != nil {
		return
	}
	m.emit("event", string(data))
}

func ratchetBodyText(body json.RawMessage) string {
	var b struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(body, &b)
	return b.Text
}

// ratchetPendingRef keeps pending new-protocol changes apart from legacy
// client message IDs in the shared dmPending map.
func ratchetPendingRef(ref string) string { return "olm\x00" + ref }

// projectRatchetRecord merges one direct record into the identity's sealed DM
// log, idempotently: entries are keyed by (protocol, sender, logical ID), so
// legacy entries and other senders reusing a logical string never collide.
// The log is a view; the protected archive replays it after a crash.
func (a *App) projectRatchetRecord(m *connManager, r RatchetRecord, _ bool) {
	if r.Scope != "direct" || r.Peer == "" {
		return
	}
	dir, err := a.dmHistoryDir()
	if err != nil {
		return
	}
	id, err := m.identity()
	if err != nil {
		return
	}
	store, err := dmHistoryStoreForIdentity(dir, id)
	if err != nil {
		return
	}
	a.dmMu.Lock()
	defer a.dmMu.Unlock()
	l, err := store.load(r.Peer)
	if err != nil {
		log.Printf("ratchet projection: %v", err)
		return
	}
	l.Peer = r.Peer
	find := func(sender, logical string) *DMEntry {
		for i := range l.Messages {
			e := &l.Messages[i]
			if e.Protocol == ratchetProtocol && e.FromUniqueID == sender && e.ClientMsgID == logical {
				return e
			}
		}
		return nil
	}
	if r.Purpose == netproto.DMActionEdit || r.Purpose == netproto.DMActionDelete {
		// An own change applies only once accepted (a linked copy from another
		// own device carries no status and was accepted there).
		if r.Self && r.Status != "" && r.Status != "accepted" {
			return
		}
		c := dmChange{Action: r.Purpose, Ref: r.Reference, From: r.Sender, Body: ratchetBodyText(r.Body), At: int64(r.SentAt)}
		if r.Purpose == netproto.DMActionEdit && strings.TrimSpace(c.Body) == "" {
			return
		}
		if e := find(r.Sender, r.Reference); e != nil {
			applyDMChange(e, c)
			e.EncVerified, e.EncMode = true, "e2e"
			_ = store.save(l)
		} else {
			a.dmPending.add(store.path(r.Peer), dmChange{Action: c.Action, Ref: ratchetPendingRef(c.Ref), From: c.From, Body: c.Body, At: c.At}, time.Now())
		}
		return
	}
	status := r.Status
	if status == "" && !r.Self {
		status = "received"
	}
	if e := find(r.Sender, r.LogicalID); e != nil {
		// A terminal state never regresses to pending (stale replay snapshot).
		if e.Status == status || status == "pending" && (e.Status == "accepted" || e.Status == "rejected" || e.Status == "aborted") {
			return
		}
		e.Status = status
		_ = store.save(l)
		return
	}
	e := DMEntry{FromUniqueID: r.Sender, Body: ratchetBodyText(r.Body), SentAt: int64(r.SentAt), Self: r.Self, ClientMsgID: r.LogicalID,
		EncVerified: true, EncMode: "e2e", Protocol: ratchetProtocol, Key: r.Key, Status: status}
	if n := len(l.Messages); n > 0 {
		e.Seq = l.Messages[n-1].Seq + 1
	} else {
		e.Seq = 1
	}
	l.Messages = append(l.Messages, e)
	for _, change := range a.dmPending.take(store.path(r.Peer), ratchetPendingRef(r.LogicalID), time.Now()) {
		if change.From == r.Sender {
			applyDMChange(&l.Messages[len(l.Messages)-1], change)
		}
	}
	if err := store.save(l); err != nil {
		log.Printf("ratchet projection: %v", err)
	}
}

// RatchetConversationHistoryForTab returns the authoritative new-protocol
// records of one private group (own sends and receipts), oldest first. Legacy
// numeric server history stays available through ConversationForTab.
func (a *App) RatchetConversationHistoryForTab(tabID, groupID string) ([]RatchetRecord, error) {
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return nil, err
	}
	return m.ratchetConversationHistory(groupID)
}

func (m *connManager) ratchetConversationHistory(groupID string) ([]RatchetRecord, error) {
	if !m.ratchetConfigured() {
		return []RatchetRecord{}, nil
	}
	c, err := m.ratchetConsumerOn(nil)
	if err != nil {
		return nil, err
	}
	records, err := c.history()
	if err != nil {
		return nil, err
	}
	out := []RatchetRecord{}
	for _, r := range records {
		if r.Scope == "group" && r.Conversation == groupID {
			out = append(out, r)
		}
	}
	return out, nil
}

// applyRatchetTrustReset applies a durable, explicitly confirmed identity
// change right away when a session is live. The intent itself is stored with
// the contact pin, so a missing session or a failed reset is retried by the
// next trust check of the consumer; a failure is surfaced, not only logged.
func (m *connManager) applyRatchetTrustReset(uid string) {
	c, err := m.ratchetConsumerOn(nil)
	if err != nil {
		return
	}
	token, err := m.ratchetResetPending(uid)
	if err != nil || token == "" {
		return
	}
	// The intent is cleared by the consumer's next trust check once the new
	// key is fetched fresh and pinned; here the old pins are dropped now.
	err = c.resetTrust(uid)
	if err != nil {
		log.Printf("ratchet: trust reset for %s stays pending: %v", uid, err)
		m.emitRatchetTrust(uid, "pending", err)
	}
}

func (m *connManager) emitRatchetTrust(uid, state string, err error) {
	payload := map[string]any{"contact": uid, "state": state}
	if err != nil {
		payload["error"] = ratchetCode(err)
	}
	data, _ := json.Marshal(map[string]any{"type": "ratchet_trust", "data": payload})
	m.emit("event", string(data))
}

var ratchetPrompts sync.Map // manager+contact -> confirmation in flight

// promptRatchetIdentity asks the user (once at a time per contact) to confirm
// a changed contact identity that blocked a new-protocol operation. Only the
// explicit confirmation pins the new key and stores the ratchet reset intent.
// A change the user declined (that exact new fingerprint) is not re-prompted
// for 10 minutes; a transient lookup failure is not a decline.
func (m *connManager) promptRatchetIdentity(uid string) {
	if m.recentlyDeclined(uid) {
		m.emitRatchetTrust(uid, "blocked", nil)
		return
	}
	key := fmt.Sprintf("%p\x00%s", m, uid)
	if _, busy := ratchetPrompts.LoadOrStore(key, true); busy {
		return
	}
	m.emitRatchetTrust(uid, "confirmation_required", nil)
	go guardCrash("ratchet identity prompt", func() {
		defer ratchetPrompts.Delete(key)
		if _, result := m.lookupPeerPubKey(uid); result == peerKeyFound {
			m.forgetDMDevices(uid)
		}
	})
}

// recentlyDeclined reports whether the contact's currently pending new
// identity was explicitly declined within the last 10 minutes.
func (m *connManager) recentlyDeclined(uid string) bool {
	path, _, err := m.peerPinPath(uid)
	if err != nil {
		return false
	}
	dmTrustMu.Lock()
	pin, err := readDMPin(path)
	dmTrustMu.Unlock()
	if err != nil || pin.Pending == "" {
		return false
	}
	record, err := e2ee.ParseKeyRecord(pin.Pending, uid)
	if err != nil {
		return false
	}
	v, ok := dmDeclined.Load(path + "\x00" + record.Fingerprint())
	return ok && time.Since(v.(time.Time)) < 10*time.Minute
}

// ratchetStoreDir is the protected store directory of one identity under a
// base directory: every server/device namespace of that identity lives there,
// so identity deletion destroys exactly its own namespaces.
func ratchetStoreDir(base string, id *identity) string {
	sum := sha256.Sum256([]byte("noxa-ratchet-identity-v1\x00" + id.PublicKey))
	return filepath.Join(base, hex.EncodeToString(sum[:16]))
}

// defaultRatchetBase is the production base (next to the identities).
func defaultRatchetBase() (string, error) {
	root, err := identityRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "ratchet"), nil
}

// ratchetDeleted fences identities whose namespaces were destroyed: no new
// session may open them again in this process.
var ratchetDeleted sync.Map

// destroyRatchetNamespaces destroys an identity's stores without removing its
// identity file (tests; DeleteIdentity uses deleteIdentityWithStores).
func (a *App) destroyRatchetNamespaces(id *identity) error {
	return a.deleteIdentityWithStores(id, "")
}

// deleteIdentityWithStores runs on IDENTITY DELETION only (a sign-out or
// disconnect keeps the namespaces and readable archive). Steps, serialised
// with every store open of this identity:
//
//  1. fence new sessions and stop + await this process's live sessions;
//  2. probe every store for a lock held by any other opener (all platforms);
//     a held store fails the deletion with nothing changed;
//  3. rename every store directory to a tombstone (the pending-wipe record);
//  4. remove the identity file — the commit point;
//  5. erase the tombstones (retried by the sweep at startup and before the
//     next deletion if erasing fails).
//
// A failure before the commit point rolls back (renames back, fence lifted,
// sessions restarted). A crash before it leaves tombstones whose identity
// still exists: the sweep restores them; after it, the sweep erases them.
func (a *App) deleteIdentityWithStores(id *identity, identityPath string) error {
	unlock := lockRatchetIdentity(id.PublicKey)
	defer unlock()
	bases := a.ratchetBases()
	sweepRatchetTombstones(bases)
	ratchetDeleted.Store(id.PublicKey, true)
	a.tabsMu.Lock()
	managers := make([]*connManager, 0, len(a.tabs))
	for _, tab := range a.tabs {
		if tab != nil && tab.cm != nil {
			managers = append(managers, tab.cm)
		}
	}
	a.tabsMu.Unlock()
	var stopped []*connManager
	for _, cm := range managers {
		cm.mu.Lock()
		var s *ratchetSession
		if cm.id != nil && cm.id.PublicKey == id.PublicKey {
			s = cm.takeRatchetLocked()
		}
		cm.mu.Unlock()
		if s != nil {
			s.stop()
			stopped = append(stopped, cm)
		}
	}
	type move struct{ dir, tomb string }
	var moved []move
	rollback := func(cause error) error {
		var lost []string
		for _, mv := range moved {
			if err := os.Rename(mv.tomb, mv.dir); err != nil {
				lost = append(lost, mv.tomb)
			}
		}
		restoreRatchetIdentity(id.PublicKey) // the identity survives
		for _, cm := range stopped {
			cm.mu.Lock()
			conn := cm.conn
			cm.mu.Unlock()
			go guardCrash("ratchet restart", func() { cm.startRatchetRecorded(conn) })
		}
		if len(lost) > 0 {
			return fmt.Errorf("%w; the store could not be restored yet and is restored at next start", cause)
		}
		return cause
	}
	// Our own sessions are stopped; any remaining lock belongs to another
	// opener (another process): fail before anything is renamed.
	for base := range bases {
		if err := durable.ProbeUnlocked(ratchetStoreDir(base, id)); err != nil {
			return rollback(fmt.Errorf("secure message store of this identity is in use: %w", err))
		}
	}
	for base := range bases {
		dir := ratchetStoreDir(base, id)
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			continue
		}
		tomb := dir + ratchetTombstone + newClientMsgID()
		if err := os.Rename(dir, tomb); err != nil {
			return rollback(fmt.Errorf("secure message store of this identity is in use: %w", err))
		}
		moved = append(moved, move{dir, tomb})
	}
	if identityPath != "" {
		if err := os.Remove(identityPath); err != nil {
			return rollback(err)
		}
	}
	for _, mv := range moved {
		if err := os.RemoveAll(mv.tomb); err != nil {
			log.Printf("ratchet: erasing a deleted store failed, retried at next start: %v", err)
			go guardCrash("ratchet wipe retry", func() {
				time.Sleep(30 * time.Second)
				sweepRatchetTombstones(map[string]bool{filepath.Dir(mv.tomb): true})
			})
		}
	}
	return nil
}

const ratchetTombstone = ".deleted-"

// ratchetBases lists the store bases in use (production + test overrides).
func (a *App) ratchetBases() map[string]bool {
	bases := map[string]bool{}
	if base, err := defaultRatchetBase(); err == nil {
		bases[base] = true
	}
	a.tabsMu.Lock()
	for _, tab := range a.tabs {
		if tab != nil && tab.cm != nil {
			tab.cm.mu.Lock()
			if cfg := tab.cm.ratchetTest; cfg != nil && cfg.Dir != "" {
				bases[cfg.Dir] = true
			}
			tab.cm.mu.Unlock()
		}
	}
	a.tabsMu.Unlock()
	return bases
}

// sweepRatchetTombstones finishes interrupted deletions: a tombstone whose
// identity still exists (crash before the commit point, or a failed
// rollback) is restored when its live directory is absent; any other
// tombstone is erased.
func sweepRatchetTombstones(bases map[string]bool) {
	live := map[string]bool{}
	if dir, err := identitiesDir(); err == nil {
		for _, id := range identityIDs(dir) {
			if path, err := identityPathFor(id); err == nil {
				if loaded, err := loadIdentityAtStrict(path); err == nil {
					live[filepath.Base(ratchetStoreDir("", loaded))] = true
				}
			}
		}
	}
	for base := range bases {
		entries, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			i := strings.Index(name, ratchetTombstone)
			if !e.IsDir() || i <= 0 {
				continue
			}
			tomb, dir := filepath.Join(base, name), filepath.Join(base, name[:i])
			if live[name[:i]] {
				if _, err := os.Stat(dir); os.IsNotExist(err) {
					if err := os.Rename(tomb, dir); err != nil {
						log.Printf("ratchet: restoring an interrupted deletion failed: %v", err)
					}
					continue
				}
			}
			if err := os.RemoveAll(tomb); err != nil {
				log.Printf("ratchet: erasing a deleted store failed: %v", err)
			}
		}
	}
}

// SweepRatchetStores runs once at startup.
func (a *App) sweepRatchetStores() {
	a.identityMu.Lock()
	defer a.identityMu.Unlock()
	sweepRatchetTombstones(a.ratchetBases())
}

// restoreRatchetIdentity lifts the deletion fence (deletion failed, or the
// same identity was imported again).
func restoreRatchetIdentity(publicKey string) { ratchetDeleted.Delete(publicKey) }

// startRatchetRecorded starts the session of conn (single-flight per
// connection) and records a failure for the outbox (truthful blocked state,
// bounded background retries). A start that finishes for a replaced
// connection kicks the start of the current one, so a reconnect during a slow
// start never leaves the new connection without a session.
func (m *connManager) startRatchetRecorded(conn net.Conn) {
	m.mu.Lock()
	if conn == nil || m.ratchetStartingConn == conn || m.conn != conn {
		m.mu.Unlock()
		return
	}
	m.ratchetStartingConn, m.ratchetStartAt = conn, time.Now()
	m.mu.Unlock()
	err := m.startRatchet(conn)
	m.mu.Lock()
	if m.ratchetStartingConn == conn {
		m.ratchetStartingConn = nil
	}
	current := m.conn
	kick := current != nil && current != conn && m.ratchetSess == nil && m.ratchetStartingConn == nil && m.ratchetTest != nil
	if current == conn {
		m.ratchetStartErr = err
	}
	m.mu.Unlock()
	if err != nil && current == conn {
		log.Printf("ratchet session unavailable: %v", err)
	}
	if kick {
		go guardCrash("ratchet start", func() { m.startRatchetRecorded(current) })
	}
}

// ratchetBlockedReason is the waiting entries' text while the configured
// session cannot attach; it schedules a bounded retry (30 s).
func (m *connManager) ratchetBlockedReason() string {
	m.mu.Lock()
	err, conn := m.ratchetStartErr, m.conn
	retry := err != nil && conn != nil && m.ratchetStartingConn == nil && time.Since(m.ratchetStartAt) > 30*time.Second
	m.mu.Unlock()
	if err == nil {
		return ""
	}
	if retry {
		go guardCrash("ratchet restart", func() { m.startRatchetRecorded(conn) })
	}
	return "secure messaging unavailable: " + err.Error()
}
