// Package nativecapture captures a monitor or window with Windows Graphics
// Capture, converts it to NV12 on the GPU and encodes H.264 with a Media
// Foundation hardware encoder, all in pure Go (no cgo). Other platforms get
// a stub that reports ErrUnsupported.
package nativecapture

import (
	"errors"
	"time"
)

var (
	// ErrUnsupported: native capture is not available on this platform/OS.
	ErrUnsupported = errors.New("native screen capture is not supported here")
	// ErrSourceClosed: the captured window was closed.
	ErrSourceClosed = errors.New("captured window closed")
)

// Region is a crop rectangle in source pixels.
type Region struct{ X, Y, Width, Height int }

// Source selects what to capture: a window when Window != 0, else a monitor.
type Source struct {
	Monitor int     `json:"monitor"`
	Window  uint64  `json:"window,string"`
	Region  *Region `json:"region,omitempty"`
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
