package speechnames

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
)

// Mastering contract of tools/generate-speech.py (master_samples) with the
// ST13 settings from provenance.json, so a name clip matches the phrase clip
// it is played with. Names may be shorter than the 0.5 s phrase minimum.
const (
	SampleRate       = 24000
	silenceThreshold = 0.002
	leadingPadding   = 0.040
	trailingPadding  = 0.060
	endpointFade     = 0.006
	targetRMSDBFS    = -31.0
	peakCeiling      = 0.115
	minDuration      = 0.15
	maxDuration      = 4.0
)

var errBadClip = errors.New("speechnames: unusable synthesized clip")

// master trims, removes DC, fades the endpoints and sets the quiet level.
// It returns a 24 kHz mono PCM16 WAV file.
func master(samples []float64) ([]byte, error) {
	first, last := -1, -1
	for i, v := range samples {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, errBadClip
		}
		if math.Abs(v) > silenceThreshold {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first < 0 {
		return nil, errBadClip
	}
	start := max(0, first-int(math.Round(SampleRate*leadingPadding)))
	end := min(len(samples), last+int(math.Round(SampleRate*trailingPadding))+1)
	clip := append([]float64(nil), samples[start:end]...)
	if d := float64(len(clip)) / SampleRate; d < minDuration || d > maxDuration {
		return nil, errBadClip
	}
	mean := 0.0
	for _, v := range clip {
		mean += v
	}
	mean /= float64(len(clip))
	ramp := int(math.Round(SampleRate * endpointFade))
	for i := range clip {
		clip[i] -= mean
		if i < ramp {
			clip[i] *= float64(i) / float64(ramp-1)
		}
		if j := len(clip) - 1 - i; j < ramp {
			clip[i] *= float64(j) / float64(ramp-1)
		}
	}
	sum, peak := 0.0, 0.0
	for _, v := range clip {
		sum += v * v
		peak = max(peak, math.Abs(v))
	}
	rms := math.Sqrt(sum / float64(len(clip)))
	if rms <= 0 || peak <= 0 {
		return nil, errBadClip
	}
	gain := min(math.Pow(10, targetRMSDBFS/20)/rms, peakCeiling/peak)
	var out bytes.Buffer
	header := struct {
		Riff     [4]byte
		Size     uint32
		Wave     [4]byte
		Fmt      [4]byte
		FmtSize  uint32
		Format   uint16
		Channels uint16
		Rate     uint32
		Bytes    uint32
		Align    uint16
		Bits     uint16
		Data     [4]byte
		DataSize uint32
	}{[4]byte{'R', 'I', 'F', 'F'}, uint32(36 + 2*len(clip)), [4]byte{'W', 'A', 'V', 'E'}, [4]byte{'f', 'm', 't', ' '}, 16, 1, 1,
		SampleRate, SampleRate * 2, 2, 16, [4]byte{'d', 'a', 't', 'a'}, uint32(2 * len(clip))}
	_ = binary.Write(&out, binary.LittleEndian, header)
	for _, v := range clip {
		_ = binary.Write(&out, binary.LittleEndian, int16(math.Round(v*gain*32767)))
	}
	return out.Bytes(), nil
}
