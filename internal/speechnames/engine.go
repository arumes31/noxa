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
	"os/exec"
	"path/filepath"
)

// Image layout (Dockerfile): the sherpa-onnx CLI behind a wrapper that runs it
// with its private glibc, and the pinned Supertonic 3 int8 model files.
const (
	DefaultTTS      = "/opt/noxa-speech/tts"
	DefaultModelDir = "/opt/noxa-speech/model"
)

// ST13 "Eisbrecher" (tools/voices/ST13.json): preset blend and settings. The
// style hash is the one every shipped ST13 clip records in metrics.json.
const st13StyleSHA256 = "e0dc430684ef97e15ad22e781c0d9c26a131f5f1a42c0c89ccf6a6501cab985e"

var st13Blend = map[int]float32{9: 1.25, 5: -0.25} // preset_order index: M5, M1

// ErrEngine marks a failure of the engine itself (missing, crashing), as
// opposed to one name that produced unusable audio.
var ErrEngine = errors.New("speechnames: speech engine failed")

// Engine runs sherpa-onnx-offline-tts and ffmpeg as subprocesses.
type Engine struct {
	TTS      string
	ModelDir string
	Style    string // blended ST13 style file, written by NewEngine
	FFmpeg   string
	style    []byte
}

// NewEngine checks the engine files and writes the ST13 style into workDir.
func NewEngine(tts, modelDir, workDir string) (*Engine, error) {
	if _, err := os.Stat(tts); err != nil {
		return nil, err
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, err
	}
	voice, err := os.ReadFile(filepath.Join(modelDir, "voice.bin"))
	if err != nil {
		return nil, err
	}
	style, err := blendStyle(voice, st13Blend)
	if err != nil {
		return nil, err
	}
	if sum := sha256.Sum256(style); hex.EncodeToString(sum[:]) != st13StyleSHA256 {
		return nil, fmt.Errorf("speechnames: ST13 style does not match the pinned voice")
	}
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(workDir, "st13-style.bin")
	if err := os.WriteFile(path, style, 0o600); err != nil {
		return nil, err
	}
	return &Engine{TTS: tts, ModelDir: modelDir, Style: path, FFmpeg: ffmpeg, style: style}, nil
}

// blendStyle is blended_style of tools/generate-supertonic-speech.py: the
// weighted sum of preset style vectors in voice.bin, in float32.
func blendStyle(voice []byte, weights map[int]float32) ([]byte, error) {
	if len(voice) < 48 {
		return nil, errors.New("speechnames: short voice.bin")
	}
	dims := func(at int) (n [3]int64, size int64) {
		size = 1
		for i := range n {
			n[i] = int64(binary.LittleEndian.Uint64(voice[at+8*i:]))
			size *= n[i]
		}
		return n, size
	}
	ttlDims, ttlSize := dims(0)
	dpDims, dpSize := dims(24)
	if ttlDims[0] < 1 || dpDims[0] != ttlDims[0] || ttlSize <= 0 || dpSize <= 0 || int64(len(voice)) < 48+4*(ttlSize+dpSize) {
		return nil, errors.New("speechnames: malformed voice.bin")
	}
	sum := func(offset, size, presets int64) []float32 {
		per := size / presets
		out := make([]float32, per)
		for preset, weight := range weights {
			for i := range out {
				at := offset + 4*(int64(preset)*per+int64(i))
				// Explicit float32 rounding of each product, as numpy does;
				// a fused multiply-add would change the pinned hash.
				product := float32(math.Float32frombits(binary.LittleEndian.Uint32(voice[at:])) * weight)
				out[i] = float32(out[i] + product)
			}
		}
		return out
	}
	var b bytes.Buffer
	for _, v := range []int64{1, ttlDims[1], ttlDims[2], 1, dpDims[1], dpDims[2]} {
		_ = binary.Write(&b, binary.LittleEndian, v)
	}
	_ = binary.Write(&b, binary.LittleEndian, sum(48, ttlSize, ttlDims[0]))
	_ = binary.Write(&b, binary.LittleEndian, sum(48+4*ttlSize, dpSize, ttlDims[0]))
	return b.Bytes(), nil
}

// ttsArgs ends options with "--": a nickname such as "--output-filename=x"
// must stay spoken text.
func (e *Engine) ttsArgs(language, text, output string) []string {
	m := e.ModelDir
	return []string{
		"--supertonic-duration-predictor=" + m + "/duration_predictor.int8.onnx",
		"--supertonic-text-encoder=" + m + "/text_encoder.int8.onnx",
		"--supertonic-vector-estimator=" + m + "/vector_estimator.int8.onnx",
		"--supertonic-vocoder=" + m + "/vocoder.int8.onnx",
		"--supertonic-tts-json=" + m + "/tts.json",
		"--supertonic-unicode-indexer=" + m + "/unicode_indexer.bin",
		"--supertonic-voice-style=" + e.Style, "--sid=0", "--lang=" + language,
		"--speed=0.97", "--num-steps=8", "--num-threads=2", "--output-filename=" + output, "--", text,
	}
}

// Synthesize renders text and returns 24 kHz mono samples.
func (e *Engine) Synthesize(ctx context.Context, language, text string) ([]float64, error) {
	if _, err := os.Stat(e.Style); err != nil && e.style != nil {
		_ = os.WriteFile(e.Style, e.style, 0o600) // the data volume was cleaned under us
	}
	raw, err := os.CreateTemp(filepath.Dir(e.Style), "raw-*.wav")
	if err != nil {
		return nil, err
	}
	_ = raw.Close()
	defer func() { _ = os.Remove(raw.Name()) }()
	if out, err := exec.CommandContext(ctx, e.TTS, e.ttsArgs(language, text, raw.Name())...).CombinedOutput(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %v: %s", ErrEngine, err, tail(out))
	}
	pcm, err := exec.CommandContext(ctx, e.FFmpeg, "-nostdin", "-v", "error", "-i", raw.Name(),
		"-ac", "1", "-ar", fmt.Sprint(SampleRate), "-f", "f32le", "-").Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: ffmpeg: %v", ErrEngine, err)
	}
	samples := make([]float64, len(pcm)/4)
	for i := range samples {
		samples[i] = float64(math.Float32frombits(binary.LittleEndian.Uint32(pcm[4*i:])))
	}
	return samples, nil
}

func tail(out []byte) []byte {
	return out[max(0, len(out)-200):]
}
