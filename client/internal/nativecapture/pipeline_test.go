package nativecapture

import "testing"

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
