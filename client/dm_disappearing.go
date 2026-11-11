// dm_disappearing.go: disappearing direct messages (plan item 28).
//
// Direct messages are end-to-end encrypted (noxa-olm-v1), so the server cannot
// enforce expiry: every device does it for its own copies.
//
//   - The sender puts "ttl" (seconds) inside the encrypted body of each chat;
//     receivers never trust server metadata for it.
//   - A setting change is a chat whose body carries "disappearing" (seconds,
//     0 = off) next to readable text, so an older app still shows what
//     happened. The newest change by either side is the conversation timer.
//   - The own copy expires TTL after sending, a received copy TTL after it
//     was first displayed on this device (StartDirectDisappearingForTab).
//   - On expiry the log entry loses its body (Deleted + Expired: search,
//     previews and exports already skip deleted entries) and every archive
//     record of the message and of its edits is redacted in the protected
//     store (durable.RedactArchive), so a history replay cannot bring the
//     text back. Redaction needs a connected session; it is retried until
//     done (Redacted).
//
// Copies are not a security boundary: a recipient can still screenshot or
// copy a message before it expires (the UI says so).
package main

import (
	"encoding/json"
	"errors"
	"log"
	"sync"
	"time"

	"noxa/internal/e2ee/durable"
	"noxa/internal/netproto"
)

// dmDisappearMaxTTL bounds a received TTL (four weeks).
const dmDisappearMaxTTL int64 = 28 * 24 * 3600

// dmDisappearOptions are the timers this app offers (seconds).
var dmDisappearOptions = map[int64]bool{30: true, 300: true, 3600: true, 86400: true, 604800: true}

// dmDisappearEvent tells the frontend which messages expired.
const dmDisappearEvent = "dm_expired"

// dmDisappearSweepInterval is how often the sweeper checks for due expiries.
var dmDisappearSweepInterval = 5 * time.Second

func validDisappearTTL(ttl int64) bool { return ttl > 0 && ttl <= dmDisappearMaxTTL }

// ratchetExpiredBody replaces the body of a redacted archive record.
var ratchetExpiredBody = json.RawMessage(`{"expired":true}`)

// ratchetBodyDisappearing reads the disappearing fields of a decrypted body:
// the message TTL, a setting change, or the expired marker of a redacted
// record. Out-of-range values are ignored.
func ratchetBodyDisappearing(body json.RawMessage) (ttl int64, timer *int64, expired bool) {
	var b struct {
		TTL          int64  `json:"ttl"`
		Disappearing *int64 `json:"disappearing"`
		Expired      bool   `json:"expired"`
	}
	if json.Unmarshal(body, &b) != nil {
		return 0, nil, false
	}
	if validDisappearTTL(b.TTL) {
		ttl = b.TTL
	}
	if b.Disappearing != nil && (*b.Disappearing == 0 || validDisappearTTL(*b.Disappearing)) {
		v := *b.Disappearing
		timer = &v
	}
	return ttl, timer, b.Expired
}

// ratchetChatBodyWith is ratchetChatBody with an optional TTL or setting change.
func ratchetChatBodyWith(text string, ttl int64, timer *int64) json.RawMessage {
	return ratchetJSON(struct {
		Text         string  `json:"text"`
		SentAt       float64 `json:"sentAt"`
		TTL          int64   `json:"ttl,omitempty"`
		Disappearing *int64  `json:"disappearing,omitempty"`
	}{text, float64(time.Now().UnixMilli()) / 1000, ttl, timer})
}

// dmTimerSkew is how far ahead of this device's clock a contact's send time
// may order a timer change.
const dmTimerSkew = 5 * time.Minute

// applyRatchetDisappearing records the disappearing fields of a new direct
// record on its log entry. It returns false for a redacted (expired) record,
// which is never shown again as a message.
func applyRatchetDisappearing(l *dmLog, e *DMEntry, r RatchetRecord, now time.Time) bool {
	ttl, timer, expired := ratchetBodyDisappearing(r.Body)
	if expired {
		return false
	}
	if timer != nil {
		// A setting change is a system line and never expires itself.
		e.Timer = timer
		// The send time is sender-chosen: a contact cannot date a change so
		// far ahead that no later change could replace it.
		order := r.SentAt
		if limit := float64(now.Add(dmTimerSkew).Unix()); !r.Self && order > limit {
			order = limit
		}
		if order >= l.DisappearingAt {
			l.Disappearing, l.DisappearingAt = *timer, order
		}
		return true
	}
	if ttl > 0 {
		e.TTL = ttl
		if r.Self {
			e.ExpiresAt = int64(r.SentAt) + ttl
		}
	}
	return true
}

// dmDisappearState tracks the earliest pending expiry so the sweeper only
// opens logs when something is due. next: 0 = unknown (scan everything),
// -1 = nothing pending, otherwise a unix time.
type dmDisappearState struct {
	mu   sync.Mutex
	next int64
	// redact: expired messages whose archive copy still needs redacting;
	// redactAt throttles those archive scans; misses counts scans in which a
	// connected archive did not hold a pending message.
	redact   bool
	redactAt time.Time
	misses   map[string]int
	// pending caches expired messages still to redact (semantic key ->
	// archive server, "" = unknown); nil = reload from the logs.
	pending map[string]string
}

// dmRedactRetry throttles archive scans for pending redactions, and
// dmRedactGiveUp is how many scans may miss a message without a known
// archive (absent from every archive while every tab has a session) before
// it counts as having nothing left to redact.
var (
	dmRedactRetry  = 30 * time.Second
	dmRedactGiveUp = 5
)

// noteDisappearing lowers the next sweep time to at (unix seconds).
func (a *App) noteDisappearing(at int64) {
	if at <= 0 {
		return
	}
	a.dmDisappear.mu.Lock()
	if a.dmDisappear.next <= 0 || at < a.dmDisappear.next {
		a.dmDisappear.next = at
	}
	a.dmDisappear.mu.Unlock()
}

// dmDisappearingTTL returns peer's current timer for a new message (0 = off).
func (a *App) dmDisappearingTTL(m *connManager, peer string) int64 {
	if peer == "" {
		return 0
	}
	dir, err := a.dmHistoryDir()
	if err != nil {
		return 0
	}
	id, err := m.identity()
	if err != nil {
		return 0
	}
	store, err := dmHistoryStoreForIdentity(dir, id)
	if err != nil {
		return 0
	}
	a.dmMu.Lock()
	defer a.dmMu.Unlock()
	l, err := store.load(peer)
	if err != nil || !validDisappearTTL(l.Disappearing) {
		return 0
	}
	return l.Disappearing
}

// DirectDisappearingForTab returns the disappearing-messages timer of the
// conversation with peer, in seconds (0 = off).
func (a *App) DirectDisappearingForTab(tabID, peer string) (int64, error) {
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return 0, err
	}
	return a.dmDisappearingTTL(m, peer), nil
}

// SetDirectDisappearingForTab turns disappearing messages with peer on
// (seconds: one of the offered timers) or off (0). The change travels as an
// end-to-end encrypted message to every device of both accounts; notice is
// the readable line older apps show instead of the system line. It returns
// "" or the error.
func (a *App) SetDirectDisappearingForTab(tabID, peer string, seconds int64, notice string) string {
	if peer == "" || (seconds != 0 && !dmDisappearOptions[seconds]) {
		return "invalid disappearing-messages timer"
	}
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return err.Error()
	}
	if !m.ratchetConfigured() {
		return "disappearing messages need secure messaging (an updated server and app)"
	}
	if notice == "" {
		notice = "Disappearing messages changed"
	}
	c, err := m.ratchetSendConsumer(nil)
	if err != nil {
		return ratchetFailure(err).Error()
	}
	timer := seconds
	intent := ratchetIntent{LogicalID: "timer:" + newClientMsgID(), Scope: "direct", Peer: peer, Purpose: "chat", Body: ratchetChatBodyWith(notice, 0, &timer)}
	out, err := c.send(intent)
	if _, ok := ratchetSent(out, err); !ok {
		return ratchetFailure(err).Error()
	}
	return ""
}

// DMExpiry is the expiry of one message (unix seconds).
type DMExpiry struct {
	From        string `json:"from_unique_id"`
	ClientMsgID string `json:"client_msg_id"`
	ExpiresAt   int64  `json:"expires_at"`
}

// StartDirectDisappearingForTab starts the timers of received disappearing
// messages from peer that are now on screen. It returns the expiries started.
func (a *App) StartDirectDisappearingForTab(tabID, peer string) ([]DMExpiry, error) {
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return nil, err
	}
	dir, err := a.dmHistoryDir()
	if err != nil {
		return nil, err
	}
	id, err := m.identity()
	if err != nil {
		return nil, err
	}
	store, err := dmHistoryStoreForIdentity(dir, id)
	if err != nil {
		return nil, err
	}
	return a.startDisappearingWith(store, peer, time.Now())
}

func (a *App) startDisappearingWith(store dmHistoryStore, peer string, now time.Time) ([]DMExpiry, error) {
	if peer == "" {
		return nil, errors.New("peer is required")
	}
	a.dmMu.Lock()
	defer a.dmMu.Unlock()
	l, err := store.load(peer)
	if err != nil {
		return nil, err
	}
	out := []DMExpiry{}
	for i := range l.Messages {
		e := &l.Messages[i]
		if e.Self || e.TTL <= 0 || e.ExpiresAt != 0 || e.Deleted || e.Expired {
			continue
		}
		e.ExpiresAt = now.Unix() + e.TTL
		out = append(out, DMExpiry{From: e.FromUniqueID, ClientMsgID: e.ClientMsgID, ExpiresAt: e.ExpiresAt})
		a.noteDisappearing(e.ExpiresAt)
	}
	if len(out) > 0 {
		if err := store.save(l); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// dmExpiredMessage identifies one expired message for the frontend and for
// archive redaction.
type dmExpiredMessage struct {
	From        string `json:"from_unique_id"`
	ClientMsgID string `json:"client_msg_id"`
}

// expireDueWith deletes the bodies of messages due at now in every log of
// store. It returns the expired messages per peer, whether any expired entry
// still needs its archive redacted, and the next pending expiry (0 = none).
func (a *App) expireDueWith(store dmHistoryStore, now time.Time) (map[string][]dmExpiredMessage, bool, int64) {
	expired := map[string][]dmExpiredMessage{}
	needRedact := false
	var next int64
	for _, p := range store.peers() {
		a.dmMu.Lock()
		l, err := store.load(p.UniqueID)
		if err != nil {
			a.dmMu.Unlock()
			continue
		}
		changed := false
		for i := range l.Messages {
			e := &l.Messages[i]
			if e.Expired {
				needRedact = needRedact || (!e.Redacted && e.Protocol == ratchetProtocol)
				continue
			}
			if e.ExpiresAt <= 0 {
				continue
			}
			if e.ExpiresAt > now.Unix() {
				if next == 0 || e.ExpiresAt < next {
					next = e.ExpiresAt
				}
				continue
			}
			e.Body, e.Deleted, e.Expired, e.Edited = "", true, true, false
			e.Redacted = e.Protocol != ratchetProtocol // legacy records have no archive copy
			needRedact = needRedact || !e.Redacted
			expired[l.Peer] = append(expired[l.Peer], dmExpiredMessage{From: e.FromUniqueID, ClientMsgID: e.ClientMsgID})
			changed = true
		}
		if changed {
			if err := store.save(l); err != nil {
				log.Printf("disappearing messages: %v", err)
			}
		}
		a.dmMu.Unlock()
	}
	return expired, needRedact, next
}

// reopenRedactionForLateEdit marks an expired, already redacted entry for
// redaction again when an edit of it arrives: the edit stored its own
// readable archive record, which the next sweep must redact as well.
func reopenRedactionForLateEdit(e *DMEntry, purpose string) bool {
	if !e.Expired || !e.Redacted || purpose != netproto.DMActionEdit {
		return false
	}
	e.Redacted = false
	return true
}

// reopenDisappearingRedaction makes the next sweep reload the pending list
// and redact again, after an archive record was added for an expired message.
func (a *App) reopenDisappearingRedaction() {
	// shortcut: a sweep already running may clear redact when it finishes; the
	// entry stays Redacted=false so the next expiry sweep redacts it again.
	a.dmDisappear.mu.Lock()
	a.dmDisappear.pending = nil
	a.dmDisappear.redact = true
	a.dmDisappear.redactAt = time.Time{}
	a.dmDisappear.mu.Unlock()
}

// markRedactedWith records that the archive copies of done are redacted.
func (a *App) markRedactedWith(store dmHistoryStore, done map[string]bool) {
	if len(done) == 0 {
		return
	}
	a.dmDisappear.mu.Lock()
	for k := range done {
		delete(a.dmDisappear.pending, k)
	}
	a.dmDisappear.mu.Unlock()
	for _, p := range store.peers() {
		a.dmMu.Lock()
		l, err := store.load(p.UniqueID)
		if err == nil {
			changed := false
			for i := range l.Messages {
				e := &l.Messages[i]
				if e.Expired && !e.Redacted && done[ratchetSemanticKey(e.FromUniqueID, e.ClientMsgID)] {
					e.Redacted, changed = true, true
				}
			}
			if changed {
				_ = store.save(l)
			}
		}
		a.dmMu.Unlock()
	}
}

// pendingRedactionsWith lists expired new-protocol messages whose archive
// copy is not redacted yet: semantic key -> archive server ("" = unknown).
// The list is cached until the next expiry, so a server that stays offline
// does not make every sweep decrypt every log.
func (a *App) pendingRedactionsWith(store dmHistoryStore) map[string]string {
	a.dmDisappear.mu.Lock()
	cached := a.dmDisappear.pending
	a.dmDisappear.mu.Unlock()
	if cached != nil {
		out := make(map[string]string, len(cached))
		for k, v := range cached {
			out[k] = v
		}
		return out
	}
	out := map[string]string{}
	for _, p := range store.peers() {
		a.dmMu.Lock()
		l, err := store.load(p.UniqueID)
		a.dmMu.Unlock()
		if err != nil {
			continue
		}
		for _, e := range l.Messages {
			if e.Expired && !e.Redacted && e.Protocol == ratchetProtocol {
				out[ratchetSemanticKey(e.FromUniqueID, e.ClientMsgID)] = e.Archive
			}
		}
	}
	a.dmDisappear.mu.Lock()
	a.dmDisappear.pending = make(map[string]string, len(out))
	for k, v := range out {
		a.dmDisappear.pending[k] = v
	}
	a.dmDisappear.mu.Unlock()
	return out
}

// sweepDisappearing expires due messages and redacts their archive copies
// through every connected session. It runs on a ticker and is cheap when
// nothing is due.
func (a *App) sweepDisappearing(now time.Time) {
	a.dmDisappear.mu.Lock()
	seen := a.dmDisappear.next
	due := seen == 0 || (seen > 0 && seen <= now.Unix())
	redact := a.dmDisappear.redact && now.Sub(a.dmDisappear.redactAt) >= dmRedactRetry
	a.dmDisappear.mu.Unlock()
	if !due && !redact {
		return
	}
	store, err := a.disappearingStore()
	if err != nil {
		return
	}
	a.sweepDisappearingWith(store, now, seen, due, redact)
}

// disappearingStore captures the active identity's DM history for the
// sweeper. Unlike captureDMHistory it never creates an identity: it runs at
// startup, before onboarding may have chosen or created one. Offline, an
// existing identity file is read strictly under identityMu.
func (a *App) disappearingStore() (dmHistoryStore, error) {
	dir, err := a.dmHistoryDir()
	if err != nil {
		return dmHistoryStore{}, err
	}
	var id *identity
	if m := a.cmLoad(); m != nil {
		id, err = m.identity()
	} else {
		a.identityMu.Lock()
		var path string
		if _, path, err = a.resolveActive(); err == nil {
			id, err = loadIdentityAtStrict(path)
		}
		a.identityMu.Unlock()
	}
	if err != nil {
		return dmHistoryStore{}, err
	}
	return dmHistoryStoreForIdentity(dir, id)
}

// sweepDisappearingWith is sweepDisappearing on a captured store.
func (a *App) sweepDisappearingWith(store dmHistoryStore, now time.Time, seen int64, due, redact bool) {
	a.dmDisappear.mu.Lock()
	pendingRedact := a.dmDisappear.redact
	a.dmDisappear.mu.Unlock()
	if due {
		expired, more, next := a.expireDueWith(store, now)
		pendingRedact = pendingRedact || more
		redact = redact || len(expired) > 0
		if len(expired) > 0 {
			a.dmDisappear.mu.Lock()
			a.dmDisappear.pending = nil // reload: new messages to redact
			a.dmDisappear.mu.Unlock()
		}
		for peer, list := range expired {
			a.emitPlain(dmDisappearEvent, map[string]any{"peer": peer, "messages": list})
		}
		a.dmDisappear.mu.Lock()
		switch current := a.dmDisappear.next; {
		case current != seen && current > 0 && (next <= 0 || current < next):
			// An earlier expiry was noted while this sweep ran: keep it.
		case next > 0:
			a.dmDisappear.next = next
		default:
			a.dmDisappear.next = -1 // nothing pending until noteDisappearing
		}
		a.dmDisappear.mu.Unlock()
	}
	if redact && pendingRedact {
		pendingRedact = !a.redactPendingWith(store)
		a.dmDisappear.mu.Lock()
		a.dmDisappear.redactAt = now
		a.dmDisappear.mu.Unlock()
	}
	a.dmDisappear.mu.Lock()
	a.dmDisappear.redact = pendingRedact
	a.dmDisappear.mu.Unlock()
}

// redactPendingWith redacts the archive copies of expired messages through
// the connected sessions and reports whether nothing is left to redact.
func (a *App) redactPendingWith(store dmHistoryStore) bool {
	pending := a.pendingRedactionsWith(store)
	if len(pending) == 0 {
		return true
	}
	a.tabsMu.Lock()
	managers := make([]*connManager, 0, len(a.tabs))
	for _, ts := range a.tabs {
		if ts != nil && ts.cm != nil {
			managers = append(managers, ts.cm)
		}
	}
	a.tabsMu.Unlock()
	done := map[string]bool{}
	blocked := map[string]bool{}
	allSessions := len(managers) > 0
	for _, m := range managers {
		c, err := m.ratchetConsumerOn(nil)
		if err != nil {
			allSessions = false
			continue
		}
		// A message is redacted in the archive of the server it came
		// through; an entry without that binding is tried everywhere.
		server := c.d.Namespace.Server
		targets := map[string]bool{}
		for k, archive := range pending {
			if archive == "" || archive == server {
				targets[k] = true
			}
		}
		if len(targets) == 0 {
			continue
		}
		found, held, err := c.redact(targets)
		if err != nil {
			allSessions = false
			log.Printf("disappearing messages: archive redaction: %v", err)
			continue
		}
		for k := range found {
			done[k] = true
		}
		for k := range held {
			blocked[k] = true
		}
	}
	// A bound message waits for its own server's session, however long.
	// Only an unbound one that no archive holds while every tab has a
	// session counts as having nothing left to redact, after a few scans.
	a.dmDisappear.mu.Lock()
	if a.dmDisappear.misses == nil {
		a.dmDisappear.misses = map[string]int{}
	}
	for k, archive := range pending {
		switch {
		case done[k], blocked[k]:
			delete(a.dmDisappear.misses, k)
		case archive == "" && allSessions:
			a.dmDisappear.misses[k]++
			if a.dmDisappear.misses[k] >= dmRedactGiveUp {
				done[k] = true
				delete(a.dmDisappear.misses, k)
			}
		}
	}
	a.dmDisappear.mu.Unlock()
	a.markRedactedWith(store, done)
	return len(done) == len(pending)
}

// ratchetArchiveServer names the server whose protected archive the current
// session writes ("" without a session).
func (m *connManager) ratchetArchiveServer() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ratchetSess == nil || m.ratchetSess.consumer == nil {
		return ""
	}
	return m.ratchetSess.consumer.d.Namespace.Server
}

// runDisappearingSweeper ticks until stop is closed.
func (a *App) runDisappearingSweeper(stop <-chan struct{}) {
	t := time.NewTicker(dmDisappearSweepInterval)
	defer t.Stop()
	a.sweepDisappearing(time.Now())
	for {
		select {
		case <-stop:
			return
		case now := <-t.C:
			a.sweepDisappearing(now)
		}
	}
}

// redact replaces the archived body of every record of the given messages
// (keyed by ratchetSemanticKey: the message itself and its edits) with the
// expired marker. It returns the keys fully redacted in this archive and the
// keys with a record that cannot be redacted yet (an own send without a
// terminal receipt), which the next sweep retries.
func (c *ratchetConsumer) redact(targets map[string]bool) (found, blocked map[string]bool, err error) {
	c.serial.Lock()
	defer c.serial.Unlock()
	if err := c.check(); err != nil {
		return nil, nil, err
	}
	found, blocked = map[string]bool{}, map[string]bool{}
	for _, kind := range []string{durable.Inbox, durable.Outbox} {
		refs, err := c.refs(kind)
		if err != nil {
			return nil, nil, err
		}
		records, err := c.readMany(refs)
		if err != nil {
			return nil, nil, err
		}
		for ref, r := range records {
			archive, ok := ratchetArchiveOf(kind, r.Data)
			if !ok {
				continue
			}
			var rec RatchetRecord
			if json.Unmarshal(archive, &rec) != nil || rec.Scope != "direct" {
				continue
			}
			key := ratchetSemanticKey(rec.Sender, rec.LogicalID)
			if rec.Purpose == "edit" {
				key = ratchetSemanticKey(rec.Sender, rec.Reference)
			}
			if !targets[key] {
				continue
			}
			if _, _, done := ratchetBodyDisappearing(rec.Body); done {
				found[key] = true
				continue
			}
			redacted, err := ratchetRedactedArchive(archive)
			if err != nil {
				continue
			}
			snap, err := c.read(ref)
			if err != nil {
				return nil, nil, err
			}
			if err = c.d.Store.RedactArchive(snap, ref, redacted); err != nil {
				if errors.Is(err, durable.ErrConflict) {
					blocked[key] = true
					continue
				}
				return nil, nil, err
			}
			found[key] = true
		}
	}
	for k := range blocked {
		delete(found, k)
	}
	return found, blocked, nil
}

// ratchetArchiveOf returns the archive bytes of an inbox or outbox record.
func ratchetArchiveOf(kind string, data []byte) ([]byte, bool) {
	if kind == durable.Inbox {
		var saved struct {
			Archive []byte `json:"archive"`
		}
		if json.Unmarshal(data, &saved) != nil || len(saved.Archive) == 0 {
			return nil, false
		}
		return saved.Archive, true
	}
	var op durable.OutboundRecord
	if json.Unmarshal(data, &op) != nil || len(op.Archive) == 0 {
		return nil, false
	}
	return op.Archive, true
}

// ratchetRedactedArchive keeps every archive field but the body.
func ratchetRedactedArchive(archive []byte) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(archive, &fields); err != nil {
		return nil, err
	}
	fields["body"] = ratchetExpiredBody
	return json.Marshal(fields)
}

// disappearingTTL is the conversation timer for a new direct message to peer.
func (m *connManager) disappearingTTL(peer string) int64 {
	if m == nil || m.dmTTL == nil {
		return 0
	}
	return m.dmTTL(peer)
}
