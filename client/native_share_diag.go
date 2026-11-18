package main

// native_share_diag.go: client.log diagnostics and the stall watchdog of a
// native screen share. Counters are cumulative; one compact line is logged
// at 2, 5 and 10 s, then every 60 s, and when the share stalls.

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"noxa/client/internal/nativecapture"
	"noxa/internal/framecrypt"
	"noxa/internal/nativesender"
)

const (
	// nativeConnectTimeout: the native peer must connect this soon after its offer.
	nativeConnectTimeout = 15 * time.Second
	// nativeVideoTimeout: a connected, uploading share must write video RTP this often.
	nativeVideoTimeout = 10 * time.Second
	// nativeReasonLimit bounds the distinct first-occurrence reasons kept.
	nativeReasonLimit = 16
)

// Browser-mode share diagnostics from the webview: lines per process, bytes per line.
const (
	browserShareLogMax   = 64
	browserShareLogBytes = 1024
)

var browserShareLogs atomic.Int32

// LogScreenShareDiagnostics writes one Browser-mode screen share stats line
// (built by the webview) to client.log: bounded, cut and escaped.
func (a *App) LogScreenShareDiagnostics(line string) {
	if browserShareLogs.Add(1) > browserShareLogMax {
		return
	}
	log.Printf("browser share %s", browserShareLogLine(line))
}

func browserShareLogLine(line string) string {
	if len(line) > browserShareLogBytes {
		cut := browserShareLogBytes
		for cut > 0 && !utf8.RuneStart(line[cut]) {
			cut--
		}
		line = line[:cut] + "..."
	}
	return escapeLogRecord(line)
}

// nativeStatsAt is when the i-th (0-based) stats line is due after start.
func nativeStatsAt(i int) time.Duration {
	if marks := []time.Duration{2 * time.Second, 5 * time.Second, 10 * time.Second}; i < len(marks) {
		return marks[i]
	}
	return 10*time.Second + time.Duration(i-2)*time.Minute
}

// nativeFrameDiag counts encoded frames that never became RTP and logs the
// first occurrence of each distinct reason (never one line per frame).
type nativeFrameDiag struct {
	unsent, rejected, dropped atomic.Uint64 // upload off/no peer; refused by the sender; withheld (no key, key gap)
	noKey                     atomic.Uint64 // the dropped frames that had no active media key

	mu   sync.Mutex
	last string
	seen map[string]bool
}

// note records a frame (or audio packet) the sender refused.
func (d *nativeFrameDiag) note(kind string, err error) {
	switch kind {
	case "rejected":
		d.rejected.Add(1)
	case "dropped":
		d.dropped.Add(1)
		if errors.Is(err, framecrypt.ErrNoKey) {
			d.noKey.Add(1)
		}
	}
	reason := kind + ": " + strings.ReplaceAll(err.Error(), "\n", "; ") // errors.Join text is multi-line
	d.mu.Lock()
	d.last = reason
	first := !d.seen[reason] && len(d.seen) < nativeReasonLimit
	if first {
		if d.seen == nil {
			d.seen = make(map[string]bool)
		}
		d.seen[reason] = true
	}
	d.mu.Unlock()
	if first {
		log.Printf("native share first %s", reason)
	}
}

// dropKind names a sender error: "dropped" for a frame withheld for lack of
// a key (no active media key, or waiting for a keyframe), else "rejected".
func dropKind(err error) string {
	if errors.Is(err, nativesender.ErrDropped) {
		return "dropped"
	}
	return "rejected"
}

// nativeShareSnapshot is one stats line's input.
type nativeShareSnapshot struct {
	elapsed                   time.Duration
	peer                      string
	upload                    bool
	generation                uint64
	cs                        nativecapture.Stats
	ss                        nativesender.Stats
	encoder                   nativecapture.EncoderInfo
	unsent, rejected, dropped uint64
	noKey                     uint64
	last                      string
}

func (s nativeShareSnapshot) String() string {
	onOff := map[bool]string{true: "on", false: "off"}
	kind := map[bool]string{true: "hw", false: "sw"}[s.encoder.Hardware]
	var b strings.Builder
	fmt.Fprintf(&b, "t=%s peer=%s upload=%s gen=%d minimized=%v captured=%d converted=%d submitted=%d needinput=%d encoded=%d keyframes=%d",
		s.elapsed.Round(time.Second), s.peer, onOff[s.upload], s.generation, s.cs.Minimized,
		s.cs.Captured, s.cs.Converted, s.cs.Submitted, s.cs.NeedInput, s.cs.Encoded, s.cs.Keyframes)
	fmt.Fprintf(&b, " unsent=%d rejected=%d dropped=%d nokey=%d sent=%d keysent=%d rtp=%d wire=%d audio=%d/%d encoder=%q/%s/%s",
		s.unsent, s.rejected, s.dropped, s.noKey, s.ss.FramesSent, s.ss.KeyframesSent, s.ss.VideoPackets, s.ss.PacketsSent,
		s.ss.AudioFramesSent, s.ss.AudioFramesDropped, s.encoder.Name, s.encoder.Vendor, kind)
	if s.last != "" {
		fmt.Fprintf(&b, " last=%q", s.last)
	}
	return b.String()
}

// nativeStallState is the watchdog's view of one native peer.
type nativeStallState struct {
	Offered   time.Time // the current sender's offer was answered
	Connected time.Time // zero until that peer reached connected
	LastVideo time.Time // a video RTP packet was last written
	Resumed   time.Time // the share last left an idle state (paused, minimized, restarted)
	Idle      bool      // paused (no viewer demand) or minimized by design
	Fired     bool
}

// nativeStallReason is the watchdog decision: why the share stalled, or "".
func nativeStallReason(s nativeStallState, now time.Time) string {
	if s.Fired || s.Idle || s.Offered.IsZero() {
		return ""
	}
	if s.Connected.IsZero() {
		if now.Sub(s.Offered) >= nativeConnectTimeout {
			return fmt.Sprintf("native peer not connected %s after the offer", nativeConnectTimeout)
		}
		return ""
	}
	since := s.Connected
	for _, t := range []time.Time{s.LastVideo, s.Resumed} {
		if t.After(since) {
			since = t
		}
	}
	if now.Sub(since) >= nativeVideoTimeout {
		return fmt.Sprintf("no video RTP written for %s", now.Sub(since).Round(time.Second))
	}
	return ""
}
