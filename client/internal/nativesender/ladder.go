package nativesender

import "time"

// Level is one encode setting chosen by the quality ladder.
type Level struct {
	MaxWidth, MaxHeight int
	FPS                 int
	Bitrate             int
}

// Ladder adapts the encoder to the bandwidth estimate: first the target
// bitrate, then the frame rate (60 → 30), then the resolution (1080p → 720p).
// Upgrades need sustained headroom so the encoder is not reopened on noise.
type Ladder struct {
	Top          Level // the user's profile: the ceiling
	current      Level
	upgradeSince time.Time
}

const (
	// Below these bitrates a step down is taken (60 fps needs more bits).
	fps60MinBitrate    = 4_000_000
	full1080MinBitrate = 1_800_000
	upgradeHeadroom    = 1.3
	upgradeHold        = 8 * time.Second
	// Share of the estimate given to video: leave room for RTX and audio.
	estimateShare = 0.85
)

// NewLadder starts at the top level with the initial bitrate.
func NewLadder(top Level, initialBitrate int) *Ladder {
	l := &Ladder{Top: top}
	l.current = l.shape(min(initialBitrate, top.Bitrate))
	return l
}

func (l *Ladder) Current() Level { return l.current }

// shape picks fps and size for a bitrate, never above the user's profile.
func (l *Ladder) shape(bitrate int) Level {
	out := Level{MaxWidth: l.Top.MaxWidth, MaxHeight: l.Top.MaxHeight, FPS: l.Top.FPS, Bitrate: bitrate}
	if out.FPS > 30 && bitrate < fps60MinBitrate {
		out.FPS = 30
	}
	if out.MaxHeight > 720 && bitrate < full1080MinBitrate {
		out.MaxWidth, out.MaxHeight = min(out.MaxWidth, 1280), 720
	}
	return out
}

// Update applies a new estimate. Downgrades are immediate; an upgrade of fps
// or size waits until the estimate held enough headroom for upgradeHold.
func (l *Ladder) Update(estimate int, now time.Time) Level {
	bitrate := max(minBitrate, min(int(float64(estimate)*estimateShare), l.Top.Bitrate))
	want := l.shape(bitrate)
	up := want.FPS > l.current.FPS || want.MaxHeight > l.current.MaxHeight
	if !up {
		l.upgradeSince = time.Time{}
		l.current = want
		return want
	}
	// Bitrate may still rise within the current shape.
	l.current.Bitrate = bitrate
	headroom := l.shape(int(float64(bitrate) / upgradeHeadroom))
	if headroom.FPS <= l.current.FPS && headroom.MaxHeight <= l.current.MaxHeight {
		l.upgradeSince = time.Time{}
		return l.current
	}
	if l.upgradeSince.IsZero() {
		l.upgradeSince = now
	}
	if now.Sub(l.upgradeSince) >= upgradeHold {
		l.upgradeSince = time.Time{}
		l.current = want
	}
	return l.current
}
