package nativesender

import (
	"testing"
	"time"
)

func TestLadderStepsBitrateThenFPSThenResolution(t *testing.T) {
	top := Level{MaxWidth: 1920, MaxHeight: 1080, FPS: 60, Bitrate: 8_000_000}
	l := NewLadder(top, 8_000_000)
	now := time.Unix(0, 0)
	if got := l.Update(20_000_000, now); got != top {
		t.Fatalf("ample bandwidth: %+v", got)
	}
	if got := l.Update(7_000_000, now); got.FPS != 60 || got.MaxHeight != 1080 || got.Bitrate != 5_950_000 {
		t.Fatalf("bitrate first: %+v", got)
	}
	if got := l.Update(3_000_000, now); got.FPS != 30 || got.MaxHeight != 1080 {
		t.Fatalf("then fps: %+v", got)
	}
	if got := l.Update(1_500_000, now); got.FPS != 30 || got.MaxHeight != 720 || got.MaxWidth != 1280 {
		t.Fatalf("then resolution: %+v", got)
	}
	if got := l.Update(100_000, now); got.Bitrate != minBitrate {
		t.Fatalf("floor: %+v", got)
	}
}

func TestLadderUpgradeNeedsSustainedHeadroom(t *testing.T) {
	top := Level{MaxWidth: 1920, MaxHeight: 1080, FPS: 60, Bitrate: 8_000_000}
	l := NewLadder(top, 1_000_000)
	start := time.Unix(0, 0)
	if got := l.Current(); got.MaxHeight != 720 || got.FPS != 30 {
		t.Fatalf("initial: %+v", got)
	}
	// Just above the 1080p threshold: no headroom, stay at 720p.
	if got := l.Update(2_200_000, start); got.MaxHeight != 720 {
		t.Fatalf("no headroom upgrade: %+v", got)
	}
	// Enough headroom but not long enough.
	l.Update(3_500_000, start)
	if got := l.Update(3_500_000, start.Add(upgradeHold/2)); got.MaxHeight != 720 || got.Bitrate != 2_975_000 {
		t.Fatalf("early upgrade or bitrate not raised: %+v", got)
	}
	if got := l.Update(3_500_000, start.Add(upgradeHold)); got.MaxHeight != 1080 || got.FPS != 30 {
		t.Fatalf("sustained upgrade: %+v", got)
	}
	// A dip resets the hold.
	l.Update(20_000_000, start.Add(upgradeHold))
	l.Update(3_000_000, start.Add(upgradeHold+time.Second))
	if got := l.Update(20_000_000, start.Add(upgradeHold+2*time.Second)); got.FPS != 30 {
		t.Fatalf("hold not reset: %+v", got)
	}
}

func TestLadderRespectsUserProfile(t *testing.T) {
	l := NewLadder(Level{MaxWidth: 1280, MaxHeight: 720, FPS: 30, Bitrate: 2_500_000}, 2_500_000)
	if got := l.Update(50_000_000, time.Unix(0, 0)); got != (Level{MaxWidth: 1280, MaxHeight: 720, FPS: 30, Bitrate: 2_500_000}) {
		t.Fatalf("exceeded profile: %+v", got)
	}
}
