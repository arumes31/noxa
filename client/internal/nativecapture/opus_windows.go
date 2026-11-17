//go:build windows

package nativecapture

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The reference libopus (tools/libopus/build.sh) is embedded, so the signed
// exe and its updater patches carry it. It is extracted per user, and the
// file is hashed against the committed pin while a share-read-only handle
// keeps it unchanged until LoadLibraryEx has mapped it by absolute path.
//
//go:embed libopus
var libopusFS embed.FS

type opusLib struct {
	dll                          *windows.DLL
	create, encode, ctl, destroy *windows.Proc
}

var (
	opusOnce sync.Once
	opus     *opusLib
	opusErr  error
)

func loadOpus() (*opusLib, error) {
	opusOnce.Do(func() { opus, opusErr = loadOpusDLL() })
	return opus, opusErr
}

func loadOpusDLL() (*opusLib, error) {
	dll, err := libopusFS.ReadFile("libopus/opus.dll")
	if err != nil {
		return nil, fmt.Errorf("%w: not bundled", ErrOpusUnavailable)
	}
	pin, err := libopusFS.ReadFile("libopus/opus.dll.sha256")
	if err != nil {
		return nil, fmt.Errorf("%w: no pin", ErrOpusUnavailable)
	}
	want := strings.Fields(string(pin))
	sum := sha256.Sum256(dll)
	hash := hex.EncodeToString(sum[:])
	if len(want) == 0 || want[0] != hash {
		return nil, fmt.Errorf("%w: bundled DLL does not match its pin", ErrOpusUnavailable)
	}
	base, err := os.UserCacheDir() // %LocalAppData%
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrOpusUnavailable, err)
	}
	dir := filepath.Join(base, "noxa", "native")
	path := filepath.Join(dir, "opus-"+hash[:16]+".dll")
	h, err := openVerified(path, sum[:])
	if err != nil {
		// Missing or tampered: rewrite it, then verify again.
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrOpusUnavailable, err)
		}
		tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
		if err := os.WriteFile(tmp, dll, 0o600); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrOpusUnavailable, err)
		}
		if err := os.Rename(tmp, path); err != nil {
			_ = os.Remove(tmp)
			return nil, fmt.Errorf("%w: %v", ErrOpusUnavailable, err)
		}
		if h, err = openVerified(path, sum[:]); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrOpusUnavailable, err)
		}
	}
	defer func() { _ = windows.CloseHandle(h) }()
	// Absolute path; its imports (KERNEL32, msvcrt) resolve from System32 only.
	module, err := windows.LoadLibraryEx(path, 0, windows.LOAD_LIBRARY_SEARCH_SYSTEM32)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrOpusUnavailable, err)
	}
	d := &windows.DLL{Name: path, Handle: module}
	lib := &opusLib{dll: d}
	for name, p := range map[string]**windows.Proc{"opus_encoder_create": &lib.create, "opus_encode": &lib.encode,
		"opus_encoder_ctl": &lib.ctl, "opus_encoder_destroy": &lib.destroy} {
		if *p, err = d.FindProc(name); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrOpusUnavailable, err)
		}
	}
	return lib, nil
}

// openVerified opens path denying writers and checks its SHA-256.
func openVerified(path string, sum []byte) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return 0, err
	}
	digest := sha256.New()
	buf := make([]byte, 64<<10)
	for {
		var n uint32
		if err = windows.ReadFile(h, buf, &n, nil); err != nil || n == 0 {
			break
		}
		digest.Write(buf[:n])
	}
	if err != nil || !bytes.Equal(digest.Sum(nil), sum) {
		_ = windows.CloseHandle(h)
		return 0, errors.New("libopus hash mismatch")
	}
	return h, nil
}

// libopus constants (opus_defines.h).
const (
	opusApplicationAudio = 2049
	opusSetBitrate       = 4002
	opusSetVBR           = 4006
	opusSetComplexity    = 4010
	opusSetInbandFEC     = 4012
	opusSetPacketLoss    = 4014
	opusSetDTX           = 4016
	opusSetSignal        = 4024
	opusSignalMusic      = 3002
)

// opusEncoder is one libopus encoder: 48 kHz stereo, 20 ms frames, 128 kbps
// VBR, in-band FEC on (used by libopus only in SILK/hybrid modes, as for the
// webview's Opus), DTX off: receivers drop empty frames and screen audio is
// continuous.
type opusEncoder struct {
	lib *opusLib
	st  uintptr
	out []byte
}

func newOpusEncoder(bitrate int) (*opusEncoder, error) {
	lib, err := loadOpus()
	if err != nil {
		return nil, err
	}
	var code int32
	st, _, _ := lib.create.Call(AudioRate, AudioChannels, opusApplicationAudio, uintptr(unsafe.Pointer(&code))) // #nosec G103
	if st == 0 || code != 0 {
		return nil, fmt.Errorf("opus_encoder_create: %d", code)
	}
	e := &opusEncoder{lib: lib, st: st, out: make([]byte, 4000)}
	for _, c := range [][2]int{{opusSetBitrate, bitrate}, {opusSetVBR, 1}, {opusSetComplexity, 9}, {opusSetInbandFEC, 1},
		{opusSetPacketLoss, 5}, {opusSetDTX, 0}, {opusSetSignal, opusSignalMusic}} {
		if r, _, _ := lib.ctl.Call(st, uintptr(c[0]), uintptr(c[1])); int32(r) != 0 {
			e.close()
			return nil, fmt.Errorf("opus_encoder_ctl %d: %d", c[0], int32(r))
		}
	}
	return e, nil
}

// encode encodes one 20 ms interleaved stereo frame; the result is reused.
func (e *opusEncoder) encode(pcm []int16) ([]byte, error) {
	if len(pcm) != AudioFrame*AudioChannels {
		return nil, errors.New("opus: wrong frame size")
	}
	// #nosec G103 -- libopus reads pcm and writes at most len(out) bytes.
	n, _, _ := syscall.SyscallN(e.lib.encode.Addr(), e.st, uintptr(unsafe.Pointer(&pcm[0])), AudioFrame, uintptr(unsafe.Pointer(&e.out[0])), uintptr(len(e.out)))
	if int32(n) <= 0 {
		return nil, fmt.Errorf("opus_encode: %d", int32(n))
	}
	return e.out[:int32(n)], nil
}

func (e *opusEncoder) close() {
	if e.st != 0 {
		_, _, _ = e.lib.destroy.Call(e.st)
		e.st = 0
	}
}
