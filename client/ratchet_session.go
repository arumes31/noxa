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
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
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
		handle, err = durable.OpenShared(cfg.Dir, ns, *cfg.Protection)
	} else {
		_, secret, e := identity.x25519()
		if e != nil {
			return e
		}
		handle, err = openDurableRatchets(cfg.Dir, ns, secret[:])
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
	})
	if err != nil {
		_ = handle.Close()
		return err
	}
	s := &ratchetSession{conn: conn, epoch: epoch, handle: handle, consumer: consumer, quit: make(chan struct{}), done: make(chan struct{})}
	consumer.project(func(r RatchetRecord, isNew bool) { m.projectRatchet(r, isNew) })
	m.mu.Lock()
	if m.conn != conn || m.connEpoch != epoch || m.closed {
		m.mu.Unlock()
		close(s.done)
		s.stop()
		return errRatchetUnavailable
	}
	old := m.ratchetSess
	m.ratchetSess = s
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
				if key := string(ratchetJSON(list)); key != last {
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
func (m *connManager) ratchetDeviceKey(_ context.Context, principal, device string) (ed25519.PublicKey, error) {
	for _, force := range []bool{false, true} {
		devices, err := m.dmDeviceList(principal, force)
		if err != nil {
			if errors.Is(err, errDMDevicesUnverified) || errors.Is(err, devicetrust.ErrUntrusted) || errors.Is(err, errDMIdentityChanged) {
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
	out, err := c.send(ratchetIntent{LogicalID: logicalID, Scope: "direct", Peer: peer, Purpose: "chat", Body: ratchetChatBody(text)})
	if sent, ok := ratchetSent(out, err); ok {
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
	out, err := c.send(ratchetIntent{LogicalID: reference, Scope: "group", Conversation: groupID, Purpose: "chat", Body: ratchetChatBody(text)})
	out, ok := ratchetSent(out, err)
	if !ok {
		return ConversationSendResult{}, ratchetFailure(err)
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
	out, err := c.send(ratchetIntent{LogicalID: newClientMsgID(), Scope: "direct", Peer: peer, Purpose: action, Reference: reference, Body: body})
	if _, ok := ratchetSent(out, err); !ok {
		return ratchetFailure(err)
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

func (m *connManager) projectRatchet(r RatchetRecord, isNew bool) {
	if r.Protocol != ratchetProtocol {
		return
	}
	if hook := m.ratchetProject; hook != nil {
		hook(m, r, isNew)
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
		if e.Status == status {
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

// resetRatchetTrust follows an explicitly confirmed contact identity change.
func (m *connManager) resetRatchetTrust(uid string) {
	if c, err := m.ratchetConsumerOn(nil); err == nil {
		if err := c.resetTrust(uid); err != nil {
			log.Printf("ratchet: reset trust: %v", err)
		}
	}
}
