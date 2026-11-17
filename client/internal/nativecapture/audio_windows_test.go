//go:build windows

package nativecapture

import (
	"encoding/binary"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"
)

// opusDecoder decodes test output with the same pinned libopus.
type opusDecoder struct {
	lib *opusLib
	st  uintptr
}

func newTestDecoder(t *testing.T) *opusDecoder {
	t.Helper()
	lib := requireOpus(t)
	create, err := lib.dll.FindProc("opus_decoder_create")
	if err != nil {
		t.Fatal(err)
	}
	var code int32
	st, _, _ := create.Call(AudioRate, AudioChannels, uintptr(unsafe.Pointer(&code)))
	if st == 0 || code != 0 {
		t.Fatalf("opus_decoder_create: %d", code)
	}
	t.Cleanup(func() {
		if destroy, err := lib.dll.FindProc("opus_decoder_destroy"); err == nil {
			_, _, _ = destroy.Call(st)
		}
	})
	return &opusDecoder{lib: lib, st: st}
}

func (d *opusDecoder) decode(t *testing.T, packet []byte) []int16 {
	decode, err := d.lib.dll.FindProc("opus_decode")
	if err != nil {
		t.Fatal(err)
	}
	pcm := make([]int16, AudioFrame*AudioChannels*6)
	n, _, _ := decode.Call(d.st, uintptr(unsafe.Pointer(&packet[0])), uintptr(len(packet)), uintptr(unsafe.Pointer(&pcm[0])), uintptr(len(pcm)/AudioChannels), 0)
	if int32(n) < 0 {
		t.Fatalf("opus_decode: %d", int32(n))
	}
	return pcm[:int(int32(n))*AudioChannels]
}

func requireOpus(t *testing.T) *opusLib {
	t.Helper()
	lib, err := loadOpus()
	if err != nil {
		if errors.Is(err, ErrOpusUnavailable) && os.Getenv("NOXA_REQUIRE_OPUS") == "" {
			t.Skipf("libopus not bundled in this build: %v", err)
		}
		t.Fatal(err)
	}
	return lib
}

func rms(pcm []int16) float64 {
	var sum float64
	for _, s := range pcm {
		sum += float64(s) * float64(s)
	}
	return math.Sqrt(sum / float64(max(len(pcm), 1)))
}

func sine(n int, hz float64, from int) []int16 {
	pcm := make([]int16, n*AudioChannels)
	for i := range n {
		v := int16(8000 * math.Sin(2*math.Pi*hz*float64(from+i)/AudioRate))
		pcm[2*i], pcm[2*i+1] = v, v
	}
	return pcm
}

// TestOpusEncoderRoundTrip encodes a 1 kHz tone with the bundled libopus and
// decodes it back: 20 ms packets near 128 kbps that keep the tone's energy.
func TestOpusEncoderRoundTrip(t *testing.T) {
	enc, err := newOpusEncoder(AudioBitrate)
	if err != nil {
		requireOpus(t)
		t.Fatal(err)
	}
	defer enc.close()
	dec := newTestDecoder(t)
	var bytes int
	var energy float64
	for i := range 100 {
		packet, err := enc.encode(sine(AudioFrame, 1000, i*AudioFrame))
		if err != nil {
			t.Fatal(err)
		}
		bytes += len(packet)
		if toc := packet[0] >> 3; toc < 28 { // CELT fullband configs are 28..31
			t.Logf("packet %d TOC config %d", i, toc)
		}
		pcm := dec.decode(t, packet)
		if len(pcm) != AudioFrame*AudioChannels {
			t.Fatalf("decoded %d samples", len(pcm))
		}
		if i >= 10 {
			energy += rms(pcm)
		}
	}
	kbps := float64(bytes*8) / 2 / 1000
	t.Logf("tone: %.0f kbps, decoded rms %.0f (input 5657)", kbps, energy/90)
	if kbps < 40 || kbps > 200 || energy/90 < 4000 {
		t.Fatalf("unexpected encode result: %.0f kbps, rms %.0f", kbps, energy/90)
	}
}

func TestAudioPlanFallbacks(t *testing.T) {
	if got := AudioPlan("application", true); len(got) != 2 || got[0] != AudioApplication || got[1] != AudioSystem {
		t.Fatalf("application plan %v", got)
	}
}

// writeToneWAV writes a 440 Hz 16-bit stereo WAV of the given length.
func writeToneWAV(t *testing.T, seconds int) string {
	t.Helper()
	pcm := sine(AudioRate*seconds, 440, 0)
	data := make([]byte, 44+len(pcm)*2)
	copy(data, "RIFF")
	binary.LittleEndian.PutUint32(data[4:], uint32(36+len(pcm)*2))
	copy(data[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(data[16:], 16)
	binary.LittleEndian.PutUint16(data[20:], 1)
	binary.LittleEndian.PutUint16(data[22:], AudioChannels)
	binary.LittleEndian.PutUint32(data[24:], AudioRate)
	binary.LittleEndian.PutUint32(data[28:], AudioRate*4)
	binary.LittleEndian.PutUint16(data[32:], 4)
	binary.LittleEndian.PutUint16(data[34:], 16)
	copy(data[36:], "data")
	binary.LittleEndian.PutUint32(data[40:], uint32(len(pcm)*2))
	for i, s := range pcm {
		binary.LittleEndian.PutUint16(data[44+2*i:], uint16(s))
	}
	path := filepath.Join(t.TempDir(), "tone.wav")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// playTone starts cmd.exe -> powershell.exe playing wav: the tone comes from
// a grandchild of the test process, like WebView2's audio service under noXa.
func playTone(t *testing.T, wav string) *exec.Cmd {
	t.Helper()
	script := "(New-Object Media.SoundPlayer '" + strings.ReplaceAll(wav, "'", "''") + "').PlaySync()"
	cmd := exec.Command("cmd.exe", "/c", "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd
}

// captureRMS records ~2 s of loopback audio and returns the decoded RMS of
// the second half and the packet count.
func captureRMS(t *testing.T, pid uint32, loopback int32) (float64, int) {
	t.Helper()
	dec := newTestDecoder(t)
	var mu sync.Mutex
	var packets []AudioPacket
	s, err := startLoopback(AudioSystem, pid, loopback, func(p AudioPacket) {
		mu.Lock()
		packets = append(packets, p)
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2500 * time.Millisecond)
	s.Close()
	mu.Lock()
	defer mu.Unlock()
	var energy float64
	n := 0
	for i, p := range packets {
		pcm := dec.decode(t, p.Data)
		if i >= len(packets)/2 {
			energy += rms(pcm)
			n++
		}
		if i > 0 && !p.Captured.After(packets[i-1].Captured) {
			t.Errorf("packet %d capture time not increasing", i)
		}
	}
	return energy / float64(max(n, 1)), len(packets)
}

// TestProcessLoopbackTreeDepth plays a tone in a grandchild (test -> cmd ->
// powershell), the shape of noXa -> WebView2 browser -> audio service.
// INCLUDE of the middle process hears it; EXCLUDE of the middle process (what
// selfAudioRoot picks for WebView2) removes it while system capture still
// hears it; EXCLUDE/INCLUDE of the grandparent shows that the loopback "tree"
// stops at direct children. NOXA_NATIVE_CAPTURE=1 on a desktop with an audio
// endpoint enables it.
func TestProcessLoopbackTreeDepth(t *testing.T) {
	if os.Getenv("NOXA_NATIVE_CAPTURE") == "" {
		t.Skip("set NOXA_NATIVE_CAPTURE=1 on an interactive Windows desktop with audio output")
	}
	requireOpus(t)
	if !processLoopbackSupported() {
		t.Skip("process loopback needs Windows 10 2004+")
	}
	cmd := playTone(t, writeToneWAV(t, 40))
	time.Sleep(1500 * time.Millisecond) // powershell start-up
	self := uint32(os.Getpid())         // #nosec G115
	middle := childProcess(self, "cmd.exe")
	if middle != uint32(cmd.Process.Pid) { // #nosec G115
		t.Fatalf("childProcess found %d, want cmd.exe %d", middle, cmd.Process.Pid)
	}
	start := time.Now()
	include, n := captureRMS(t, middle, processLoopbackInclude)
	t.Logf("include middle: rms %.0f over %d packets (%.1f packets/s)", include, n, float64(n)/time.Since(start).Seconds())
	exclude, _ := captureRMS(t, middle, processLoopbackExclude)
	t.Logf("exclude middle: rms %.0f", exclude)
	system, _ := captureRMS(t, 4, processLoopbackExclude) // System never plays audio
	t.Logf("exclude unrelated process: rms %.0f", system)
	grand, _ := captureRMS(t, self, processLoopbackExclude)
	t.Logf("exclude grandparent: rms %.0f (non-zero: the tree stops at direct children)", grand)
	if include < 1000 || system < 1000 {
		t.Fatalf("tone not captured: include %.0f, system %.0f", include, system)
	}
	if exclude > include/20 {
		t.Fatalf("excluded tree still audible: rms %.0f vs tone %.0f", exclude, include)
	}
}
