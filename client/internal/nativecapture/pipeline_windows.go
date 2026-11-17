//go:build windows

package nativecapture

import (
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/windows"
)

const nv12Ring = 4

var procSetThreadPriority = windows.NewLazySystemDLL("kernel32.dll").NewProc("SetThreadPriority")

// Session is one running capture → convert → encode pipeline. Its OS thread
// owns all D3D11/WGC/MF calls except the blocking MFT event wait.
type Session struct {
	cfg       Config
	out       func(Frame)
	done      chan struct{}
	exited    chan struct{}
	fatal     chan error
	closeOnce sync.Once

	keyframe atomic.Bool
	paused   atomic.Bool
	fps      atomic.Int32
	bitrate  atomic.Int32
	resize   atomic.Pointer[[2]int]

	mu    sync.Mutex
	stats Stats
	info  EncoderInfo
}

// Start opens the capture and a hardware encoder and begins delivering frames
// to out (from the pipeline thread; out must not block for long).
func Start(cfg Config, out func(Frame)) (*Session, error) {
	if cfg.FPS <= 0 {
		cfg.FPS = 30
	}
	s := &Session{cfg: cfg, out: out, done: make(chan struct{}), exited: make(chan struct{}), fatal: make(chan error, 1)}
	s.fps.Store(int32(cfg.FPS))
	s.bitrate.Store(int32(cfg.Bitrate))
	ready := make(chan error, 1)
	go s.run(ready)
	if err := <-ready; err != nil {
		<-s.exited
		return nil, err
	}
	return s, nil
}

func (s *Session) RequestKeyframe()   { s.keyframe.Store(true) }
func (s *Session) SetPaused(p bool)   { s.paused.Store(p); s.keyframe.Store(true) }
func (s *Session) SetFPS(fps int)     { s.fps.Store(int32(max(1, min(fps, 60)))) }
func (s *Session) SetBitrate(bps int) { s.bitrate.Store(int32(bps)) }

// SetMaxSize changes the output bound; the encoder is reopened.
func (s *Session) SetMaxSize(w, h int) { s.resize.Store(&[2]int{w, h}) }

// Fatal reports a pipeline failure (window closed, device lost, encoder error).
func (s *Session) Fatal() <-chan error { return s.fatal }

func (s *Session) Info() EncoderInfo { s.mu.Lock(); defer s.mu.Unlock(); return s.info }
func (s *Session) Stats() Stats      { s.mu.Lock(); defer s.mu.Unlock(); return s.stats }

func (s *Session) Close() {
	s.closeOnce.Do(func() { close(s.done) })
	<-s.exited
}

func (s *Session) fail(err error) {
	select {
	case s.fatal <- err:
	default:
	}
}

type ringSlot struct {
	tex, view uintptr
	queued    time.Time
}

// pipeline holds the GPU objects that depend on the output size.
type pipeline struct {
	d          *device
	conv       *converter
	enc        *encoder
	ring       [nv12Ring]ringSlot
	next       int
	outW, outH int
	srcW, srcH int
	events     chan uint32
	eventErr   chan error
	needInput  int
	pending    []int // ring slots converted but not yet submitted
	inFlight   []flight
	last       int // ring slot of the newest converted frame, -1 none
	openedAt   time.Time
}

func (s *Session) run(ready chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(s.exited)
	initThread()
	// Capture is latency critical: keep its thread ahead of the game's.
	_, _, _ = procSetThreadPriority.Call(uintptr(windows.CurrentThread()), 2) // THREAD_PRIORITY_HIGHEST
	d, err := newDevice()
	if err != nil {
		ready <- err
		return
	}
	defer d.close()
	capt, err := newCapturer(d, s.cfg.Source)
	if err != nil {
		ready <- err
		return
	}
	defer capt.close()
	p := &pipeline{d: d, last: -1}
	defer p.closeSized()
	// The first frame fixes the source size; wait for it before reporting ready.
	deadline := time.Now().Add(3 * time.Second)
	started := false
	for !started {
		got, err := capt.latest(func(tex uintptr, w, h uint32) {
			if err := s.ensureSized(p, int(w), int(h)); err != nil {
				ready <- err
				started = true
				return
			}
			s.convert(p, tex)
			started = true
			ready <- nil
		})
		if err != nil {
			ready <- err
			return
		}
		if !got {
			if time.Now().After(deadline) {
				// A minimized window delivers no frames at all.
				ready <- errors.New("capture produced no frames")
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	if p.enc == nil {
		return // ensureSized failed and already reported.
	}
	s.loop(p, capt)
}

func (s *Session) ensureSized(p *pipeline, w, h int) error {
	if r := s.cfg.Source.Region; r != nil {
		w, h = min(r.Width, w-r.X), min(r.Height, h-r.Y)
		if w < 2 || h < 2 {
			return errors.New("capture region outside the source")
		}
	}
	maxW, maxH := s.cfg.MaxWidth, s.cfg.MaxHeight
	if r := s.resize.Swap(nil); r != nil {
		s.cfg.MaxWidth, s.cfg.MaxHeight = r[0], r[1]
		maxW, maxH = r[0], r[1]
		p.srcW = 0 // force rebuild
	}
	if p.enc != nil && p.srcW == w && p.srcH == h {
		return nil
	}
	ow, oh := outputSize(w, h, maxW, maxH)
	p.closeSized()
	var err error
	if p.conv, err = p.d.newConverter(uint32(w), uint32(h), uint32(ow), uint32(oh)); err != nil {
		return err
	}
	for i := range p.ring {
		if p.ring[i].tex, err = p.d.createTexture(uint32(ow), uint32(oh), dxgiFormatNV12, bindRenderTarget); err != nil {
			return err
		}
		if p.ring[i].view, err = p.conv.outputView(p.ring[i].tex); err != nil {
			return err
		}
	}
	fps := int(s.fps.Load())
	if p.enc, err = newEncoder(p.d, uint32(ow), uint32(oh), uint32(max(fps, 30)), uint32(s.bitrate.Load()), s.cfg.AllowSoftware); err != nil {
		return err
	}
	p.srcW, p.srcH, p.outW, p.outH = w, h, ow, oh
	p.openedAt = time.Now()
	p.needInput, p.pending, p.inFlight, p.last = 0, nil, nil, -1
	if !p.enc.async {
		p.needInput = 1 << 30 // Synchronous MFT: input is always accepted.
	} else {
		p.events, p.eventErr = make(chan uint32, 64), make(chan error, 1)
		vcall(p.enc.events, 1) // AddRef: waitEvents owns this reference.
		go waitEvents(p.enc.events, p.events, p.eventErr)
	}
	s.mu.Lock()
	s.info = EncoderInfo{Name: p.enc.info.name, Vendor: p.enc.info.vendor, Hardware: p.enc.info.hardware}
	s.stats.Width, s.stats.Height = ow, oh
	s.mu.Unlock()
	s.keyframe.Store(true)
	return nil
}

// waitEvents forwards async MFT events; it ends when the MFT shuts down.
func waitEvents(generator uintptr, events chan<- uint32, errs chan<- error) {
	defer release(generator)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	initThread()
	for {
		kind, err := waitEvent(generator)
		if err != nil {
			errs <- err
			close(events)
			return
		}
		events <- kind
	}
}

func (p *pipeline) closeSized() {
	if p.enc != nil {
		p.enc.close() // Shuts down the event generator, ending waitEvents.
		if p.events != nil {
			for range p.events {
			}
		}
		p.enc, p.events = nil, nil
	}
	for i := range p.ring {
		release(p.ring[i].view)
		release(p.ring[i].tex)
		p.ring[i] = ringSlot{}
	}
	if p.conv != nil {
		p.conv.close()
		p.conv = nil
	}
}

// convert crops the captured texture into the converter input and scales it
// into the next NV12 ring slot.
func (s *Session) convert(p *pipeline, tex uintptr) {
	box := d3dBox{Right: uint32(p.srcW), Bottom: uint32(p.srcH), Back: 1}
	if r := s.cfg.Source.Region; r != nil {
		box = d3dBox{Left: uint32(r.X), Top: uint32(r.Y), Right: uint32(r.X + p.srcW), Bottom: uint32(r.Y + p.srcH), Back: 1}
	}
	p.d.copyRegion(p.conv.input, tex, box)
	// The encoder may still read a submitted texture: never overwrite one.
	slot := freeSlot(p.next, nv12Ring, p.inFlight, time.Now())
	if slot < 0 {
		return // every slot is held: drop this frame
	}
	p.next = (slot + 1) % nv12Ring
	if err := p.conv.blt(p.ring[slot].view); err != nil {
		s.fail(err)
		return
	}
	p.ring[slot].queued = time.Now()
	p.last = slot
	// Keep only the newest unsubmitted frame: never encode stale content.
	p.pending = append(p.pending[:0], slot)
}

func (s *Session) loop(p *pipeline, capt *capturer) {
	start := time.Now()
	var lastSubmit time.Time
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	nextCapture := time.Now()
	submit := func(now time.Time) error {
		if len(p.pending) == 0 || p.needInput <= 0 || p.enc == nil || s.paused.Load() {
			return nil
		}
		slot := p.pending[0]
		p.pending = p.pending[:0]
		interval := time.Second / time.Duration(s.fps.Load())
		if err := p.enc.submit(p.ring[slot].tex, now.Sub(start).Nanoseconds()/100, int64(interval/100), s.keyframe.Swap(false)); err != nil {
			return err
		}
		p.needInput--
		queued := p.ring[slot].queued
		if !queued.After(lastSubmit) {
			queued = now // A resent picture: measure from the resend.
		}
		p.inFlight = append(p.inFlight, flight{at: queued, slot: slot})
		lastSubmit = now
		if !p.enc.async {
			return s.drain(p)
		}
		return nil
	}
	for {
		select {
		case <-s.done:
			return
		case kind, ok := <-p.events:
			if !ok {
				s.fail(<-p.eventErr)
				return
			}
			switch kind {
			case meTransformNeedInput:
				p.needInput++
				// A frame converted while the encoder was busy goes out now.
				if err := submit(time.Now()); err != nil {
					s.fail(err)
					return
				}
			case meTransformHaveOutput:
				if err := s.drain(p); err != nil {
					s.fail(err)
					return
				}
			}
		case now := <-tick.C:
			if now.Before(nextCapture) {
				break
			}
			if s.cfg.Source.Window != 0 {
				alive, minimized := WindowState(s.cfg.Source.Window)
				if !alive {
					s.fail(ErrSourceClosed)
					return
				}
				s.mu.Lock()
				s.stats.Minimized = minimized
				s.mu.Unlock()
			}
			interval := time.Second / time.Duration(s.fps.Load())
			nextCapture = nextCapture.Add(interval)
			if now.Sub(nextCapture) > interval {
				nextCapture = now.Add(interval)
			}
			if s.resize.Load() != nil {
				p.srcW = 0
			}
			got, err := capt.latest(func(tex uintptr, w, h uint32) {
				if err := s.ensureSized(p, int(w), int(h)); err != nil {
					s.fail(err)
					return
				}
				s.convert(p, tex)
			})
			if err != nil {
				s.fail(err)
				return
			}
			if got {
				s.mu.Lock()
				s.stats.Captured++
				s.mu.Unlock()
			}
			if p.enc == nil {
				return
			}
			if s.paused.Load() {
				p.pending = p.pending[:0]
				break
			}
			// Static or minimized source: resend the last picture once a second,
			// and at once for a keyframe request, so viewers get a picture.
			if len(p.pending) == 0 && p.last >= 0 && (now.Sub(lastSubmit) >= time.Second || s.keyframe.Load() && now.Sub(lastSubmit) >= 100*time.Millisecond) {
				p.pending = append(p.pending, p.last)
			}
			if err := submit(now); err != nil {
				s.fail(err)
				return
			}
			// Dynamic CODECAPI bitrate changes are not honoured by every
			// hardware MFT (Quick Sync doubled its output instead), so a
			// significant change reopens the encoder, at most every 3 s.
			if br := int(s.bitrate.Load()); br > 0 && p.enc != nil && now.Sub(p.openedAt) >= 3*time.Second {
				if diff := br - p.enc.bitrate; diff*4 > p.enc.bitrate || -diff*4 > p.enc.bitrate {
					p.srcW = 0 // rebuilt with the new bitrate on the next captured frame
				}
			}
		}
	}
}

func (s *Session) drain(p *pipeline) error {
	for {
		data, key, err := p.enc.collect()
		if err != nil || data == nil {
			return err
		}
		captured := time.Now()
		if len(p.inFlight) > 0 {
			captured = p.inFlight[0].at
			p.inFlight = p.inFlight[1:]
		}
		s.mu.Lock()
		s.stats.Encoded++
		if key {
			s.stats.Keyframes++
		}
		s.stats.EncodeTime += time.Since(captured)
		s.mu.Unlock()
		s.out(Frame{Data: data, Keyframe: key, SeqHeader: p.enc.seqHeader, Captured: captured, Width: p.outW, Height: p.outH})
		if p.enc.async {
			return nil // One output per METransformHaveOutput.
		}
	}
}
