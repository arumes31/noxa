//go:build windows

package nativecapture

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// WASAPI process loopback (audioclientactivationparams.h, audioclient.h):
// ActivateAudioInterfaceAsync on the virtual process-loopback device with
// AUDIOCLIENT_ACTIVATION_TYPE_PROCESS_LOOPBACK. INCLUDE captures the target
// "process tree" only (a shared window's application); EXCLUDE captures every
// process except it. Measured on Windows 11 (TestProcessLoopbackTreeDepth):
// the tree is the target and its DIRECT children only, not grandchildren.
// noXa's voices play in WebView2's audio-service process, a child of the
// WebView2 browser process, which is a child of noxa.exe: so system audio
// excludes the WebView2 browser process (see selfAudioRoot).

var (
	mmdevapi                        = windows.NewLazySystemDLL("mmdevapi.dll")
	procActivateAudioInterfaceAsync = mmdevapi.NewProc("ActivateAudioInterfaceAsync")
	procGetWindowThreadProcessId    = user32.NewProc("GetWindowThreadProcessId")
	procQueryPerformanceCounter     = windows.NewLazySystemDLL("kernel32.dll").NewProc("QueryPerformanceCounter")
	procQueryPerformanceFrequency   = windows.NewLazySystemDLL("kernel32.dll").NewProc("QueryPerformanceFrequency")

	iidIAudioClient        = mustGUID("1CB9AD4C-DBFA-4c32-B178-C2F568A703B2")
	iidIAudioCaptureClient = mustGUID("C8ADBD64-E71E-48a0-A4DE-185C395CD317")
	iidIUnknown            = mustGUID("00000000-0000-0000-C000-000000000046")
	iidIAgileObject        = mustGUID("94ea2b94-e9cc-49e0-c0ff-ee64ca8f5b90")
	iidActivateHandler     = mustGUID("41D949AB-9862-444A-80F6-C261334DA5EB")
)

const (
	processLoopbackInclude = 0
	processLoopbackExclude = 1
	audclntStreamLoopback  = 0x00020000
	audclntStreamEvent     = 0x00040000
	audclntAutoConvertPCM  = 0x80000000
	audclntSrcDefault      = 0x08000000
	audclntSilent          = 0x2
	audclntDiscontinuity   = 0x1
	audclntBufferEmpty     = hresult(0x08890001) // AUDCLNT_S_BUFFER_EMPTY: success, no data
)

// Each activation gets its own Go COM completion handler (IUnknown +
// IAgileObject + IActivateAudioInterfaceCompletionHandler) with its own
// signal: a late completion of a timed-out activation can never wake the
// next one. A handler stays registered (alive for the GC) until Windows has
// called it.
type comObject struct {
	vtbl *[4]uintptr
	done chan struct{}
}

var (
	activateVtbl     = [4]uintptr{windows.NewCallback(handlerQI), windows.NewCallback(handlerRef), windows.NewCallback(handlerRef), windows.NewCallback(handlerCompleted)}
	activateHandlers sync.Map // uintptr(handler) -> *comObject
)

func handlerQI(this, riid, out uintptr) uintptr {
	iid := *(*windows.GUID)(unsafe.Pointer(riid)) //nolint:govet // #nosec G103 -- REFIID from COM
	if iid == iidIUnknown || iid == iidIAgileObject || iid == iidActivateHandler {
		*(*uintptr)(unsafe.Pointer(out)) = this //nolint:govet // #nosec G103 -- COM out pointer
		return 0
	}
	*(*uintptr)(unsafe.Pointer(out)) = 0 //nolint:govet // #nosec G103 -- COM out pointer
	return 0x80004002                    // E_NOINTERFACE
}

func handlerRef(uintptr) uintptr { return 1 } // lifetime is the registry's, not COM's

func handlerCompleted(this, _ uintptr) uintptr {
	if h, ok := activateHandlers.LoadAndDelete(this); ok {
		close(h.(*comObject).done)
	}
	return 0
}

// processLoopbackSupported: process-loopback activation works from build
// 20348 (Windows Server 2022 / Windows 11); older builds reject it.
func processLoopbackSupported() bool {
	return windows.RtlGetVersion().BuildNumber >= 20348 && procActivateAudioInterfaceAsync.Find() == nil
}

// activateProcessLoopback returns an IAudioClient for pid's tree (include or exclude).
func activateProcessLoopback(pid uint32, mode int32) (uintptr, error) {
	if !processLoopbackSupported() {
		return 0, ErrAudioUnsupported
	}
	handler := &comObject{vtbl: &activateVtbl, done: make(chan struct{})}
	key := uintptr(unsafe.Pointer(handler)) // #nosec G103 -- the registry keeps handler alive
	activateHandlers.Store(key, handler)
	params := struct {
		ActivationType int32 // AUDIOCLIENT_ACTIVATION_TYPE_PROCESS_LOOPBACK
		TargetPID      uint32
		Mode           int32
	}{1, pid, mode}
	prop := struct { // PROPVARIANT with VT_BLOB
		vt       uint16
		_        [3]uint16
		cbSize   uint32
		_        uint32
		blobData unsafe.Pointer // a tracked pointer: params may not move before Windows reads it
	}{vt: 65, cbSize: uint32(unsafe.Sizeof(params)), blobData: unsafe.Pointer(&params)} // #nosec G103
	path, _ := windows.UTF16PtrFromString(`VAD\Process_Loopback`)
	var op uintptr
	// #nosec G103 -- arguments live until the completion below.
	r, _, _ := procActivateAudioInterfaceAsync.Call(uintptr(unsafe.Pointer(path)), uintptr(unsafe.Pointer(&iidIAudioClient)),
		uintptr(unsafe.Pointer(&prop)), key, uintptr(unsafe.Pointer(&op)))
	runtime.KeepAlive(&params)
	if hresult(r).failed() {
		activateHandlers.Delete(key) // no callback follows a failed call
		return 0, hresult(r).err("ActivateAudioInterfaceAsync")
	}
	defer release(op)
	select {
	case <-handler.done:
	case <-time.After(5 * time.Second):
		return 0, errors.New("process loopback activation timed out")
	}
	runtime.KeepAlive(&prop)
	var result hresult
	var client uintptr
	// IActivateAudioInterfaceAsyncOperation::GetActivateResult
	if hr := vcall(op, 3, uintptr(unsafe.Pointer(&result)), uintptr(unsafe.Pointer(&client))); hr.failed() { // #nosec G103
		return 0, hr.err("GetActivateResult")
	}
	if result.failed() {
		release(client)
		return 0, result.err("process loopback activation")
	}
	return client, nil
}

// AudioSession captures loopback audio and emits encoded Opus packets.
type AudioSession struct {
	Mode AudioMode
	stop chan struct{}
	done chan struct{}
	fail chan error
	once sync.Once
}

// Failed reports a capture that broke for good (e.g. the audio service
// restarted and reopening failed); the session is then silent.
func (s *AudioSession) Failed() <-chan error { return s.fail }

// StartAudio opens the loopback for mode (window = the shared window for
// AudioApplication) and the Opus encoder, then runs until Close.
func StartAudio(mode AudioMode, window uint64, out func(AudioPacket)) (*AudioSession, error) {
	var pid uint32
	loopback := int32(processLoopbackExclude)
	switch mode {
	case AudioApplication:
		_, _, _ = procGetWindowThreadProcessId.Call(uintptr(window), uintptr(unsafe.Pointer(&pid))) // #nosec G103
		if pid == 0 {
			return nil, errors.New("shared window has no process")
		}
		loopback = processLoopbackInclude
	case AudioSystem:
		pid = selfAudioRoot()
	default:
		return nil, fmt.Errorf("unknown audio mode %q", mode)
	}
	return startLoopback(mode, pid, loopback, out)
}

// selfAudioRoot is the process whose tree holds noXa's audio output: the
// WebView2 browser process (its audio service is a direct child), else noXa.
// shortcut: the first WebView2 browser child wins; noXa runs one webview
// environment. Exclude a list of roots if that ever changes.
func selfAudioRoot() uint32 {
	self := uint32(os.Getpid()) // #nosec G115 -- Windows process IDs are 32-bit
	if child := childProcess(self, "msedgewebview2.exe"); child != 0 {
		return child
	}
	return self
}

// childProcess returns the first direct child of parent running exe, or 0.
func childProcess(parent uint32, exe string) uint32 {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0
	}
	defer func() { _ = windows.CloseHandle(snap) }()
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err = windows.Process32First(snap, &entry); err == nil; err = windows.Process32Next(snap, &entry) {
		if entry.ParentProcessID == parent && strings.EqualFold(windows.UTF16ToString(entry.ExeFile[:]), exe) {
			return entry.ProcessID
		}
	}
	return 0
}

func startLoopback(mode AudioMode, pid uint32, loopback int32, out func(AudioPacket)) (*AudioSession, error) {
	enc, err := newOpusEncoder(AudioBitrate)
	if err != nil {
		return nil, err
	}
	s := &AudioSession{Mode: mode, stop: make(chan struct{}), done: make(chan struct{}), fail: make(chan error, 1)}
	ready := make(chan error, 1)
	go s.run(pid, loopback, enc, out, ready)
	if err := <-ready; err != nil {
		return nil, err
	}
	return s, nil
}

// Close stops capture and waits for the capture thread.
func (s *AudioSession) Close() {
	if s.stop == nil {
		return // never started
	}
	s.once.Do(func() { close(s.stop) })
	<-s.done
}

// audioReopenAfter: a capture that ran at least this long is reopened once
// after a terminal error; one that fails sooner is given up.
const audioReopenAfter = 5 * time.Second

func (s *AudioSession) run(pid uint32, loopback int32, enc *opusEncoder, out func(AudioPacket), ready chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(s.done)
	defer enc.close()
	initThread()
	emit := func(pcm []int16, at time.Time) {
		packet, err := enc.encode(pcm)
		if err != nil {
			return
		}
		out(AudioPacket{Data: append([]byte(nil), packet...), Captured: at})
	}
	started, running := time.Now(), false
	err := s.capture(pid, loopback, emit, func() { running = true; ready <- nil })
	if !running {
		ready <- err // setup failed: StartAudio returns it
		return
	}
	for err != nil && !s.stopped() {
		if time.Since(started) < audioReopenAfter {
			s.fail <- err
			return
		}
		select {
		case <-s.stop:
			return
		case <-time.After(500 * time.Millisecond):
		}
		started = time.Now()
		err = s.capture(pid, loopback, emit, func() {})
	}
}

func (s *AudioSession) stopped() bool {
	select {
	case <-s.stop:
		return true
	default:
		return false
	}
}

// capture opens one loopback client and runs it until Close (nil) or a
// terminal WASAPI error; running is called once capture has started.
func (s *AudioSession) capture(pid uint32, loopback int32, emit func([]int16, time.Time), running func()) error {
	client, err := activateProcessLoopback(pid, loopback)
	if err != nil {
		return err
	}
	defer release(client)
	format := struct {
		Tag, Channels  uint16
		Rate, AvgBytes uint32
		Align, Bits    uint16
		Size           uint16
	}{1, AudioChannels, AudioRate, AudioRate * AudioChannels * 2, AudioChannels * 2, 16, 0} // WAVE_FORMAT_PCM
	// IAudioClient::Initialize (shared, 100 ms buffer); process loopback has
	// no mix format, so the engine converts to this PCM format.
	if hr := vcall(client, 3, 0, audclntStreamLoopback|audclntStreamEvent|audclntAutoConvertPCM|audclntSrcDefault,
		1_000_000, 0, uintptr(unsafe.Pointer(&format)), 0); hr.failed() { // #nosec G103
		return hr.err("IAudioClient.Initialize")
	}
	event, err := windows.CreateEvent(nil, 0, 0, nil)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(event) }()
	if hr := vcall(client, 13, uintptr(event)); hr.failed() {
		return hr.err("IAudioClient.SetEventHandle")
	}
	var capture uintptr
	if hr := vcall(client, 14, uintptr(unsafe.Pointer(&iidIAudioCaptureClient)), uintptr(unsafe.Pointer(&capture))); hr.failed() { // #nosec G103
		return hr.err("IAudioClient.GetService")
	}
	defer release(capture)
	if hr := vcall(client, 10); hr.failed() {
		return hr.err("IAudioClient.Start")
	}
	defer vcall(client, 11)
	running()
	clock := newQPCClock()
	var framer audioFramer
	silence := make([]int16, 0, AudioRate/10*AudioChannels)
	for {
		select {
		case <-s.stop:
			return nil
		default:
		}
		_, _ = windows.WaitForSingleObject(event, 100)
		for {
			var frames uint32
			// An empty buffer is success with 0 frames; a failed call (device
			// invalidated, audio service restarted) ends this client.
			if hr := vcall(capture, 5, uintptr(unsafe.Pointer(&frames))); hr.failed() { // #nosec G103 -- GetNextPacketSize
				return hr.err("IAudioCaptureClient.GetNextPacketSize")
			}
			if frames == 0 {
				break
			}
			var data uintptr
			var flags uint32
			var devPos, qpcPos uint64
			// #nosec G103 -- GetBuffer out pointers.
			hr := vcall(capture, 3, uintptr(unsafe.Pointer(&data)), uintptr(unsafe.Pointer(&frames)), uintptr(unsafe.Pointer(&flags)),
				uintptr(unsafe.Pointer(&devPos)), uintptr(unsafe.Pointer(&qpcPos)))
			if hr.failed() {
				return hr.err("IAudioCaptureClient.GetBuffer")
			}
			if hr == audclntBufferEmpty || frames == 0 {
				break
			}
			var pcm []int16
			if flags&audclntSilent != 0 || data == 0 {
				silence = append(silence[:0], make([]int16, int(frames)*AudioChannels)...)
				pcm = silence
			} else {
				pcm = unsafe.Slice((*int16)(unsafe.Pointer(data)), int(frames)*AudioChannels) //nolint:govet // #nosec G103 -- WASAPI buffer valid until ReleaseBuffer
			}
			framer.push(pcm, clock.at(qpcPos), flags&audclntDiscontinuity != 0, emit)
			if hr := vcall(capture, 4, uintptr(frames)); hr.failed() {
				return hr.err("IAudioCaptureClient.ReleaseBuffer")
			}
		}
	}
}

// qpcClock maps WASAPI QPC positions (100 ns units) to wall-clock times.
type qpcClock struct{ freq int64 }

func newQPCClock() qpcClock {
	var f int64
	_, _, _ = procQueryPerformanceFrequency.Call(uintptr(unsafe.Pointer(&f))) // #nosec G103
	return qpcClock{freq: max(f, 1)}
}

func (c qpcClock) at(pos100ns uint64) time.Time {
	var counter int64
	now := time.Now()
	_, _, _ = procQueryPerformanceCounter.Call(uintptr(unsafe.Pointer(&counter))) // #nosec G103
	now100 := counter/c.freq*10_000_000 + counter%c.freq*10_000_000/c.freq
	if pos100ns == 0 {
		return now
	}
	return now.Add(-time.Duration(now100-int64(pos100ns)) * 100) // #nosec G115 -- QPC fits int64
}
