package server

// olm_rate.go: budgets and the delivery doorbell for noxa-olm-v1 admission
// (Task 2a review A P1-1).
//
// Before this, every admission action shared the per-principal chat bucket
// (default 5 per 3 s) or one ":olm-admission" bucket of the same size, and
// clients polled `deliveries` every second during voice. A channel rekey (one
// control per recipient device, about five admission frames each) then took
// tens of seconds and starved the user's chat sends.
//
// Now:
//   - only a chat (non-control) `begin` is charged to the chat-send bucket;
//     a control `begin` uses its own per-device control budget, and a
//     `finalize` follows the purpose of its own `begin`;
//   - every other action uses a per-device admission budget, and the drain
//     actions (`deliveries`, `receive_ack`) a separate per-device drain budget;
//   - after a successful `finalize` the server rings a content-free
//     "olm_pending" doorbell on every live session of each target device, so
//     clients drain when there is something to drain (their slow 5 s poll is
//     only a fallback). The doorbell carries no sender, purpose or count.

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"noxa/internal/e2ee/admission"
	"noxa/internal/netproto"
	"noxa/internal/store"
)

// Budgets (per device, fixed windows). A 12-member channel rekey is 11
// controls; at ~5 admission frames each it fits the admission budget twice.
const (
	olmControlBegins   = 96  // control begins per window
	olmAdmissionFrames = 240 // other admission actions per window
	olmDrainFrames     = 120 // deliveries + receive_ack per window
	keyLookups         = 60  // public key lookups per principal per window
	olmBudgetWindow    = 10 * time.Second
	olmAttemptMemory   = 4096
	olmAttemptTTL      = 10 * time.Minute
	eventOlmPending    = "olm_pending"
)

type olmAttemptInfo struct {
	control bool
	targets []admission.Target
	at      time.Time
}

// olmAttemptMemo remembers, for a short time, the purpose and targets of
// attempts this server admitted a `begin` for (bounded, in memory only).
type olmAttemptMemo struct {
	mu    sync.Mutex
	items map[string]olmAttemptInfo
}

func olmAttemptKey(principal, device, attempt string) string {
	return principal + "\x00" + device + "\x00" + attempt
}

func (m *olmAttemptMemo) note(principal, device string, manifest admission.Manifest) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	if m.items == nil {
		m.items = map[string]olmAttemptInfo{}
	}
	if len(m.items) >= olmAttemptMemory {
		for k, v := range m.items {
			if now.Sub(v.at) > olmAttemptTTL || len(m.items) >= olmAttemptMemory {
				delete(m.items, k)
			}
		}
	}
	m.items[olmAttemptKey(principal, device, manifest.AttemptID)] = olmAttemptInfo{control: manifest.Purpose == "control", targets: manifest.Targets, at: now}
}

func (m *olmAttemptMemo) get(principal, device, attempt string) (olmAttemptInfo, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.items[olmAttemptKey(principal, device, attempt)]
	if ok && time.Since(v.at) > olmAttemptTTL {
		delete(m.items, olmAttemptKey(principal, device, attempt))
		return olmAttemptInfo{}, false
	}
	return v, ok
}

// allowOlmAdmission charges one admission request to its budget.
func (s *TCPServer) allowOlmAdmission(ctx context.Context, client *Client, a store.OlmSource, r netproto.OlmAdmissionRequest) bool {
	if s.chatRate == nil {
		return true
	}
	now := time.Now()
	device := a.Principal + "\x00" + a.Device
	switch r.Action {
	case "begin":
		if r.Manifest != nil && r.Manifest.Purpose == "control" {
			return allowOrUnset(s.olmControlRate, device, now)
		}
		return s.chatRate.allowLimit(a.Principal, now, s.chatActionLimit(ctx, client))
	case "finalize":
		// A finalize is part of its begin's operation: chat begins were
		// already charged to the chat bucket.
		return allowOrUnset(s.olmAdmissionRate, device, now)
	case "deliveries", "receive_ack":
		return allowOrUnset(s.olmDrainRate, device, now)
	default:
		return allowOrUnset(s.olmAdmissionRate, device, now)
	}
}

// ringOlmDoorbells tells every live session of each target device of a
// finalized attempt that it has deliveries pending.
func (s *TCPServer) ringOlmDoorbells(principal, device, attempt string) {
	info, ok := s.olmAttempts.get(principal, device, attempt)
	if !ok {
		return
	}
	payload, err := eventEnvelope(eventOlmPending, struct{}{})
	if err != nil {
		return
	}
	targets := map[string]bool{}
	for _, t := range info.targets {
		if !t.Local {
			targets[t.Principal+"\x00"+t.Device] = true
		}
	}
	s.mu.RLock()
	var ring []*Client
	for _, c := range s.clients {
		if c.isAuthed() && targets[c.uniqueID()+"\x00"+c.currentDeviceID()] {
			ring = append(ring, c)
		}
	}
	s.mu.RUnlock()
	for _, c := range ring {
		_ = s.writeMessage(c, netproto.MsgEvent, json.RawMessage(payload))
	}
}

func allowOrUnset(l *chatRateLimiter, key string, now time.Time) bool {
	return l == nil || l.allow(key, now)
}
