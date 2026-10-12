package main

import (
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"
)

const displayCaptureLeaseTTL = 120 * time.Second
const maxDisplayCaptureLeases = 16

// Windows request types; no away-mode or execution-required request is made.
// https://learn.microsoft.com/en-us/windows/win32/api/winbase/nf-winbase-powersetrequest
const (
	displayCaptureDisplayRequired uint32 = 0
	displayCaptureSystemRequired  uint32 = 1
)

type displayCapturePowerBackend interface {
	create() (uintptr, error)
	set(uintptr, uint32) error
	clear(uintptr, uint32) error
	close(uintptr) error
	state() (uint64, bool, error)
	shutdown() error
}

// Each live capture owns a lease, independent of server publication or viewers.
// Expiration bounds retention if the renderer dies without delivering cleanup.
type displayCapturePower struct {
	mu         sync.Mutex
	backend    displayCapturePowerBackend
	now        func() time.Time
	schedule   func(time.Duration, func()) func()
	leases     map[string]time.Time
	handle     uintptr
	closed     bool
	timer      func()
	epoch      uint64
	powerEpoch uint64
	powerSeen  bool
}

func (p *displayCapturePower) initLocked() {
	if p.backend == nil {
		p.backend = newDisplayCapturePowerBackend()
	}
	if p.now == nil {
		p.now = time.Now
	}
	if p.schedule == nil {
		p.schedule = func(delay time.Duration, callback func()) func() {
			timer := time.AfterFunc(delay, callback)
			return func() { timer.Stop() }
		}
	}
	if p.leases == nil {
		p.leases = make(map[string]time.Time)
	}
}

func (p *displayCapturePower) closeHandleLocked() error {
	handle := p.handle
	p.handle = 0
	if handle == 0 {
		return nil
	}
	// Always close even if a request was terminated by explicit user sleep.
	return errors.Join(p.backend.clear(handle, displayCaptureDisplayRequired),
		p.backend.clear(handle, displayCaptureSystemRequired), p.backend.close(handle))
}

func (p *displayCapturePower) expireLocked(now time.Time) error {
	for id, deadline := range p.leases {
		if !now.Before(deadline) {
			delete(p.leases, id)
		}
	}
	if len(p.leases) == 0 {
		return p.closeHandleLocked()
	}
	return nil
}

func (p *displayCapturePower) armLocked() {
	p.epoch++
	epoch := p.epoch
	if p.timer != nil {
		p.timer()
		p.timer = nil
	}
	if p.closed || len(p.leases) == 0 {
		return
	}
	var first time.Time
	for _, deadline := range p.leases {
		if first.IsZero() || deadline.Before(first) {
			first = deadline
		}
	}
	delay := first.Sub(p.now())
	if delay < 0 {
		delay = 0
	}
	p.timer = p.schedule(delay, func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.closed || epoch != p.epoch {
			return
		}
		if err := p.expireLocked(p.now()); err != nil {
			log.Printf("display capture power cleanup: %v", err)
		}
		p.armLocked()
	})
}

// A native power transition invalidates all pre-sleep leases. Automatic resume
// alone does not authorize turning the display on; wait for interactive resume.
func (p *displayCapturePower) synchronizePowerLocked() error {
	epoch, blocked, err := p.backend.state()
	if err != nil {
		clear(p.leases)
		return errors.Join(err, p.closeHandleLocked())
	}
	if p.powerSeen && epoch != p.powerEpoch {
		clear(p.leases)
		err = p.closeHandleLocked()
	}
	p.powerEpoch, p.powerSeen = epoch, true
	if blocked {
		return errors.Join(errors.New("display capture power is suspended until interactive resume"), err)
	}
	return err
}
func (p *displayCapturePower) acquire() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return "", errors.New("display capture power manager is closed")
	}
	p.initLocked()
	defer p.armLocked()
	if err := p.expireLocked(p.now()); err != nil {
		return "", err
	}
	if err := p.synchronizePowerLocked(); err != nil {
		return "", err
	}
	if len(p.leases) >= maxDisplayCaptureLeases {
		return "", errors.New("too many display capture leases")
	}
	if p.handle == 0 {
		handle, err := p.backend.create()
		if err != nil {
			return "", err
		}
		if handle == 0 {
			return "", errors.New("display capture power backend returned an empty handle")
		}
		if err := p.backend.set(handle, displayCaptureSystemRequired); err != nil {
			return "", errors.Join(err, p.backend.close(handle))
		}
		if err := p.backend.set(handle, displayCaptureDisplayRequired); err != nil {
			return "", errors.Join(err, p.backend.clear(handle, displayCaptureSystemRequired), p.backend.close(handle))
		}
		p.handle = handle
	}
	// Native calls can overlap a power callback. Never return a lease for a
	// request acquired across that transition, even if resume was very short.
	acquiredEpoch := p.powerEpoch
	if err := p.synchronizePowerLocked(); err != nil {
		return "", err
	}
	if p.powerEpoch != acquiredEpoch || p.handle == 0 {
		return "", errors.New("power changed while acquiring display capture lease")
	}
	id := rand.Text()
	p.leases[id] = p.now().Add(displayCaptureLeaseTTL)
	return id, nil
}

func (p *displayCapturePower) refresh(id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return errors.New("display capture power manager is closed")
	}
	p.initLocked()
	err := p.expireLocked(p.now())
	if len(p.leases) > 0 {
		err = errors.Join(err, p.synchronizePowerLocked())
	}
	if _, ok := p.leases[id]; !ok {
		p.armLocked()
		return errors.Join(errors.New("display capture lease is missing or expired"), err)
	}
	p.leases[id] = p.now().Add(displayCaptureLeaseTTL)
	p.armLocked()
	return err
}

func (p *displayCapturePower) release(id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.initLocked()
	delete(p.leases, id)
	err := p.expireLocked(p.now())
	p.armLocked()
	return err
}

func (p *displayCapturePower) shutdown() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	p.epoch++
	if p.timer != nil {
		p.timer()
		p.timer = nil
	}
	clear(p.leases)
	err := p.closeHandleLocked()
	if p.backend != nil {
		err = errors.Join(err, p.backend.shutdown())
	}
	return err
}

// AcquireDisplayCaptureLease keeps an actively captured display and system awake.
// Failure does not grant a lease; capture can continue without this protection.
func (a *App) AcquireDisplayCaptureLease() (string, error) {
	if a.UpdateStartupPending() {
		return "", errors.New(updateStartupPendingMessage)
	}
	id, err := a.displayCapturePower.acquire()
	if err != nil {
		return "", fmt.Errorf("keep display capture awake: %w", err)
	}
	return id, nil
}

// RefreshDisplayCaptureLease renews an existing lease, never an expired one.
func (a *App) RefreshDisplayCaptureLease(id string) error {
	return a.displayCapturePower.refresh(id)
}

// ReleaseDisplayCaptureLease releases exactly this capture's lease, idempotently.
func (a *App) ReleaseDisplayCaptureLease(id string) error {
	return a.displayCapturePower.release(id)
}
