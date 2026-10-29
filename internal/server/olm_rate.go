package server

// olm_rate.go: budgets and the delivery doorbell for noxa-olm-v1 admission
// (Task 2a reviews A P1-1 and fix review P1-1/P2-1..3).
//
// Budgets (fixed windows of olmBudgetWindow):
//   - a chat (non-control) `begin` is charged to the chat-send bucket, as
//     before; every `finalize` and other admission action is charged to the
//     admission budget, drains (`deliveries`, `receive_ack`) to the drain
//     budget, and a control `begin` to the control budget;
//   - control and admission budgets exist per device AND per account (ten
//     devices do not get ten times the budget) and scale with the sender's
//     encrypted voice channel (one rekey = one control per recipient device,
//     up to MediaE2EEMaxVoice members);
//   - drains are per device only: their volume is driven by other users'
//     sends to every device of the account (a per-account cap starved
//     multi-device recipients);
//   - a control `begin` is additionally capped per (sender account, target
//     device), charged only after the device and account caps passed (the
//     target is client-chosen; limiter maps are swept, and at map capacity
//     this secondary cap is skipped, never refused server-wide);
//   - a bucket's limit grows within its window when the channel grows (the
//     difference is added to the remaining tokens). Invisible senders are
//     refused before any budget with the non-retryable code "invisible". A
//     control can only
//     reach one device, stays pending at most MaxPendingControls per pair and
//     is never shown as a message, so the control budget cannot carry chat.
//
// Doorbell: only a `finalize` that moved the attempt from staging to accepted
// rings, and only the devices whose copies the store inserted in that call.
// Rings are queued through the per-client broadcaster (never written on the
// sender's goroutine) and coalesced per target device to at most one per
// olmDoorbellInterval. The event carries no sender, purpose or count. When a
// slow reader's 16-slot queue is full the ring is dropped; that is harmless:
// the client's fallback poll (5 s) drains the same deliveries.

import (
	"context"
	"sync"
	"time"

	"noxa/internal/e2ee/admission"
	"noxa/internal/netproto"
	"noxa/internal/store"
)

const (
	olmBudgetWindow = 10 * time.Second
	// Per device per window. A 12-member rekey is 11 controls of ~5 frames.
	olmControlBegins   = 48
	olmAdmissionFrames = 120
	olmDrainFrames     = 120
	// Per account per window (2.5x one device).
	olmAccountControlBegins   = 120
	olmAccountAdmissionFrames = 300
	// Control begins per (sender account, target device) per window.
	olmControlsPerTarget = 12
	// MediaE2EEMaxVoice bounds an encrypted voice channel: each member rekeys
	// with one control per other member device, so the cost grows with n^2.
	MediaE2EEMaxVoice   = 64
	keyLookups          = 60 // public key lookups per principal per window
	olmDoorbellInterval = 250 * time.Millisecond
	eventOlmPending     = "olm_pending"
)

// olmBudgets are the admission limiters of one server.
type olmBudgets struct {
	control, admission, drain        *chatRateLimiter
	accountControl, accountAdmission *chatRateLimiter
	perTarget                        *chatRateLimiter
}

func newOlmBudgets() *olmBudgets {
	return &olmBudgets{
		control: newChatRateLimiter(olmControlBegins, olmBudgetWindow), admission: newChatRateLimiter(olmAdmissionFrames, olmBudgetWindow),
		drain: newChatRateLimiter(olmDrainFrames, olmBudgetWindow), accountControl: newChatRateLimiter(olmAccountControlBegins, olmBudgetWindow),
		accountAdmission: newChatRateLimiter(olmAccountAdmissionFrames, olmBudgetWindow),
		perTarget:        newChatRateLimiter(olmControlsPerTarget, olmBudgetWindow),
	}
}

// both charges the device and the account budget (the account first, so a
// refused account does not consume a device token).
func both(device, account *chatRateLimiter, deviceKey, accountKey string, now time.Time, deviceLimit, accountLimit int) bool {
	// The device first: a device beyond its own cap never consumes the
	// account (its siblings, e.g. the voice device, keep their share).
	if !device.allowLimit(deviceKey, now, deviceLimit) {
		return false
	}
	if !account.allowLimit(accountKey, now, accountLimit) {
		device.refund(deviceKey, now)
		return false
	}
	return true
}

// controlTarget is the single non-local target of a control manifest.
func controlTarget(m *admission.Manifest) string {
	for _, t := range m.Targets {
		if !t.Local {
			return t.Principal + "\x00" + t.Device
		}
	}
	return ""
}

// allowOlmAdmission charges one admission request to its budgets.
func (s *TCPServer) allowOlmAdmission(ctx context.Context, client *Client, a store.OlmSource, r netproto.OlmAdmissionRequest) bool {
	if s.chatRate == nil || s.olmBudget == nil {
		return true
	}
	b := s.olmBudget
	now := time.Now()
	device := a.Principal + "\x00" + a.Device
	switch r.Action {
	case "begin":
		if r.Manifest != nil && r.Manifest.Purpose == "control" {
			// Recipients are counted only for the budgets that scale with them.
			controls := max(olmControlBegins, 2*s.voiceRecipients(client))
			if !both(b.control, b.accountControl, device, a.Principal, now, controls, controls*5/2) {
				return false
			}
			// The client-chosen target is charged only after the caps passed.
			// It is a secondary cap behind the bounded account caps: at map
			// capacity it is skipped, never refused server-wide.
			if !b.perTarget.allowOrSkip(a.Principal+"\x00"+controlTarget(r.Manifest), now) {
				b.control.refund(device, now)
				b.accountControl.refund(a.Principal, now)
				return false
			}
			return true
		}
		return s.chatRate.allowLimit(a.Principal, now, s.chatActionLimit(ctx, client))
	case "deliveries", "receive_ack":
		return b.drain.allow(device, now)
	default:
		frames := max(olmAdmissionFrames, 6*s.voiceRecipients(client))
		return both(b.admission, b.accountAdmission, device, a.Principal, now, frames, frames*5/2)
	}
}

// voiceRecipients is the number of other encrypted voice seats in the
// client's channel (the recipients of one rekey), bounded by
// MediaE2EEMaxVoice. It counts seats without copying the roster.
func (s *TCPServer) voiceRecipients(client *Client) int {
	if s.deps == nil || s.deps.State == nil || client == nil {
		return 0
	}
	ch, _, ok := s.deps.State.ClientChannelState(client.ID)
	if !ok || ch <= 0 {
		return 0
	}
	return min(s.mediaSeats.others(ch, client.ID), MediaE2EEMaxVoice)
}

func (s *TCPServer) clientInvisible(client *Client) bool {
	if s.deps == nil || s.deps.State == nil {
		return false
	}
	c, ok := s.deps.State.GetClient(client.ID)
	return ok && c.Status == "invisible"
}

// olmDoorbells coalesces rings per target device.
type olmDoorbells struct {
	mu      sync.Mutex
	last    map[string]time.Time
	pending map[string]bool
}

// ringOlmDoorbells queues one doorbell per device that received a copy.
func (s *TCPServer) ringOlmDoorbells(delivered []store.OlmDeliveredDevice) {
	for _, d := range delivered {
		s.ringOlmDevice(d.Principal + "\x00" + d.Device)
	}
}

func (s *TCPServer) ringOlmDevice(key string) {
	db := &s.olmDoorbell
	db.mu.Lock()
	if db.last == nil {
		db.last, db.pending = map[string]time.Time{}, map[string]bool{}
	}
	now := time.Now()
	if wait := olmDoorbellInterval - now.Sub(db.last[key]); wait > 0 {
		if !db.pending[key] {
			db.pending[key] = true
			time.AfterFunc(wait, func() {
				db.mu.Lock()
				delete(db.pending, key)
				db.last[key] = time.Now()
				db.mu.Unlock()
				s.deliverOlmDoorbell(key)
			})
		}
		db.mu.Unlock()
		return
	}
	db.last[key] = now
	// Bounded: entries older than the interval carry no information.
	if len(db.last) > 8192 {
		for k, t := range db.last {
			if now.Sub(t) > olmDoorbellInterval && !db.pending[k] {
				delete(db.last, k)
			}
		}
	}
	db.mu.Unlock()
	s.deliverOlmDoorbell(key)
}

// deliverOlmDoorbell queues the event on every live session of the device
// through the broadcaster (non-blocking per client).
func (s *TCPServer) deliverOlmDoorbell(key string) {
	if s.deps == nil || s.deps.Broadcast == nil {
		return
	}
	payload, err := eventEnvelope(eventOlmPending, struct{}{})
	if err != nil {
		return
	}
	s.mu.RLock()
	var ids []string
	for id, c := range s.clients {
		if c.isAuthed() && c.uniqueID()+"\x00"+c.currentDeviceID() == key {
			ids = append(ids, id)
		}
	}
	s.mu.RUnlock()
	for _, id := range ids {
		_ = s.deps.Broadcast.BroadcastToClient(id, payload)
	}
}
