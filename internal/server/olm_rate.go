package server

// olm_rate.go: budgets and the delivery doorbell for noxa-olm-v1 admission
// (Task 2a reviews A P1-1 and fix review P1-1/P2-1..3).
//
// Budgets (fixed windows of olmBudgetWindow):
//   - a chat (non-control) `begin` is charged to the chat-send bucket, as
//     before; every `finalize` and other admission action is charged to the
//     admission budget, drains (`deliveries`, `receive_ack`) to the drain
//     budget, and a control `begin` to the control budget;
//   - each of those budgets exists per device AND per account (an account
//     with ten devices does not get ten times the budget);
//   - a control `begin` is additionally capped per (sender account, target
//     device), and refused while the sender is invisible. A control can only
//     reach one device, stays pending at most MaxPendingControls per pair and
//     is never shown as a message, so the control budget cannot carry chat.
//
// Doorbell: only a `finalize` that moved the attempt from staging to accepted
// rings, and only the devices whose copies the store inserted in that call.
// Rings are queued through the per-client broadcaster (never written on the
// sender's goroutine) and coalesced per target device to at most one per
// olmDoorbellInterval. The event carries no sender, purpose or count.

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
	olmAccountDrainFrames     = 300
	// Control begins per (sender account, target device) per window.
	olmControlsPerTarget = 12
	keyLookups           = 60 // public key lookups per principal per window
	olmDoorbellInterval  = 250 * time.Millisecond
	eventOlmPending      = "olm_pending"
)

// olmBudgets are the admission limiters of one server.
type olmBudgets struct {
	control, admission, drain                      *chatRateLimiter
	accountControl, accountAdmission, accountDrain *chatRateLimiter
	perTarget                                      *chatRateLimiter
}

func newOlmBudgets() *olmBudgets {
	return &olmBudgets{
		control: newChatRateLimiter(olmControlBegins, olmBudgetWindow), admission: newChatRateLimiter(olmAdmissionFrames, olmBudgetWindow),
		drain: newChatRateLimiter(olmDrainFrames, olmBudgetWindow), accountControl: newChatRateLimiter(olmAccountControlBegins, olmBudgetWindow),
		accountAdmission: newChatRateLimiter(olmAccountAdmissionFrames, olmBudgetWindow), accountDrain: newChatRateLimiter(olmAccountDrainFrames, olmBudgetWindow),
		perTarget: newChatRateLimiter(olmControlsPerTarget, olmBudgetWindow),
	}
}

// both charges the device and the account budget (the account first, so a
// refused account does not consume a device token).
func both(device, account *chatRateLimiter, deviceKey, accountKey string, now time.Time) bool {
	return account.allow(accountKey, now) && device.allow(deviceKey, now)
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
			if s.clientInvisible(client) {
				return false
			}
			return b.perTarget.allow(a.Principal+"\x00"+controlTarget(r.Manifest), now) && both(b.control, b.accountControl, device, a.Principal, now)
		}
		return s.chatRate.allowLimit(a.Principal, now, s.chatActionLimit(ctx, client))
	case "deliveries", "receive_ack":
		return both(b.drain, b.accountDrain, device, a.Principal, now)
	default:
		return both(b.admission, b.accountAdmission, device, a.Principal, now)
	}
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
