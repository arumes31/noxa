package server

import (
	"noxa/internal/netproto"
	"sync"
	"time"
)

// Fixed per-session buckets bound memory and isolate authenticated clients.
// ICE has its own generous burst; heartbeat never competes with database work.
type requestBucket struct {
	tokens  float64
	updated time.Time
}
type sessionRequestBudget struct {
	mu      sync.Mutex
	buckets [10]requestBucket
}

func (b *sessionRequestBudget) allow(mt netproto.MessageType, now time.Time) bool {
	if mt == netproto.MsgPing {
		return true
	}
	class, burst, rate := 0, 120.0, 60.0
	switch mt {
	case netproto.MsgLinkPreviewRequest, netproto.MsgOlmDirectoryRequest:
		class, burst, rate = 3, 12, 2
	case netproto.MsgOlmAdmissionRequest:
		// Admission has its own per-device budgets (olm_rate.go); this bound
		// only caps one session's database work. A 64-member rekey is ~315 frames.
		class, burst, rate = 4, 400, 16
	case netproto.MsgChatHistory, netproto.MsgFileList, netproto.MsgFileTransferInit, netproto.MsgJoinChannel:
		class, burst, rate = 1, 24, 4
	case netproto.MsgICECandidate, netproto.MsgWebRTCAnswer:
		class, burst, rate = 2, 256, 64
	case netproto.MsgWebRTCOffer:
		// Each offer rebuilds the session's transceivers. Clients send one at a
		// time (each waits for its answer) on source changes, ICE restarts and
		// up to two glare retries; member joins arrive as server offers.
		class, burst, rate = 6, 64, 8
	case netproto.MsgSpokenNameGet:
		// One per member entering the channel; cached clips are cheap.
		class, burst, rate = 7, 40, 1
	case netproto.MsgAudioStateSet:
		// A real change rebuilds a snapshot for every recipient; repeats are
		// only acknowledged. Loose enough for rapid mute toggling.
		class, burst, rate = 9, 20, 4
	case netproto.MsgSetStatus, netproto.MsgDisplayNameSet:
		// Each change rebuilds a snapshot for every recipient.
		class, burst, rate = 5, 5, 0.5
	}
	return b.take(class, burst, rate, now)
}

func (b *sessionRequestBudget) take(class int, burst, rate float64, now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	v := &b.buckets[class]
	if v.updated.IsZero() {
		v.tokens = burst
	} else {
		v.tokens = min(burst, v.tokens+max(0, now.Sub(v.updated).Seconds())*rate)
	}
	v.updated = now
	if v.tokens < 1 {
		return false
	}
	v.tokens--
	return true
}
