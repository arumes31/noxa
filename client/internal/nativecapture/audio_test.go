package nativecapture

import (
	"slices"
	"testing"
	"time"
)

func TestAudioPlan(t *testing.T) {
	cases := map[[2]string][]AudioMode{
		{"application", "window"}:  {AudioApplication, AudioSystem},
		{"application", "monitor"}: {AudioSystem},
		{"system", "window"}:       {AudioSystem},
		{"system", "monitor"}:      {AudioSystem},
		{"none", "window"}:         nil,
		{"", "monitor"}:            nil,
		{"bogus", "window"}:        nil,
	}
	for in, want := range cases {
		if got := AudioPlan(in[0], in[1] == "window"); !slices.Equal(got, want) {
			t.Errorf("AudioPlan(%q, %s) = %v, want %v", in[0], in[1], got, want)
		}
	}
}

// TestAudioFramer cuts WASAPI packets of any size into 20 ms frames dated by
// their first sample, and restarts on gaps and discontinuities.
func TestAudioFramer(t *testing.T) {
	var f audioFramer
	type frame struct {
		first int16
		at    time.Time
	}
	var got []frame
	emit := func(pcm []int16, at time.Time) {
		if len(pcm) != AudioFrame*AudioChannels {
			t.Fatalf("frame of %d samples", len(pcm))
		}
		got = append(got, frame{pcm[0], at})
	}
	t0 := time.Unix(100, 0)
	packet := func(n, from int) []int16 { // n stereo samples numbered from
		pcm := make([]int16, 2*n)
		for i := range n {
			pcm[2*i], pcm[2*i+1] = int16(from+i), int16(from+i)
		}
		return pcm
	}
	at := func(sample int) time.Time { return t0.Add(time.Duration(sample) * time.Second / AudioRate) }
	// 10 ms packets (480 samples) plus an odd 7 ms one.
	for i, n := 0, 0; i < 5; i++ {
		size := 480
		if i == 2 {
			size = 336
		}
		f.push(packet(size, n), at(n), false, emit)
		n += size
	}
	if len(got) != 2 || got[0].first != 0 || !got[0].at.Equal(t0) || got[1].first != 960 || !got[1].at.Equal(at(960)) {
		t.Fatalf("contiguous frames: %+v", got)
	}
	// A gap of 100 ms drops the partial frame and dates the next one by its packet.
	got = nil
	f.push(packet(960, 10000), at(10000), false, emit)
	if len(got) != 1 || got[0].first != 10000 || !got[0].at.Equal(at(10000)) {
		t.Fatalf("after gap: %+v", got)
	}
	// A discontinuity flag restarts even without a time gap.
	got = nil
	f.push(packet(480, 10960), at(10960), false, emit)
	f.push(packet(960, 20000), at(11440), true, emit)
	if len(got) != 1 || got[0].first != 20000 || !got[0].at.Equal(at(11440)) {
		t.Fatalf("after discontinuity: %+v", got)
	}
}
