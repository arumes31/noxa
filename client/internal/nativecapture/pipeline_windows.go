//go:build windows

package nativecapture

import (
	"errors"
	"image"
	"log"
	"runtime"
	"strings"
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

	studio *studioInbox // Source.Studio only

	encoderLog string // the last logged encoder selection (pipeline thread only)
	ringBind   uint32 // NV12 ring bind flags that worked (pipeline thread only)
}

// Start opens the capture and an encoder and begins delivering frames
// to out (from the pipeline thread; out must not block for long).
func Start(cfg Config, out func(Frame)) (*Session, error) {
	if cfg.FPS <= 0 {
		cfg.FPS = 30
	}
	s := &Session{cfg: cfg, out: out, done: make(chan struct{}), exited: make(chan struct{}), fatal: make(chan error, 1)}
	if cfg.Source.Studio {
		s.studio = &studioInbox{}
		s.SetScene(cfg.Scene, cfg.Overlays)
	}
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

func (s *Session) Stats() Stats {
	s.mu.Lock()
	st := s.stats
	s.mu.Unlock()
	if s.studio != nil {
		st.CameraError = s.studio.cameraError()
	}
	return st
}

// SetScene puts a Studio scene (and its overlay images) on air; the
// pipeline thread takes it with the next frame. No-op for other sources.
func (s *Session) SetScene(scene Scene, overlays []*image.NRGBA) {
	if s.studio != nil {
		s.studio.next.Store(&studioUpdate{scene: clampScene(scene, len(overlays)), images: overlays})
	}
}

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

// frameSource delivers pictures to the pipeline: a WGC capturer, or the
// Studio compositor. A texture is valid only inside use.
type frameSource interface {
	latest(use func(tex uintptr, w, h uint32)) (bool, error)
	close()
}

type ringSlot struct {
	tex, view uintptr
	queued    time.Time
}

// pipeline holds the GPU objects that depend on the output size.
type pipeline struct {
	d            *device
	conv         *converter
	enc          *encoder
	ring         [nv12Ring]ringSlot
	next         int
	outW, outH   int
	srcW, srcH   int
	events       chan uint32
	eventErr     chan error
	needInput    int
	pending      []int // ring slots converted but not yet submitted
	inFlight     []flight
	last         int // ring slot of the newest converted frame, -1 none
	openedAt     time.Time
	size         sizeHold
	sliceChecked bool // the encoder's first keyframe was inspected for slices
	// held is a frame (AddRef'd) whose size differs from the pipeline's: a
	// window that stops repainting after a resize still gets its rebuild.
	held         uintptr
	heldW, heldH int
}

// keep retains the w x h tex while its cropped size r differs from the pipeline's.
func (p *pipeline) keep(tex uintptr, w, h int, r pixelRect) {
	release(p.held)
	p.held = 0
	if r.W != p.srcW || r.H != p.srcH {
		vcall(tex, 1) // AddRef
		p.held, p.heldW, p.heldH = tex, w, h
	}
}

func (s *Session) run(ready chan<- error) {
	// Never unlocked: the thread was raised to HIGHEST priority and joined to
	// the MTA, so it exits with this goroutine instead of returning to Go.
	runtime.LockOSThread()
	defer close(s.exited)
	initThread()
	// Capture is latency critical: keep its thread ahead of the game's.
	_, _, _ = procSetThreadPriority.Call(uintptr(windows.CurrentThread()), 2) // THREAD_PRIORITY_HIGHEST
	// The GPU scanning out the captured screen captures, converts and (by
	// preference, newEncoder) encodes: no cross-adapter copies on hybrid systems.
	d, err := newDevice(sourceMonitor(s.cfg.Source))
	if err != nil {
		ready <- err
		return
	}
	defer d.close()
	var capt frameSource
	if s.cfg.Source.Studio {
		w, h := s.cfg.CanvasWidth, s.cfg.CanvasHeight
		if w <= 0 || h <= 0 {
			w, h = s.cfg.MaxWidth, s.cfg.MaxHeight
		}
		capt, err = newCompositor(d, w, h, s.studio)
	} else {
		capt, err = newCapturer(d, s.cfg.Source)
	}
	if err != nil {
		ready <- err
		return
	}
	defer capt.close()
	p := &pipeline{d: d, last: -1}
	defer p.closeSized()
	defer func() { release(p.held) }()
	// The first frame fixes the source size; wait for it before reporting ready.
	deadline := time.Now().Add(3 * time.Second)
	started := false
	for !started {
		got, err := capt.latest(func(tex uintptr, w, h uint32) {
			r := cropRect(int(w), int(h), s.cfg.Source.Region)
			if err := s.ensureSized(p, r.W, r.H); err != nil {
				ready <- err
				started = true
				return
			}
			s.convert(p, tex, r)
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
	maxW, maxH := s.cfg.MaxWidth, s.cfg.MaxHeight
	if r := s.resize.Swap(nil); r != nil {
		s.cfg.MaxWidth, s.cfg.MaxHeight = r[0], r[1]
		maxW, maxH = r[0], r[1]
		p.srcW = 0 // force rebuild
	}
	// A forced rebuild (srcW 0) is immediate; a source resize waits until the
	// size settles, and convert scales the current frame meanwhile.
	if p.enc != nil && p.srcW != 0 && !p.size.rebuild(p.srcW, p.srcH, w, h, time.Now()) {
		return nil
	}
	p.size = sizeHold{}
	ow, oh := outputSize(w, h, maxW, maxH)
	p.closeSized()
	var err error
	if p.conv, err = p.d.newConverter(uint32(w), uint32(h), uint32(ow), uint32(oh)); err != nil {
		return err
	}
	for i := range p.ring {
		if p.ring[i].tex, p.ring[i].view, err = s.ringTexture(p, uint32(ow), uint32(oh)); err != nil {
			return err
		}
	}
	p.outW, p.outH, p.last = ow, oh, -1
	if err := s.openEncoder(p); err != nil {
		return err
	}
	p.srcW, p.srcH = w, h
	s.mu.Lock()
	s.stats.Width, s.stats.Height = ow, oh
	s.mu.Unlock()
	return nil
}

// openEncoder opens an encoder for the pipeline's output size, from a
// rebuild or (keeping converter, ring and the last picture) after the
// previous encoder wrote multi-slice keyframes.
func (s *Session) openEncoder(p *pipeline) error {
	fps := int(s.fps.Load())
	enc, report, err := newEncoder(p.d, uint32(p.outW), uint32(p.outH), uint32(max(fps, 30)), uint32(s.bitrate.Load()))
	// Once per session and outcome: size/bitrate reopens repeat the same selection.
	if outcome := strings.Join(report[1:], "\n"); outcome != s.encoderLog {
		s.encoderLog = outcome
		for _, line := range report {
			log.Printf("native encoder: %s", line)
		}
	}
	if err != nil {
		return err
	}
	p.enc = enc
	p.openedAt = time.Now()
	p.needInput, p.pending, p.inFlight, p.sliceChecked = 0, nil, nil, false
	if !p.enc.async {
		p.needInput = 1 << 30 // Synchronous MFT: input is always accepted.
	} else {
		p.events, p.eventErr = make(chan uint32, 64), make(chan error, 1)
		vcall(p.enc.events, 1) // AddRef: waitEvents owns this reference.
		go waitEvents(p.enc.events, p.events, p.eventErr)
	}
	s.mu.Lock()
	s.info = EncoderInfo{Name: p.enc.info.name, Vendor: p.enc.info.vendor, Hardware: p.enc.info.hardware}
	s.mu.Unlock()
	s.keyframe.Store(true)
	return nil
}

// ringTexture creates one NV12 encoder input and its video processor
// output view: a render target and, where the driver takes it (D3D 11.1),
// a video encoder input.
func (s *Session) ringTexture(p *pipeline, w, h uint32) (tex, view uintptr, err error) {
	if s.ringBind == 0 {
		s.ringBind = bindRenderTarget | bindVideoEncoder
	}
	create := func() (uintptr, uintptr, error) {
		tex, err := p.d.createTexture(w, h, dxgiFormatNV12, s.ringBind)
		if err != nil {
			return 0, 0, err
		}
		view, err := p.conv.outputView(tex)
		if err != nil {
			release(tex)
			return 0, 0, err
		}
		return tex, view, nil
	}
	tex, view, err = create()
	if err != nil && s.ringBind != bindRenderTarget {
		log.Printf("native encoder: NV12 input without D3D11_BIND_VIDEO_ENCODER: %v", err)
		s.ringBind = bindRenderTarget
		tex, view, err = create()
	}
	return tex, view, err
}

// waitEvents forwards async MFT events; it ends when the MFT shuts down.
func waitEvents(generator uintptr, events chan<- uint32, errs chan<- error) {
	defer release(generator)
	runtime.LockOSThread() // Never unlocked: the MTA thread exits with this goroutine.
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

func (p *pipeline) closeEncoder() {
	if p.enc != nil {
		p.enc.close() // Shuts down the event generator, ending waitEvents.
		if p.events != nil {
			for range p.events {
			}
		}
		p.enc, p.events = nil, nil
	}
}

func (p *pipeline) closeSized() {
	p.closeEncoder()
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

// convert copies the r part of the captured texture (the region crop, else
// the whole frame) into the converter input and scales it into the next NV12
// ring slot. While a resize settles the frame may differ from the pipeline
// size: its overlap is copied and scaled to fill.
func (s *Session) convert(p *pipeline, tex uintptr, r pixelRect) {
	w, h := min(r.W, p.srcW), min(r.H, p.srcH)
	p.d.copyRegion(p.conv.input, tex, d3dBox{Left: uint32(r.X), Top: uint32(r.Y), Right: uint32(r.X + w), Bottom: uint32(r.Y + h), Back: 1})
	// The encoder may still read a submitted texture: never overwrite one.
	slot := freeSlot(p.next, nv12Ring, p.inFlight, time.Now())
	if slot < 0 {
		return // every slot is held: drop this frame
	}
	p.next = (slot + 1) % nv12Ring
	if err := p.conv.blt(p.ring[slot].view, uint32(w), uint32(h)); err != nil {
		s.fail(err)
		return
	}
	p.ring[slot].queued = time.Now()
	p.last = slot
	s.mu.Lock()
	s.stats.Converted++
	s.mu.Unlock()
	// Keep only the newest unsubmitted frame: never encode stale content.
	p.pending = append(p.pending[:0], slot)
}

func (s *Session) loop(p *pipeline, capt frameSource) {
	start := time.Now()
	var lastSubmit time.Time
	var lastTS int64
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
		// Strictly increasing: drain matches outputs to inputs by sample time.
		ts := max(now.Sub(start).Nanoseconds()/100, lastTS+1)
		if err := p.enc.submit(p.ring[slot].tex, ts, int64(interval/100), s.keyframe.Swap(false)); err != nil {
			return err
		}
		lastTS = ts
		p.needInput--
		s.mu.Lock()
		s.stats.Submitted++
		s.mu.Unlock()
		queued := p.ring[slot].queued
		if !queued.After(lastSubmit) {
			queued = now // A resent picture: measure from the resend.
		}
		p.inFlight = append(p.inFlight, flight{at: queued, slot: slot, ts: ts})
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
				s.mu.Lock()
				s.stats.NeedInput++
				s.mu.Unlock()
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
			frame := func(tex uintptr, w, h int) {
				r := cropRect(w, h, s.cfg.Source.Region)
				if err := s.ensureSized(p, r.W, r.H); err != nil {
					s.fail(err)
					return
				}
				s.convert(p, tex, r)
				p.keep(tex, w, h, r)
			}
			got, err := capt.latest(func(tex uintptr, w, h uint32) { frame(tex, int(w), int(h)) })
			if err != nil {
				s.fail(err)
				return
			}
			if !got && p.held != 0 && now.Sub(p.size.since) >= resizeSettle {
				frame(p.held, p.heldW, p.heldH) // The resized source went static.
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
		data, key, ts, hasTS, err := p.enc.collect()
		if err != nil || data == nil {
			return err
		}
		var captured time.Time
		var ok bool
		if p.inFlight, captured, ok = takeFlight(p.inFlight, ts, hasTS); !ok {
			captured = time.Now()
		}
		if key && !p.sliceChecked {
			p.sliceChecked = true
			// H.264 slices cannot be merged and the sender admits one per access
			// unit: an encoder that ignored the slice setting is replaced at once
			// by the next candidate; the keyframe resend feeds it the last picture.
			if n := sliceCount(data); n > 1 && rejectMultiSlice(p.enc.info) {
				log.Printf("native encoder: %s wrote %d slices per keyframe; opening the next encoder", p.enc.info, n)
				p.closeEncoder()
				return s.openEncoder(p)
			}
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
