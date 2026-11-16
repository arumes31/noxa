package speechnames

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"Joshi":                  "Joshi",
		"  Joshi#2 ":             "Joshi",
		"Joshi#M1":               "Joshi",
		"Jo\u200dshi\u0007":      "Joshi",
		"🎮 Gamer 👍🏽":             "Gamer",
		"Anna\tMaria":            "Anna Maria",
		"Ölmützer":               "Ölmützer",
		"🔥🔥":                     "",
		"#2":                     "",
		"--output-filename=/etc": "--output-filename=/etc",
		strings.Repeat("a", 40):  strings.Repeat("a", MaxNameRunes),
		"R2D2":                   "R2D2",
	} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
	if Key("de", "Joshi") == Key("en", "Joshi") {
		t.Fatal("languages share a key")
	}
}

func TestTTSArgsKeepNameAsText(t *testing.T) {
	args := (&Engine{ModelDir: "/m", Style: "/s"}).ttsArgs("de", "--output-filename=/x", "/out.wav")
	if n := len(args); args[n-2] != "--" || args[n-1] != "--output-filename=/x" || !slices.Contains(args, "--output-filename=/out.wav") {
		t.Fatalf("args %q", args)
	}
}

// speech is a valid synthesized clip: 0.1 s silence, 0.5 s tone, silence.
func speech() []float64 {
	samples := make([]float64, SampleRate)
	for i := SampleRate / 10; i < SampleRate*6/10; i++ {
		samples[i] = 0.5 * math.Sin(float64(i)/5)
	}
	return samples
}

type fakeSynth struct {
	calls   atomic.Int32
	release chan struct{}
	err     error
	failFor string // only this text fails with err
}

func (f *fakeSynth) Synthesize(ctx context.Context, _, text string) ([]float64, error) {
	f.calls.Add(1)
	if f.failFor != "" && text != f.failFor {
		return speech(), nil
	}
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return speech(), f.err
}

func TestClipSynthesizesOncePerName(t *testing.T) {
	synth := &fakeSynth{release: make(chan struct{})}
	svc := New(t.TempDir(), synth, zap.NewNop())
	var wg sync.WaitGroup
	results := make(chan []byte, 4)
	for _, nick := range []string{"Joshi", "Joshi#2", "Joshi#M1", "Joshi"} {
		wg.Go(func() {
			_, data, err := svc.Clip(t.Context(), "de", nick, nil)
			if err != nil {
				t.Error(err)
			}
			results <- data
		})
	}
	time.Sleep(50 * time.Millisecond)
	close(synth.release)
	wg.Wait()
	close(results)
	for data := range results {
		if !bytes.HasPrefix(data, []byte("RIFF")) || len(data) < 44 {
			t.Fatalf("clip %d bytes", len(data))
		}
	}
	if _, _, err := svc.Clip(t.Context(), "de", "Joshi", nil); err != nil || synth.calls.Load() != 1 {
		t.Fatalf("calls = %d, err %v", synth.calls.Load(), err)
	}
	// Another language is another clip.
	if _, _, err := svc.Clip(t.Context(), "en", "Joshi", nil); err != nil || synth.calls.Load() != 2 {
		t.Fatalf("calls = %d, err %v", synth.calls.Load(), err)
	}
	if _, _, err := svc.Clip(t.Context(), "de", "🔥", nil); !errors.Is(err, ErrUnspeakable) || synth.calls.Load() != 2 {
		t.Fatalf("unspeakable: %v", err)
	}
}

func TestEngineFailureDisablesService(t *testing.T) {
	svc := New(t.TempDir(), &fakeSynth{err: fmt.Errorf("%w: exec: not found", ErrEngine)}, zap.NewNop())
	if _, _, err := svc.Clip(t.Context(), "de", "Joshi", nil); !errors.Is(err, ErrUnavailable) || svc.Enabled() {
		t.Fatalf("err %v enabled %v", err, svc.Enabled())
	}
	var disabled *Service
	if disabled.Enabled() {
		t.Fatal("nil service enabled")
	}
	// A single bad clip does not turn the feature off, even when the CLI
	// fails on it: the engine still voices a known word.
	for _, synth := range []*fakeSynth{{err: errors.New("odd name"), failFor: "Joshi"}, {err: fmt.Errorf("%w: exit status 134", ErrEngine), failFor: "Joshi"}} {
		svc = New(t.TempDir(), synth, zap.NewNop())
		if _, _, err := svc.Clip(t.Context(), "de", "Joshi", nil); err == nil || !svc.Enabled() {
			t.Fatalf("err %v enabled %v", err, svc.Enabled())
		}
		if _, _, err := svc.Clip(t.Context(), "de", "Mia", nil); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPruneDeletesClipsUnusedFor30Days(t *testing.T) {
	dir := t.TempDir()
	svc := New(dir, &fakeSynth{}, zap.NewNop())
	for _, nick := range []string{"Old", "Fresh", "Touched"} {
		if _, _, err := svc.Clip(t.Context(), "de", nick, nil); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	stale := now.Add(-Retention - time.Hour)
	for _, nick := range []string{"Old", "Touched"} {
		if err := os.Chtimes(svc.path("de", nick), stale, stale); err != nil {
			t.Fatal(err)
		}
	}
	svc.Touch("Touched#2")
	if err := svc.Prune(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	for nick, kept := range map[string]bool{"Old": false, "Fresh": true, "Touched": true} {
		if _, err := os.Stat(svc.path("de", nick)); (err == nil) != kept {
			t.Errorf("%s kept=%v, err %v", nick, kept, err)
		}
	}
	if err := New(filepath.Join(dir, "missing"), nil, zap.NewNop()).Prune(t.Context(), now); err != nil {
		t.Fatal(err)
	}
}

func TestMasterMeetsContract(t *testing.T) {
	data, err := master(speech())
	if err != nil {
		t.Fatal(err)
	}
	pcm := make([]int16, (len(data)-44)/2)
	if err := binary.Read(bytes.NewReader(data[44:]), binary.LittleEndian, pcm); err != nil {
		t.Fatal(err)
	}
	peak, sum := 0.0, 0.0
	for _, v := range pcm {
		f := float64(v) / 32768
		peak, sum = max(peak, math.Abs(f)), sum+f*f
	}
	rms := 20 * math.Log10(math.Sqrt(sum/float64(len(pcm))))
	if pcm[0] != 0 || pcm[len(pcm)-1] != 0 || peak > 0.1151 || rms > -30.9 {
		t.Fatalf("endpoints %d %d, peak %.4f, rms %.1f", pcm[0], pcm[len(pcm)-1], peak, rms)
	}
	if d := float64(len(pcm)) / SampleRate; math.Abs(d-0.6) > 0.01 {
		t.Fatalf("duration %.3f", d)
	}
	if _, err := master(make([]float64, SampleRate)); err == nil {
		t.Fatal("silence accepted")
	}
}

func TestBlendStyleWeightsPresets(t *testing.T) {
	var b bytes.Buffer
	for _, v := range []int64{2, 1, 2, 2, 1, 1} { // 2 presets: ttl 1x2, dp 1x1
		_ = binary.Write(&b, binary.LittleEndian, v)
	}
	_ = binary.Write(&b, binary.LittleEndian, []float32{1, 2, 10, 20, 3, 30})
	style, err := blendStyle(b.Bytes(), map[int]float32{0: 2, 1: -1})
	if err != nil {
		t.Fatal(err)
	}
	got := make([]float32, 3)
	_ = binary.Read(bytes.NewReader(style[48:]), binary.LittleEndian, got)
	if !slices.Equal(got, []float32{-8, -16, -24}) {
		t.Fatalf("style %v", got)
	}
	if _, err := blendStyle(b.Bytes()[:60], st13Blend); err == nil {
		t.Fatal("truncated voice accepted")
	}
}

// With the real model (NOXA_SPEECH_MODEL_DIR) the Go blend must reproduce the
// style every shipped ST13 clip was rendered with.
func TestST13StyleMatchesShippedClips(t *testing.T) {
	dir := os.Getenv("NOXA_SPEECH_MODEL_DIR")
	if dir == "" {
		t.Skip("NOXA_SPEECH_MODEL_DIR not set")
	}
	tts := filepath.Join(t.TempDir(), "tts")
	if err := os.WriteFile(tts, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewEngine(tts, dir, t.TempDir()); err != nil && !strings.Contains(err.Error(), "ffmpeg") {
		t.Fatal(err)
	}
	voice, err := os.ReadFile(filepath.Join(dir, "voice.bin"))
	if err != nil {
		t.Fatal(err)
	}
	style, err := blendStyle(voice, st13Blend)
	if err != nil {
		t.Fatal(err)
	}
	if sum := sha256.Sum256(style); hex.EncodeToString(sum[:]) != st13StyleSHA256 {
		t.Fatal("ST13 style hash differs")
	}
}

// Inside the server image (NOXA_SPEECH_E2E_DIR=/data/...), the real engine
// voices a name with the shipped CLI, model and ffmpeg.
func TestImageEngineSynthesizes(t *testing.T) {
	dir := os.Getenv("NOXA_SPEECH_E2E_DIR")
	if dir == "" {
		t.Skip("NOXA_SPEECH_E2E_DIR not set")
	}
	svc := Open(dir, zap.NewNop())
	if svc == nil {
		t.Fatal("engine unavailable in the image")
	}
	for _, language := range Languages {
		start := time.Now()
		_, data, err := svc.Clip(t.Context(), language, "Joshi#2", nil)
		if err != nil || !bytes.HasPrefix(data, []byte("RIFF")) {
			t.Fatalf("%s clip %d bytes, %v", language, len(data), err)
		}
		t.Logf("%s: %d bytes (%.2f s audio) in %s", language, len(data), float64(len(data)-44)/2/SampleRate, time.Since(start))
	}
}

func TestSynthesisAllowanceOnlyGuardsNewClips(t *testing.T) {
	synth := &fakeSynth{}
	svc := New(t.TempDir(), synth, zap.NewNop())
	deny := func() bool { return false }
	if _, _, err := svc.Clip(t.Context(), "de", "Joshi", deny); !errors.Is(err, ErrRateLimited) || synth.calls.Load() != 0 {
		t.Fatalf("denied synthesis: %v, calls %d", err, synth.calls.Load())
	}
	if _, _, err := svc.Clip(t.Context(), "de", "Joshi", nil); err != nil {
		t.Fatal(err)
	}
	// A stored clip needs no allowance.
	if _, _, err := svc.Clip(t.Context(), "de", "Joshi", deny); err != nil || synth.calls.Load() != 1 {
		t.Fatalf("cached clip: %v, calls %d", err, synth.calls.Load())
	}
}
