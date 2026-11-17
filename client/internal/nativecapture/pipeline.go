// Package nativecapture captures a monitor or window with Windows Graphics
// Capture, converts it to NV12 on the GPU and encodes H.264 with a Media
// Foundation hardware encoder, all in pure Go (no cgo). Other platforms get
// a stub that reports ErrUnsupported.
package nativecapture

import (
	"errors"
	"slices"
	"time"
)

var (
	// ErrUnsupported: native capture is not available on this platform/OS.
	ErrUnsupported = errors.New("native screen capture is not supported here")
	// ErrSourceClosed: the captured window was closed.
	ErrSourceClosed = errors.New("captured window closed")
)

// Source selects what to capture: a window when Window != 0, else a monitor.
type Source struct {
	Monitor int    `json:"monitor"`
	Window  uint64 `json:"window,string"`
}

// Config is the requested capture/encode profile.
type Config struct {
	Source              Source
	MaxWidth, MaxHeight int
	FPS                 int
	Bitrate             int // bits per second
	// AllowSoftware admits a software encoder MFT (tests only).
	AllowSoftware bool
}

// Frame is one encoded H.264 access unit (Annex-B, as the encoder wrote it).
type Frame struct {
	Data      []byte
	Keyframe  bool
	SeqHeader []byte // MF_MT_MPEG_SEQUENCE_HEADER (Annex-B SPS/PPS), may be empty
	Captured  time.Time
	Width     int
	Height    int
}

// EncoderInfo describes the encoder actually in use.
type EncoderInfo struct {
	Name     string `json:"name"`
	Vendor   string `json:"vendor"`
	Hardware bool   `json:"hardware"`
}

// Stats are cumulative counters since Start.
type Stats struct {
	Captured, Encoded, Keyframes uint64
	EncodeTime                   time.Duration // sum of submit→output latencies
	Width, Height                int
	Minimized                    bool
}

// outputSize fits (w,h) into (maxW,maxH) keeping the aspect ratio, with even
// dimensions as NV12 and H.264 4:2:0 require; never upscales.
func outputSize(w, h, maxW, maxH int) (int, int) {
	if w <= 0 || h <= 0 {
		return 0, 0
	}
	if maxW <= 0 {
		maxW = w
	}
	if maxH <= 0 {
		maxH = h
	}
	scale := min(1, float64(maxW)/float64(w), float64(maxH)/float64(h))
	ow, oh := int(float64(w)*scale)&^1, int(float64(h)*scale)&^1
	return max(ow, 2), max(oh, 2)
}

// flight is one submitted picture: its capture time, NV12 ring slot and
// encoder sample time (100 ns units).
type flight struct {
	at   time.Time
	slot int
	ts   int64
}

// takeFlight pops the in-flight pictures up to an output's sample time (an
// async MFT may drop inputs) and returns the matching capture time. Without a
// usable sample time it falls back to the oldest picture.
func takeFlight(inFlight []flight, ts int64, hasTS bool) ([]flight, time.Time, bool) {
	n := 0
	if hasTS {
		for n < len(inFlight) && inFlight[n].ts <= ts {
			n++
		}
	}
	if n == 0 {
		if len(inFlight) == 0 {
			return inFlight, time.Time{}, false
		}
		n = 1
	}
	return inFlight[n:], inFlight[n-1].at, true
}

// resizeSettle is how long a new source size must hold before the sized
// pipeline is rebuilt: dragging a window edge changes it every frame.
const resizeSettle = 300 * time.Millisecond

// sizeHold debounces source size changes.
type sizeHold struct {
	w, h  int
	since time.Time
}

// rebuild reports whether a curW x curH pipeline is due a rebuild for a w x h
// source at now: only once that size has held for resizeSettle.
func (s *sizeHold) rebuild(curW, curH, w, h int, now time.Time) bool {
	if w == curW && h == curH {
		*s = sizeHold{}
		return false
	}
	if w != s.w || h != s.h {
		*s = sizeHold{w: w, h: h, since: now}
		return false
	}
	return now.Sub(s.since) >= resizeSettle
}

// flightHold bounds how long a submitted slot counts as held: an encoder
// that silently dropped a frame must not pin its slot forever.
const flightHold = time.Second

// freeSlot returns the first ring slot from next that no recent in-flight
// picture holds, or -1 when the encoder holds them all.
func freeSlot(next, size int, inFlight []flight, now time.Time) int {
	for i := range size {
		slot := (next + i) % size
		if !slices.ContainsFunc(inFlight, func(f flight) bool { return f.slot == slot && now.Sub(f.at) < flightHold }) {
			return slot
		}
	}
	return -1
}

// Window is one capturable top-level window.
type Window struct {
	Handle uint64 `json:"handle,string"`
	Title  string `json:"title"`
}

// Monitor is one display, in EnumDisplayMonitors order.
type Monitor struct {
	Index   int  `json:"index"`
	Width   int  `json:"width"`
	Height  int  `json:"height"`
	Primary bool `json:"primary"`
}
