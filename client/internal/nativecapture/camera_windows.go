//go:build windows

package nativecapture

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Studio cameras: Media Foundation source reader with its video processor
// converting to RGB32 (BGRA) on the CPU; the compositor uploads each new
// frame. A camera another process streams may refuse a second process
// (seen as MF_E_HW_MFT_FAILED_START_STREAMING from ReadSample): the Studio
// then shows a notice and the compositor retries.

var (
	mfdll                          = windows.NewLazySystemDLL("mf.dll")
	procMFEnumDeviceSources        = mfdll.NewProc("MFEnumDeviceSources")
	procMFCreateDeviceSource       = mfdll.NewProc("MFCreateDeviceSource")
	procMFCreateAttributes         = mfplat.NewProc("MFCreateAttributes")
	procMFCreateSourceReaderFromMS = windows.NewLazySystemDLL("mfreadwrite.dll").NewProc("MFCreateSourceReaderFromMediaSource")

	mfDevSourceType        = mustGUID("c60ac5fe-252a-478f-a0ef-bc8fa5f7cad3")
	mfDevSourceTypeVidcap  = mustGUID("8ac3587a-4ae7-42d8-99e0-0a6013eef90f")
	mfDevSourceFriendly    = mustGUID("60d0e559-52f8-4fa2-bbce-acdb34a8ec01")
	mfDevSourceSymlink     = mustGUID("58f0aad8-22bf-4f8a-bb3d-d2c4978c6e2f")
	mfReaderVideoProcessng = mustGUID("fb394f3d-ccf1-42ee-bbb3-f9b845d5681d")
	mfVideoFormatRGB32     = mustGUID("00000016-0000-0010-8000-00AA00389B71")
	mfMTDefaultStride      = mustGUID("644b4e48-1e02-4516-b0eb-c01ca9d49ac6")
)

const readerFirstVideoStream = 0xFFFFFFFC

// cameraFrames is a running camera; latest returns a frame newer than seq
// (nil when there is none) or the camera's failure.
type cameraFrames interface {
	latest(seq uint64) (pix []byte, w, h int, next uint64, err error)
	close()
}

// openCamera starts camera id; tests replace it with a synthetic feed.
var openCamera = func(id string) cameraFrames { return startMFCamera(id) }

// cameraProcs reports a missing Media Foundation DLL (Windows N editions)
// as an error instead of a panic on the first call.
func cameraProcs() error {
	for _, p := range []*windows.LazyProc{procMFStartup, procMFCreateAttributes, procMFCreateMediaType, procMFEnumDeviceSources,
		procMFCreateDeviceSource, procMFCreateSourceReaderFromMS} {
		if err := p.Find(); err != nil {
			return err
		}
	}
	return nil
}

func newAttributes(n uint32) (uintptr, error) {
	var a uintptr
	if r, _, _ := procMFCreateAttributes.Call(uintptr(unsafe.Pointer(&a)), uintptr(n)); hresult(r).failed() { // #nosec G103
		return 0, hresult(r).err("MFCreateAttributes")
	}
	return a, nil
}

// enumCameras calls use with each video capture device's activation object.
func enumCameras(use func(activate uintptr)) error {
	if err := cameraProcs(); err != nil {
		return err
	}
	if err := mfStartup(); err != nil {
		return err
	}
	attrs, err := newAttributes(1)
	if err != nil {
		return err
	}
	defer release(attrs)
	vcall(attrs, 24, uintptr(unsafe.Pointer(&mfDevSourceType)), uintptr(unsafe.Pointer(&mfDevSourceTypeVidcap))) // #nosec G103 -- SetGUID
	var list uintptr
	var count uint32
	if r, _, _ := procMFEnumDeviceSources.Call(attrs, uintptr(unsafe.Pointer(&list)), uintptr(unsafe.Pointer(&count))); hresult(r).failed() { // #nosec G103
		return hresult(r).err("MFEnumDeviceSources")
	}
	if list == 0 {
		return nil
	}
	defer func() { _, _, _ = procCoTaskMemFree.Call(list) }()
	for _, activate := range unsafe.Slice((*uintptr)(unsafe.Pointer(list)), count) { //nolint:govet // #nosec G103 -- CoTaskMem array
		use(activate)
		release(activate)
	}
	return nil
}

// Cameras lists the video capture devices.
func Cameras() []Camera {
	var out []Camera
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	initThread()
	_ = enumCameras(func(activate uintptr) {
		out = append(out, Camera{ID: activateString(activate, &mfDevSourceSymlink), Name: activateString(activate, &mfDevSourceFriendly)})
	})
	return out
}

type mfCamera struct {
	mu   sync.Mutex
	pix  []byte
	w, h int
	seq  uint64
	err  error
	done chan struct{}
	once sync.Once
}

func startMFCamera(id string) *mfCamera {
	c := &mfCamera{done: make(chan struct{})}
	go c.run(id)
	return c
}

func (c *mfCamera) latest(seq uint64) ([]byte, int, int, uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return nil, 0, 0, seq, c.err
	}
	if c.seq == seq || c.pix == nil {
		return nil, 0, 0, seq, nil
	}
	return c.pix, c.w, c.h, c.seq, nil
}

// close signals the reader thread and returns at once: a camera stalled in
// open or ReadSample must not hold up the pipeline thread; the reader
// releases the device when its call returns.
func (c *mfCamera) close() {
	c.once.Do(func() { close(c.done) })
}

func (c *mfCamera) fail(err error) {
	c.mu.Lock()
	c.err = err
	c.mu.Unlock()
}

func (c *mfCamera) run(id string) {
	runtime.LockOSThread() // Never unlocked: the MTA thread exits with this goroutine.
	initThread()
	reader, source, err := openReader(id)
	if err != nil {
		c.fail(err)
		<-c.done
		return
	}
	defer func() {
		release(reader)
		vcall(source, 12) // IMFMediaSource::Shutdown
		release(source)
	}()
	w, h, stride, err := readerFormat(reader)
	if err != nil {
		c.fail(err)
		<-c.done
		return
	}
	for {
		select {
		case <-c.done:
			return
		default:
		}
		var index, flags uint32
		var ts int64
		var sample uintptr
		// #nosec G103 -- out pointers (blocks until a frame arrives).
		if hr := vcall(reader, 9, readerFirstVideoStream, 0, uintptr(unsafe.Pointer(&index)), uintptr(unsafe.Pointer(&flags)),
			uintptr(unsafe.Pointer(&ts)), uintptr(unsafe.Pointer(&sample))); hr.failed() {
			c.fail(cameraErr(hr, "camera ReadSample"))
			<-c.done
			return
		}
		if flags&0x3 != 0 { // MF_SOURCE_READERF_ERROR | END_OF_STREAM
			release(sample)
			c.fail(errors.New("camera stopped"))
			<-c.done
			return
		}
		if sample == 0 {
			continue
		}
		pix := sampleBGRA(sample, w, h, stride)
		release(sample)
		if pix != nil {
			c.mu.Lock()
			c.pix, c.w, c.h = pix, w, h
			c.seq++
			c.mu.Unlock()
		}
	}
}

// openReader opens camera id with a source reader delivering RGB32 in the
// camera's largest format up to 1280 wide.
func openReader(id string) (reader, source uintptr, err error) {
	if err := cameraProcs(); err != nil {
		return 0, 0, err
	}
	if err := mfStartup(); err != nil {
		return 0, 0, err
	}
	attrs, err := newAttributes(2)
	if err != nil {
		return 0, 0, err
	}
	defer release(attrs)
	link, err := windows.UTF16PtrFromString(id)
	if err != nil {
		return 0, 0, err
	}
	vcall(attrs, 24, uintptr(unsafe.Pointer(&mfDevSourceType)), uintptr(unsafe.Pointer(&mfDevSourceTypeVidcap))) // #nosec G103 -- SetGUID
	vcall(attrs, 25, uintptr(unsafe.Pointer(&mfDevSourceSymlink)), uintptr(unsafe.Pointer(link)))                // #nosec G103 -- SetString
	if r, _, _ := procMFCreateDeviceSource.Call(attrs, uintptr(unsafe.Pointer(&source))); hresult(r).failed() {  // #nosec G103
		return 0, 0, cameraErr(hresult(r), "MFCreateDeviceSource")
	}
	rattrs, err := newAttributes(1)
	if err != nil {
		release(source)
		return 0, 0, err
	}
	defer release(rattrs)
	vcall(rattrs, 21, uintptr(unsafe.Pointer(&mfReaderVideoProcessng)), 1)                                                     // #nosec G103 -- SetUINT32
	if r, _, _ := procMFCreateSourceReaderFromMS.Call(source, rattrs, uintptr(unsafe.Pointer(&reader))); hresult(r).failed() { // #nosec G103
		vcall(source, 12)
		release(source)
		return 0, 0, cameraErr(hresult(r), "MFCreateSourceReaderFromMediaSource")
	}
	fail := func(e error) (uintptr, uintptr, error) {
		release(reader)
		vcall(source, 12)
		release(source)
		return 0, 0, e
	}
	// Pick the native format: the largest up to 1280 wide at 25 fps or more.
	best, bestArea := uintptr(0), 0
	for i := uint32(0); i < 256; i++ {
		var t uintptr
		if hr := vcall(reader, 5, readerFirstVideoStream, uintptr(i), uintptr(unsafe.Pointer(&t))); hr.failed() { // #nosec G103 -- GetNativeMediaType
			break
		}
		var size, rate uint64
		vcall(t, 8, uintptr(unsafe.Pointer(&mfMTFrameSize)), uintptr(unsafe.Pointer(&size))) // #nosec G103 -- GetUINT64
		vcall(t, 8, uintptr(unsafe.Pointer(&mfMTFrameRate)), uintptr(unsafe.Pointer(&rate))) // #nosec G103
		w, h := int(size>>32), int(uint32(size))
		fps := 0.0
		if uint32(rate) != 0 {
			fps = float64(rate>>32) / float64(uint32(rate))
		}
		if area := w * h; w <= 1280 && fps >= 25 && area > bestArea {
			release(best)
			best, bestArea = t, area
			continue
		}
		if best == 0 && i == 0 {
			best = t // fallback: the first format
			continue
		}
		release(t)
	}
	if best != 0 {
		vcall(reader, 7, readerFirstVideoStream, 0, best) // SetCurrentMediaType (native)
		release(best)
	}
	out, err := newAttributesType()
	if err != nil {
		return fail(err)
	}
	defer release(out)
	vcall(out, 24, uintptr(unsafe.Pointer(&mfMTMajorType)), uintptr(unsafe.Pointer(&mfMediaTypeVideo))) // #nosec G103 -- SetGUID
	vcall(out, 24, uintptr(unsafe.Pointer(&mfMTSubtype)), uintptr(unsafe.Pointer(&mfVideoFormatRGB32))) // #nosec G103
	if hr := vcall(reader, 7, readerFirstVideoStream, 0, out); hr.failed() {
		return fail(cameraErr(hr, "camera SetCurrentMediaType(RGB32)"))
	}
	return reader, source, nil
}

func newAttributesType() (uintptr, error) {
	var t uintptr
	if r, _, _ := procMFCreateMediaType.Call(uintptr(unsafe.Pointer(&t))); hresult(r).failed() { // #nosec G103
		return 0, hresult(r).err("MFCreateMediaType")
	}
	return t, nil
}

// cameraErr maps "in use" failures to ErrCameraBusy.
func cameraErr(hr hresult, op string) error {
	switch uint32(hr) {
	// MF_E_VIDEO_RECORDING_DEVICE_PREEMPTED, MF_E_HW_MFT_FAILED_START_STREAMING,
	// ERROR_SHARING_VIOLATION, ERROR_BUSY (as HRESULTs). E_ACCESSDENIED is the
	// camera privacy setting, not a busy camera.
	case 0xC00D3EA3, 0xC00D3704, 0x80070020, 0x800700AA:
		return fmt.Errorf("%w: %v", ErrCameraBusy, hr.err(op))
	}
	return hr.err(op)
}

// readerFormat returns the delivered frame size and its row stride (negative
// for bottom-up rows).
func readerFormat(reader uintptr) (w, h, stride int, err error) {
	var t uintptr
	if hr := vcall(reader, 6, readerFirstVideoStream, uintptr(unsafe.Pointer(&t))); hr.failed() { // #nosec G103 -- GetCurrentMediaType
		return 0, 0, 0, hr.err("camera GetCurrentMediaType")
	}
	defer release(t)
	var size uint64
	vcall(t, 8, uintptr(unsafe.Pointer(&mfMTFrameSize)), uintptr(unsafe.Pointer(&size))) // #nosec G103
	w, h = int(size>>32), int(uint32(size))
	if w <= 0 || h <= 0 || w > 8192 || h > 8192 {
		return 0, 0, 0, errors.New("camera reports no frame size")
	}
	var s uint32
	stride = w * 4
	if hr := vcall(t, 7, uintptr(unsafe.Pointer(&mfMTDefaultStride)), uintptr(unsafe.Pointer(&s))); hr.succeeded() { // #nosec G103 -- GetUINT32
		stride = int(int32(s))
	}
	return w, h, stride, nil
}

// sampleBGRA copies a sample into top-down BGRA rows with opaque alpha.
func sampleBGRA(sample uintptr, w, h, stride int) []byte {
	var buf uintptr
	if hr := vcall(sample, 41, uintptr(unsafe.Pointer(&buf))); hr.failed() { // #nosec G103 -- ConvertToContiguousBuffer
		return nil
	}
	defer release(buf)
	var p *byte
	var maxLen, cur uint32
	if hr := vcall(buf, 3, uintptr(unsafe.Pointer(&p)), uintptr(unsafe.Pointer(&maxLen)), uintptr(unsafe.Pointer(&cur))); hr.failed() || p == nil { // #nosec G103 -- Lock
		return nil
	}
	defer vcall(buf, 4)                                 // Unlock
	return bgraRows(unsafe.Slice(p, cur), w, h, stride) // #nosec G103
}
