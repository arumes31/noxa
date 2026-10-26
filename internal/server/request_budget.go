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
	buckets [4]requestBucket
}

func (b *sessionRequestBudget) allow(mt netproto.MessageType, now time.Time) bool {
	if mt == netproto.MsgPing {
		return true
	}
	class, burst, rate := 0, 120.0, 60.0
	switch mt {
	case netproto.MsgLinkPreviewRequest, netproto.MsgOlmDirectoryRequest:
		class, burst, rate = 3, 12, 2
	case netproto.MsgChatHistory, netproto.MsgFileList, netproto.MsgFileTransferInit, netproto.MsgJoinChannel:
		class, burst, rate = 1, 24, 4
	case netproto.MsgICECandidate, netproto.MsgWebRTCOffer, netproto.MsgWebRTCAnswer:
		class, burst, rate = 2, 256, 64
	}
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
