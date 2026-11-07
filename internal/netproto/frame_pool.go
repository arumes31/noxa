package netproto

import (
	"encoding/binary"
	"fmt"
	"io"
	"sync"
)

// Pooled frame reads (plan item #3) reuse body buffers for the server's
// control read loop. A pooled frame's Payload aliases a buffer that returns
// to the pool on release, so it is only safe where the caller consumes the
// payload synchronously and keeps nothing from it: every server handler
// decodes through encoding/json, which copies, and none retains the frame.
// The desktop client hands frames to waiting goroutines and keeps using
// ReadFrameLimit.

// frameBufferTiers are the pooled buffer sizes; the last covers MaxFrameSize.
var frameBufferTiers = [...]int{4 << 10, 64 << 10, MaxFrameSize}

var frameBufferPools [len(frameBufferTiers)]sync.Pool

func init() {
	for i, size := range frameBufferTiers {
		frameBufferPools[i].New = func() any {
			b := make([]byte, size)
			return &b
		}
	}
}

// poisonReleasedFrames overwrites a released buffer so that any code still
// reading a released payload sees garbage. Tests enable it to prove that no
// handler retains payload bytes.
var poisonReleasedFrames bool

// SetFramePoisoning enables or disables overwriting released pooled buffers.
// It is meant for tests only and is not safe to change while frames are read.
func SetFramePoisoning(on bool) { poisonReleasedFrames = on }

func frameBufferTier(n int) int {
	for i, size := range frameBufferTiers {
		if n <= size {
			return i
		}
	}
	return -1
}

// ReadFrameLimitPooled is ReadFrameLimit with a pooled body buffer. Call
// Release once the frame has been handled; afterwards its Payload must not be
// used. On error no buffer is held.
func ReadFrameLimitPooled(r io.Reader, limit uint32) (*Frame, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, fmt.Errorf("netproto: reading length prefix: %w", err)
	}
	length := binary.BigEndian.Uint32(header[:])
	if length < 2 {
		return nil, fmt.Errorf("%w %d (must be >= 2)", ErrInvalidFrameLength, length)
	}
	if length > MaxFrameSize || length > limit {
		return nil, ErrFrameTooLarge
	}
	n := int(length)
	tier := frameBufferTier(n)
	if tier < 0 {
		return nil, ErrFrameTooLarge
	}
	bp, _ := frameBufferPools[tier].Get().(*[]byte)
	if bp == nil || len(*bp) < n {
		b := make([]byte, frameBufferTiers[tier])
		bp = &b
	}
	buf := (*bp)[:n]
	if _, err := io.ReadFull(r, buf); err != nil {
		frameBufferPools[tier].Put(bp)
		return nil, fmt.Errorf("netproto: reading frame body: %w", err)
	}
	return &Frame{Type: binary.BigEndian.Uint16(buf[:2]), Payload: buf[2:], pooled: bp, tier: tier}, nil
}

// Release returns a pooled frame's buffer and clears its Payload. It is a
// no-op for frames that were not read with ReadFrameLimitPooled and for a
// second call.
func (f *Frame) Release() {
	if f == nil || f.pooled == nil {
		return
	}
	bp := f.pooled
	f.pooled, f.Payload = nil, nil
	if poisonReleasedFrames {
		b := *bp
		for i := range b {
			b[i] = 0xA5
		}
	}
	frameBufferPools[f.tier].Put(bp)
}
