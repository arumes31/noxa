package main

import (
	"log"
	"sync"
)

// captureBoost raises the GPU scheduling and CPU priority of the processes
// that capture, encrypt and encode a screen share while one is running: the
// WebView2 process tree in Browser mode, this process for native capture.
// Each holder is a named reason; the boost is restored when the last ends.
type captureBoost struct {
	mu      sync.Mutex
	holders map[string]bool
	apply   func(on bool) error
}

func (b *captureBoost) set(reason string, on bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.holders == nil {
		b.holders = make(map[string]bool)
	}
	if b.apply == nil {
		b.apply = applyCaptureBoost
	}
	was := len(b.holders) > 0
	if on {
		b.holders[reason] = true
	} else {
		delete(b.holders, reason)
	}
	// A refresh while held re-applies: the WebView2 GPU process can restart.
	if now := len(b.holders) > 0; now || was {
		if err := b.apply(now); err != nil {
			log.Printf("capture priority boost: %v", err)
		}
	}
}

var screenCaptureBoost captureBoost
