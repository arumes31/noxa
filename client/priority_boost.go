package main

import (
	"log"
	"sync"
)

// captureBoost raises the GPU scheduling and CPU priority of this process,
// which captures, encodes and encrypts a native screen share, while a display
// capture lease is held. Each holder is a named reason; the boost is restored
// when the last ends.
type captureBoost struct {
	mu      sync.Mutex
	holders map[string]bool
	apply   func(on bool) error
	// logged: failures already written; a refresh every 15 s must not repeat them.
	logged map[string]bool
}

// logOnce writes each distinct boost failure once per process (bounded).
func (b *captureBoost) logOnce(err error) {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, e := range joined.Unwrap() {
			b.logOnce(e)
		}
		return
	}
	msg := err.Error()
	if b.logged[msg] || len(b.logged) >= 32 {
		return
	}
	if b.logged == nil {
		b.logged = make(map[string]bool)
	}
	b.logged[msg] = true
	log.Printf("capture priority boost: %s", msg)
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
	// A refresh while held re-applies (applyCaptureBoost skips boosted processes).
	if now := len(b.holders) > 0; now || was {
		if err := b.apply(now); err != nil {
			b.logOnce(err)
		}
	}
}

var screenCaptureBoost captureBoost
