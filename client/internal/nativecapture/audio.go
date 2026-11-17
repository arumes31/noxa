package nativecapture

import (
	"errors"
	"time"
)

// Native share audio: WASAPI process loopback (build 20348+) captured as
// 48 kHz stereo 16-bit PCM and encoded to 20 ms Opus packets by the pinned
// libopus DLL (see opus_windows.go).

const (
	// AudioRate, AudioChannels and AudioFrame are the Opus profile: 48 kHz
	// stereo, 20 ms frames (960 samples per channel).
	AudioRate     = 48000
	AudioChannels = 2
	AudioFrame    = 960
	// AudioBitrate matches the webview's music-quality screen audio.
	AudioBitrate = 128_000
)

// AudioMode is what a share's audio carries.
type AudioMode string

const (
	AudioNone        AudioMode = "none"
	AudioApplication AudioMode = "application" // the shared window's process tree only
	AudioSystem      AudioMode = "system"      // everything except noXa's own process tree
)

var (
	// ErrAudioUnsupported: process loopback needs Windows build 20348 (Windows 11 / Server 2022) or later.
	ErrAudioUnsupported = errors.New("process loopback audio is not supported here")
	// ErrOpusUnavailable: the bundled libopus DLL is missing or failed its check.
	ErrOpusUnavailable = errors.New("opus encoder unavailable")
)

// AudioPlan is the ordered list of audio sources to try for a request: an
// application share falls back to system audio (minus noXa), never to plain
// endpoint loopback, which would echo every voice noXa plays.
func AudioPlan(requested string, window bool) []AudioMode {
	switch AudioMode(requested) {
	case AudioApplication:
		if window {
			return []AudioMode{AudioApplication, AudioSystem}
		}
		return []AudioMode{AudioSystem}
	case AudioSystem:
		return []AudioMode{AudioSystem}
	default:
		return nil
	}
}

// AudioPacket is one encoded 20 ms Opus packet and the capture time of its
// first sample (the clock video capture times use).
type AudioPacket struct {
	Data     []byte
	Captured time.Time
}

// audioFramer cuts interleaved PCM packets of any size into 20 ms frames and
// dates each frame by the capture time of its first sample. A gap or a
// discontinuity restarts the frame (its partial samples are dropped).
type audioFramer struct {
	buf   []int16
	start time.Time // capture time of buf[0]
}

// frameDuration is one Opus frame.
const frameDuration = AudioFrame * time.Second / AudioRate

// push appends pcm (interleaved stereo) captured at at; emit receives each
// complete frame (the slice is reused after emit returns).
func (f *audioFramer) push(pcm []int16, at time.Time, discontinuity bool, emit func([]int16, time.Time)) {
	if len(f.buf) > 0 {
		expected := f.start.Add(time.Duration(len(f.buf)/AudioChannels) * time.Second / AudioRate)
		if d := at.Sub(expected); discontinuity || d > frameDuration || d < -frameDuration {
			f.buf = f.buf[:0]
		}
	}
	if len(f.buf) == 0 {
		f.start = at
	}
	f.buf = append(f.buf, pcm...)
	n := AudioFrame * AudioChannels
	for len(f.buf) >= n {
		emit(f.buf[:n], f.start)
		f.buf = append(f.buf[:0], f.buf[n:]...)
		f.start = f.start.Add(frameDuration)
	}
}
