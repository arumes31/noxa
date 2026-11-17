package nativecapture

import (
	"testing"
	"time"
)

func TestOutputSizeFitsEvenAndNeverUpscales(t *testing.T) {
	for _, c := range []struct{ w, h, mw, mh, ow, oh int }{
		{2560, 1440, 1920, 1080, 1920, 1080},
		{3440, 1440, 1920, 1080, 1920, 802},
		{1281, 721, 1920, 1080, 1280, 720},
		{1080, 1920, 1920, 1080, 606, 1080},
		{800, 600, 0, 0, 800, 600},
		{1, 1, 1920, 1080, 2, 2},
	} {
		if ow, oh := outputSize(c.w, c.h, c.mw, c.mh); ow != c.ow || oh != c.oh {
			t.Errorf("outputSize(%d,%d,%d,%d) = %dx%d, want %dx%d", c.w, c.h, c.mw, c.mh, ow, oh, c.ow, c.oh)
		}
	}
}

func TestFreeSlotSkipsTexturesTheEncoderHolds(t *testing.T) {
	now := time.Now()
	held := []flight{{at: now, slot: 0}, {at: now, slot: 1}}
	if got := freeSlot(0, 4, held, now); got != 2 {
		t.Fatalf("freeSlot = %d, want 2", got)
	}
	if got := freeSlot(3, 4, held, now); got != 3 {
		t.Fatalf("freeSlot from 3 = %d, want 3", got)
	}
	all := []flight{held[0], held[1], {at: now, slot: 2}, {at: now, slot: 3}}
	if got := freeSlot(0, 4, all, now); got != -1 {
		t.Fatalf("freeSlot with every slot held = %d, want -1", got)
	}
	// A frame the encoder dropped without output frees its slot after flightHold.
	all[0].at = now.Add(-flightHold)
	if got := freeSlot(0, 4, all, now); got != 0 {
		t.Fatalf("freeSlot after hold = %d, want 0", got)
	}
}

func TestTakeFlightSkipsInputsTheEncoderDropped(t *testing.T) {
	t0 := time.Now()
	fl := []flight{{at: t0, ts: 10}, {at: t0.Add(time.Millisecond), ts: 20}, {at: t0.Add(2 * time.Millisecond), ts: 30}}
	// ts 10 was dropped: the output for 20 pops both and reports 20's capture time.
	rest, at, ok := takeFlight(fl, 20, true)
	if !ok || !at.Equal(fl[1].at) || len(rest) != 1 || rest[0].ts != 30 {
		t.Fatalf("takeFlight(20) = %v %v %v", rest, at, ok)
	}
	// No usable sample time: fall back to the oldest picture.
	rest, at, ok = takeFlight(fl, 0, false)
	if !ok || !at.Equal(fl[0].at) || len(rest) != 2 {
		t.Fatalf("takeFlight without time = %v %v %v", rest, at, ok)
	}
	if _, _, ok := takeFlight(nil, 5, true); ok {
		t.Fatal("takeFlight on an empty queue reported a match")
	}
}

func TestSizeHoldRebuildsOnlyOnceTheSizeSettles(t *testing.T) {
	now := time.Now()
	var s sizeHold
	if s.rebuild(800, 600, 800, 600, now) {
		t.Fatal("unchanged size rebuilt")
	}
	// Dragging an edge: a new size every frame never rebuilds.
	for i := range 10 {
		if s.rebuild(800, 600, 810+i, 600, now.Add(time.Duration(i)*50*time.Millisecond)) {
			t.Fatalf("rebuilt mid-drag at step %d", i)
		}
	}
	last := now.Add(9 * 50 * time.Millisecond)
	if s.rebuild(800, 600, 819, 600, last.Add(resizeSettle-time.Millisecond)) {
		t.Fatal("rebuilt before the size settled")
	}
	if !s.rebuild(800, 600, 819, 600, last.Add(resizeSettle)) {
		t.Fatal("settled size did not rebuild")
	}
	// Returning to the pipeline size cancels a pending change.
	s.rebuild(800, 600, 900, 600, now)
	s.rebuild(800, 600, 800, 600, now)
	if s.rebuild(800, 600, 900, 600, now.Add(resizeSettle)) {
		t.Fatal("a cancelled change kept its old start time")
	}
}
