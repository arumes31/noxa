// Package nativecapture captures a monitor or window with Windows Graphics
// Capture, converts it to NV12 on the GPU and encodes H.264 with a Media
// Foundation hardware encoder, all in pure Go (no cgo). Other platforms get
// a stub that reports ErrUnsupported.
package nativecapture

import (
	"errors"
	"image"
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
// Region crops the capture; Studio composes the session's scene instead.
type Source struct {
	Monitor int     `json:"monitor"`
	Window  uint64  `json:"window,string"`
	Region  *Region `json:"region,omitempty"`
	Studio  bool    `json:"studio,omitempty"`
}

// Region is a crop rectangle in fractions (0..1) of the captured frame.
type Region struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// pixelRect is a rectangle in pixels.
type pixelRect struct{ X, Y, W, H int }

// cropRect is region in pixels of a w x h frame, aligned to even values as
// share-frame-geometry.js cropRect does; a nil region is the whole frame.
func cropRect(w, h int, r *Region) pixelRect {
	if r == nil || w < 2 || h < 2 {
		return pixelRect{W: w, H: h}
	}
	unit := func(v float64) float64 { return min(1, max(0, v)) }
	even := func(v float64) int { return max(0, int(v)/2*2) }
	x := min(even(unit(r.X)*float64(w)), even(float64(w-2)))
	y := min(even(unit(r.Y)*float64(h)), even(float64(h-2)))
	cw := max(2, min(even(unit(r.Width)*float64(w)), even(float64(w-x))))
	ch := max(2, min(even(unit(r.Height)*float64(h)), even(float64(h-y))))
	return pixelRect{X: x, Y: y, W: cw, H: ch}
}

// Config is the requested capture/encode profile.
type Config struct {
	Source              Source
	MaxWidth, MaxHeight int
	FPS                 int
	Bitrate             int // bits per second
	// AllowSoftware admits a software encoder MFT (tests only).
	AllowSoftware bool
	// Scene and Overlays are the first Studio scene (Source.Studio), composed
	// on a CanvasWidth x CanvasHeight canvas (default MaxWidth x MaxHeight).
	Scene                     Scene
	Overlays                  []*image.NRGBA
	CanvasWidth, CanvasHeight int
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
	// CameraError: a Studio camera could not be opened (busy, unplugged).
	CameraError string
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

// Window is one capturable top-level window (noXa's own are never listed).
type Window struct {
	Handle uint64 `json:"handle,string"`
	Title  string `json:"title"`
	// Exe is the owning process's executable file name (e.g. "game.exe").
	Exe string `json:"exe"`
	// Fullscreen: the window covers its whole monitor (a running game, a
	// fullscreen video); the picker lists these first.
	Fullscreen bool `json:"fullscreen"`
}

// Monitor is one display, in EnumDisplayMonitors order.
type Monitor struct {
	Index   int  `json:"index"`
	Width   int  `json:"width"`
	Height  int  `json:"height"`
	Primary bool `json:"primary"`
}
