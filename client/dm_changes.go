// dm_changes.go edits and deletes direct messages (dm_devices_v1). A change
// travels like a message: one sealed copy per trusted device of both accounts,
// marked with DMAction and the original's ClientMsgID. Receivers apply it to
// their local log only when the original is there and came from the same
// sender; a change is never shown as a new message.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"noxa/internal/netproto"
)

// errDMChangeUnsupported means the change cannot travel as per-device copies:
// the server or the contact runs a noXa without them.
var errDMChangeUnsupported = errors.New("editing needs an updated noXa on both sides")

// errDMChangeNotDelivered refuses a change to an own new-protocol message
// whose delivery is still pending or failed.
var errDMChangeNotDelivered = errors.New("this message is not delivered yet; it can be changed once it is sent")

// errDMChangeNotOwn refuses a change to a message this account did not send.
var errDMChangeNotOwn = errors.New("only your own messages can be edited or deleted")

// dmPendingTTL and dmPendingMax bound changes kept for an original that has
// not arrived yet. A spool replays a device's messages in order, so the gap
// is short; a change still waiting after the TTL is dropped.
const (
	dmPendingTTL = 10 * time.Minute
	dmPendingMax = 256
)

// dmChange is one received edit or deletion.
type dmChange struct {
	Action string
	Ref    string
	From   string
	Body   string
	At     int64 // unix seconds
}

type dmPendingChange struct {
	change  dmChange
	expires time.Time
}

// dmPendingChanges holds changes whose original is not in the log yet, keyed
// by log file and the original's ClientMsgID. The App guards it with dmMu.
type dmPendingChanges struct {
	entries map[string][]dmPendingChange
	count   int
}

func dmPendingKey(path, ref string) string { return path + "\x00" + ref }

func (p *dmPendingChanges) add(path string, c dmChange, now time.Time) {
	if p.entries == nil {
		p.entries = make(map[string][]dmPendingChange)
	}
	if p.count >= dmPendingMax {
		p.prune(now)
		if p.count >= dmPendingMax {
			clear(p.entries)
			p.count = 0
		}
	}
	key := dmPendingKey(path, c.Ref)
	p.entries[key] = append(p.entries[key], dmPendingChange{change: c, expires: now.Add(dmPendingTTL)})
	p.count++
}

// take removes and returns the unexpired changes waiting for ref, oldest first.
func (p *dmPendingChanges) take(path, ref string, now time.Time) []dmChange {
	if ref == "" || len(p.entries) == 0 {
		return nil
	}
	key := dmPendingKey(path, ref)
	waiting := p.entries[key]
	delete(p.entries, key)
	p.count -= len(waiting)
	out := make([]dmChange, 0, len(waiting))
	for _, w := range waiting {
		if w.expires.After(now) {
			out = append(out, w.change)
		}
	}
	return out
}

func (p *dmPendingChanges) prune(now time.Time) {
	for key, waiting := range p.entries {
		kept := waiting[:0]
		for _, w := range waiting {
			if w.expires.After(now) {
				kept = append(kept, w)
			}
		}
		p.count -= len(waiting) - len(kept)
		if len(kept) == 0 {
			delete(p.entries, key)
		} else {
			p.entries[key] = kept
		}
	}
}

// applyDMChange changes one stored entry whose sender was already checked. A
// deletion is final: a later edit does not bring the message back.
func applyDMChange(e *DMEntry, c dmChange) {
	if e.Deleted {
		return
	}
	switch c.Action {
	case netproto.DMActionEdit:
		e.Body, e.Edited, e.EditedAt = c.Body, true, c.At
		// The new body was opened from a verified device copy.
		e.EncVerified, e.EncMode = true, "e2e"
	case netproto.DMActionDelete:
		e.Body, e.Deleted = "", true
	}
}

// dmHistoryApplyChangeWith applies a change to peer's log. It reports whether
// the log changed; a change whose original is missing waits in dmPending, and
// one naming another sender's message is dropped.
func (a *App) dmHistoryApplyChangeWith(store dmHistoryStore, peer string, c dmChange) (bool, error) {
	if peer == "" || c.Ref == "" || c.From == "" {
		return false, errors.New("invalid direct-message change")
	}
	a.dmMu.Lock()
	defer a.dmMu.Unlock()
	l, err := store.load(peer)
	if err != nil {
		return false, err
	}
	known := false
	for i := range l.Messages {
		e := &l.Messages[i]
		// A legacy change never touches a new-protocol record: those are
		// changed only by an authenticated noxa-olm-v1 edit/delete.
		if e.ClientMsgID != c.Ref || e.Protocol != "" {
			continue
		}
		known = true
		if e.FromUniqueID != c.From {
			continue
		}
		applyDMChange(e, c)
		l.Peer = peer
		return true, store.save(l)
	}
	if known {
		log.Printf("dm change: ignoring a %s of another sender's message", c.Action)
		return false, nil
	}
	a.dmPending.add(store.path(peer), c, time.Now())
	return false, nil
}

// recordDMChange persists a received change in the log of the identity that
// opened it and returns the conversation it applies to.
func (a *App) recordDMChange(m *connManager, peer string, c dmChange) string {
	dir, err := a.dmHistoryDir()
	if err != nil {
		return peer
	}
	id, err := m.identity()
	if err != nil {
		return peer
	}
	store, err := dmHistoryStoreForIdentity(dir, id)
	if err != nil {
		return peer
	}
	if peer == c.From {
		// A change sent from this account's other device and replayed from
		// the spool names this account as recipient, not the contact: find
		// the conversation holding the original instead.
		peer = store.peerOf(c.Ref, c.From, peer)
	}
	if _, err := a.dmHistoryApplyChangeWith(store, peer, c); err != nil {
		log.Printf("dm change: %v", err)
	}
	return peer
}

// peerOf returns the conversation whose log holds ref sent by from, or
// fallback when no log does.
func (s dmHistoryStore) peerOf(ref, from, fallback string) string {
	for _, p := range s.peers() {
		entries, err := s.messages(p.UniqueID)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.ClientMsgID == ref && e.FromUniqueID == from {
				return p.UniqueID
			}
		}
	}
	return fallback
}

// EditDirectMessageForTab replaces the text of one of this account's direct
// messages to peerUID on every device of both accounts.
func (a *App) EditDirectMessageForTab(tabID, peerUID, clientMsgID, newText string) error {
	if strings.TrimSpace(newText) == "" {
		return errors.New("empty message")
	}
	return a.changeDirectMessage(tabID, peerUID, clientMsgID, netproto.DMActionEdit, newText)
}

// DeleteDirectMessageForTab removes one of this account's direct messages to
// peerUID on every device of both accounts.
func (a *App) DeleteDirectMessageForTab(tabID, peerUID, clientMsgID string) error {
	return a.changeDirectMessage(tabID, peerUID, clientMsgID, netproto.DMActionDelete, "")
}

func (a *App) changeDirectMessage(tabID, peer, ref, action, text string) error {
	if peer == "" || !netproto.ValidDMAction(action, ref) {
		return errors.New("invalid direct-message change")
	}
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return err
	}
	protocol, err := a.ownDirectMessageProtocol(m, peer, ref)
	if err != nil {
		return err
	}
	if protocol == ratchetProtocol {
		// Authenticated new-protocol change: the reference is this sender's
		// logical ID, never a transport UUID.
		return m.changeRatchetDirect(peer, ref, action, text)
	}
	if m.ratchetConfigured() {
		// Legacy NaCl is read-only: never sent again, not even as a change.
		return errLegacyReadOnly
	}
	if !m.dmDevicesActive() {
		return errDMChangeUnsupported
	}
	msg, err := m.encryptDirectChange(peer, ref, action, text)
	if err != nil {
		return err
	}
	return m.sendChatWithKeyRetry(msg, text)
}

// requireOwnDirectMessage checks the local log: only a message this account
// sent, and that is not deleted, may be changed.
func (a *App) requireOwnDirectMessage(m *connManager, peer, ref string) error {
	_, err := a.ownDirectMessageProtocol(m, peer, ref)
	return err
}

// ownDirectMessageProtocol returns the protocol of this account's own,
// undeleted message ref in peer's log ("" = legacy). A new-protocol entry is
// preferred: its reference is sender-qualified and cannot collide.
func (a *App) ownDirectMessageProtocol(m *connManager, peer, ref string) (string, error) {
	dir, err := a.dmHistoryDir()
	if err != nil {
		return "", err
	}
	id, err := m.identity()
	if err != nil {
		return "", err
	}
	store, err := dmHistoryStoreForIdentity(dir, id)
	if err != nil {
		return "", err
	}
	entries, err := store.messages(peer)
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	own := m.uniqueID
	m.mu.Unlock()
	found, protocol := false, ""
	for _, e := range entries {
		if e.ClientMsgID == ref && e.Self && !e.Deleted && (e.FromUniqueID == own || e.FromUniqueID == "" && e.Protocol == "") {
			if !found || e.Protocol == ratchetProtocol {
				found, protocol = true, e.Protocol
			}
		}
	}
	if !found {
		return "", errDMChangeNotOwn
	}
	if protocol == ratchetProtocol {
		// As the PWA: only a delivered own message can be changed; a pending
		// or failed one may never reach the contact.
		for _, e := range entries {
			if e.Protocol == ratchetProtocol && e.ClientMsgID == ref && e.FromUniqueID == own && e.Status != "" && e.Status != "accepted" {
				return "", errDMChangeNotDelivered
			}
		}
	}
	return protocol, nil
}

// encryptDirectChange seals a change exactly like a per-device message: one
// copy per trusted device of the contact and of this account, this device
// included so the sender's echo returns. There is no single-copy fallback.
func (m *connManager) encryptDirectChange(uid, ref, action, text string) (netproto.ChatSend, error) {
	if !m.dmDevicesActive() {
		return netproto.ChatSend{}, errDMChangeUnsupported
	}
	id, err := m.identity()
	if err != nil {
		return netproto.ChatSend{}, err
	}
	pub, priv, err := id.x25519()
	if err != nil {
		return netproto.ChatSend{}, err
	}
	switch _, lookup := m.lookupPeerPubKey(uid); lookup {
	case peerKeyFound:
	case peerKeyAbsent:
		return netproto.ChatSend{}, errPeerNoE2EKey
	default:
		return netproto.ChatSend{}, fmt.Errorf("could not fetch the end-to-end key for %s; try again", uid)
	}
	send, ok, err := m.encryptDirectCopies(uid, text, pub, priv)
	if err != nil {
		return netproto.ChatSend{}, fmt.Errorf("could not verify the devices of %s; try again: %w", uid, err)
	}
	if !ok {
		return netproto.ChatSend{}, errDMChangeUnsupported
	}
	send.DMAction, send.RefClientMsgID = action, ref
	return send, nil
}

// dmChangedEvent is the "dm_changed" event: the open conversation updates the
// referenced message in place.
type dmChangedEvent struct {
	Peer           string `json:"peer"`
	Action         string `json:"action"`
	RefClientMsgID string `json:"ref_client_msg_id"`
	FromUniqueID   string `json:"from_unique_id"`
	Body           string `json:"body,omitempty"`
	EditedAt       int64  `json:"edited_at,omitempty"`
}

// receiveDMChange handles an opened change copy: it is persisted (when this
// manager belongs to an App) and announced to the frontend. A change whose
// copy could not be opened never reaches here.
func (m *connManager) receiveDMChange(chat netproto.ChatBroadcast, plain string) {
	m.mu.Lock()
	own := m.uniqueID
	m.mu.Unlock()
	peer := chat.FromUniqueID
	if peer == own {
		peer = chat.ToUniqueID // a change sent from this account's other device
	}
	if peer == "" || chat.FromUniqueID == "" || !netproto.ValidDMAction(chat.DMAction, chat.RefClientMsgID) || chat.DMAction == "" {
		return
	}
	c := dmChange{Action: chat.DMAction, Ref: chat.RefClientMsgID, From: chat.FromUniqueID, At: time.Now().Unix()}
	if c.Action == netproto.DMActionEdit {
		if strings.TrimSpace(plain) == "" {
			return
		}
		c.Body = plain
	}
	if hook := m.dmChanges; hook != nil {
		peer = hook(m, peer, c)
	}
	data, err := json.Marshal(dmChangedEvent{Peer: peer, Action: c.Action, RefClientMsgID: c.Ref, FromUniqueID: c.From, Body: c.Body, EditedAt: c.At})
	if err != nil {
		return
	}
	env, err := json.Marshal(map[string]any{"type": "dm_changed", "data": json.RawMessage(data)})
	if err != nil {
		return
	}
	m.emit("event", string(env))
}
