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
