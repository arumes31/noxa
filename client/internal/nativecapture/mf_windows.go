//go:build windows

package nativecapture

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	mfplat                       = windows.NewLazySystemDLL("mfplat.dll")
	procMFStartup                = mfplat.NewProc("MFStartup")
	procMFTEnumEx                = mfplat.NewProc("MFTEnumEx")
	procMFCreateMediaType        = mfplat.NewProc("MFCreateMediaType")
	procMFCreateSample           = mfplat.NewProc("MFCreateSample")
	procMFCreateDXGISurfaceBuf   = mfplat.NewProc("MFCreateDXGISurfaceBuffer")
	procMFCreateDXGIDeviceManger = mfplat.NewProc("MFCreateDXGIDeviceManager")
	procCoTaskMemFree            = windows.NewLazySystemDLL("ole32.dll").NewProc("CoTaskMemFree")
	mfStartOnce                  sync.Once
	mfStartErr                   error

	mftCategoryVideoEncoder = mustGUID("f79eac7d-e545-4387-bdee-d647d7bde42a")
	mfMediaTypeVideo        = mustGUID("73646976-0000-0010-8000-00AA00389B71")
	mfVideoFormatNV12       = mustGUID("3231564E-0000-0010-8000-00AA00389B71")
	mfVideoFormatH264       = mustGUID("34363248-0000-0010-8000-00AA00389B71")
	mfMTMajorType           = mustGUID("48eba18e-f8c9-4687-bf11-0a74c9f96a8f")
	mfMTSubtype             = mustGUID("f7e34c9a-42e8-4714-b74b-cb29d72c35e5")
	mfMTFrameSize           = mustGUID("1652c33d-d6b2-4012-b834-72030849a37d")
	mfMTFrameRate           = mustGUID("c459a2e8-3d2c-4e44-b132-fee5156c7bb0")
	mfMTAvgBitrate          = mustGUID("20332624-fb0d-4d9e-bd0d-cbf6786c102e")
	mfMTInterlaceMode       = mustGUID("e2724bb8-e676-4806-b4b2-a8d6efb44ccd")
	mfMTMPEG2Profile        = mustGUID("ad76a80b-2d5c-4e0b-b375-64e520137036")
	mfMTPixelAspectRatio    = mustGUID("c6376a1e-8d0a-4027-be45-6d9a0ad39bb6")
	mfMTSequenceHeader      = mustGUID("3c036de7-3ad0-4c9e-9216-ee6d6ac21cb3")
	mfMTYUVMatrix           = mustGUID("3e23d450-2c75-4d25-a00e-b91670d12327")
	mfMTNominalRange        = mustGUID("c21b8ee5-b956-4071-8daf-325edf5cab11")
	mfLowLatency            = mustGUID("9c27891a-ed7a-40e1-88e8-b22727a024ee")
	mfTransformAsync        = mustGUID("f81a699a-649a-497d-8c73-29f8fed6ad7a")
	mfTransformAsyncUnlock  = mustGUID("e5666d6b-3422-4eb6-a421-da7db1f8e207")
	mftHardwareVendorID     = mustGUID("3aecb0cc-035b-4bcc-8185-2b8d551ef3af")
	mftFriendlyName         = mustGUID("314ffbae-5b41-4c95-9c19-4e7d586face3")
	mfSampleCleanPoint      = mustGUID("9cdf01d8-a0f0-43ba-b077-eaa06cbd728a")
	iidIMFTransform         = mustGUID("bf94c121-5b05-4e6f-8000-ba598961414d")
	iidIMFMediaEventGen     = mustGUID("2CD0BD52-BCD5-4B89-B62C-EADC0C031E7D")
	iidICodecAPI            = mustGUID("901db4c7-31ce-41a2-85dc-8fa0bf41b8da")

	codecRateControlMode = mustGUID("1c0608e9-370c-4710-8a58-cb6181c42423")
	codecMeanBitRate     = mustGUID("f7222374-2144-4815-b550-a37f8e12ee52")
	codecGOPSize         = mustGUID("95f31b26-95a4-41aa-9303-246a7fc6eef1")
	codecBPictureCount   = mustGUID("8d390aac-dc5c-4200-b57f-814d04babab2")
	codecLowLatencyMode  = mustGUID("9c27891a-ed7a-40e1-88e8-b22727a024ee")
	codecForceKeyFrame   = mustGUID("398c1b98-8353-475a-9ef2-8f265d260345")
	codecSliceMode       = mustGUID("e9e782ef-5f18-44c9-a90b-e9c3c2c17b0b")
	codecSliceSize       = mustGUID("92f51df3-07a5-4172-aefe-c69ca3b60e35")
)

const (
	mftEnumHardware       = 0x4
	mftEnumSyncMFT        = 0x1
	mftEnumSortAndFilter  = 0x40
	msgSetD3DManager      = 0x2
	msgBeginStreaming     = 0x10000000
	msgStartOfStream      = 0x10000003
	msgEndOfStream        = 0x10000002
	meTransformNeedInput  = 601
	meTransformHaveOutput = 602
	eNotImpl              = 0x80004001
	mfENeedMoreInput      = 0xC00D6D72
	mfEStreamChange       = 0xC00D6D61
	h264ProfileBaseline   = 66
)

// ErrNoHardwareEncoder means no hardware H.264 encoder MFT accepted the device.
var ErrNoHardwareEncoder = errors.New("no hardware H.264 encoder")

func mfStartup() error {
	mfStartOnce.Do(func() {
		r, _, _ := procMFStartup.Call(0x00020070, 0) // MF_VERSION, MFSTARTUP_FULL
		mfStartErr = hresult(r).err("MFStartup")
	})
	return mfStartErr
}

type encoderInfo struct {
	name, vendor string
	hardware     bool
}

// encoder is one Media Foundation H.264 encoder MFT fed with NV12 D3D11
// textures. Hardware MFTs are asynchronous: input is submitted only on
// METransformNeedInput and output collected on METransformHaveOutput.
type encoder struct {
	mft, codec, events, manager uintptr
	info                        encoderInfo
	async                       bool
	bitrate                     int
	seqHeader                   []byte
}

type registerTypeInfo struct{ major, sub guid }

// variant is a VT_UI4/VT_BOOL VARIANT (24 bytes on 64-bit).
type variant struct {
	vt  uint16
	_   [3]uint16
	val uint64
	_   uint64
}

func (e *encoder) setCodec(id *guid, value uint32, boolean bool) error {
	if e.codec == 0 {
		return errors.New("ICodecAPI unavailable")
	}
	v := variant{vt: 19, val: uint64(value)}
	if boolean {
		v.vt = 11
		if value != 0 {
			v.val = 0xFFFF
		}
	}
	return vcall(e.codec, 9, uintptr(unsafe.Pointer(id)), uintptr(unsafe.Pointer(&v))).err("ICodecAPI::SetValue") // #nosec G103
}

func activateString(activate uintptr, key *guid) string {
	var p *uint16
	var n uint32
	if hr := vcall(activate, 13, uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(&p)), uintptr(unsafe.Pointer(&n))); hr.failed() || p == nil { // #nosec G103
		return ""
	}
	defer func() { _, _, _ = procCoTaskMemFree.Call(uintptr(unsafe.Pointer(p))) }() // #nosec G103
	return windows.UTF16PtrToString(p)
}

func vendorOf(id, name string) string {
	s := strings.ToLower(id + " " + name)
	switch {
	case strings.Contains(s, "ven_10de") || strings.Contains(s, "nvidia"):
		return "nvidia"
	case strings.Contains(s, "ven_1002") || strings.Contains(s, "amd"):
		return "amd"
	case strings.Contains(s, "ven_8086") || strings.Contains(s, "intel"):
		return "intel"
	}
	return ""
}

// newEncoder opens the first H.264 encoder MFT that accepts the device.
// Hardware only unless allowSoftware (tests on GPU-less runners).
func newEncoder(d *device, width, height, fps, bitrate uint32, allowSoftware bool) (*encoder, error) {
	if err := mfStartup(); err != nil {
		return nil, err
	}
	in := registerTypeInfo{mfMediaTypeVideo, mfVideoFormatNV12}
	out := registerTypeInfo{mfMediaTypeVideo, mfVideoFormatH264}
	flags := []uintptr{mftEnumHardware | mftEnumSortAndFilter}
	if allowSoftware {
		flags = append(flags, mftEnumSyncMFT|mftEnumSortAndFilter)
	}
	lastErr := ErrNoHardwareEncoder
	for _, flag := range flags {
		var list uintptr
		var count uint32
		// #nosec G103 -- MFTEnumEx takes the category GUID by value (passed by reference on x64) and returns a CoTaskMem array.
		r, _, _ := procMFTEnumEx.Call(uintptr(unsafe.Pointer(&mftCategoryVideoEncoder)), flag,
			uintptr(unsafe.Pointer(&in)), uintptr(unsafe.Pointer(&out)), uintptr(unsafe.Pointer(&list)), uintptr(unsafe.Pointer(&count)))
		if hresult(r).failed() || count == 0 {
			continue
		}
		activates := unsafe.Slice((*uintptr)(unsafe.Pointer(list)), count) //nolint:govet // #nosec G103 -- CoTaskMem array from MFTEnumEx
		var opened *encoder
		for _, activate := range activates {
			if opened == nil {
				info := encoderInfo{name: activateString(activate, &mftFriendlyName), hardware: flag&mftEnumHardware != 0}
				info.vendor = vendorOf(activateString(activate, &mftHardwareVendorID), info.name)
				enc, err := openEncoder(d, activate, info, width, height, fps, bitrate)
				if err == nil {
					opened = enc
				} else {
					lastErr = err
				}
			}
			release(activate)
		}
		_, _, _ = procCoTaskMemFree.Call(list)
		if opened != nil {
			return opened, nil
		}
	}
	return nil, lastErr
}

func openEncoder(d *device, activate uintptr, info encoderInfo, width, height, fps, bitrate uint32) (_ *encoder, err error) {
	e := &encoder{info: info, bitrate: int(bitrate)}
	defer func() {
		if err != nil {
			vcall(activate, 34) // ShutdownObject
			e.close()
		}
	}()
	if hr := vcall(activate, 33, uintptr(unsafe.Pointer(&iidIMFTransform)), uintptr(unsafe.Pointer(&e.mft))); hr.failed() { // #nosec G103
		return nil, hr.err("ActivateObject")
	}
	var attrs uintptr
	if hr := vcall(e.mft, 8, uintptr(unsafe.Pointer(&attrs))); hr.succeeded() && attrs != 0 { // #nosec G103 -- GetAttributes
		var async uint32
		vcall(attrs, 7, uintptr(unsafe.Pointer(&mfTransformAsync)), uintptr(unsafe.Pointer(&async))) // #nosec G103
		if async != 0 {
			e.async = true
			vcall(attrs, 21, uintptr(unsafe.Pointer(&mfTransformAsyncUnlock)), 1) // #nosec G103
		}
		vcall(attrs, 21, uintptr(unsafe.Pointer(&mfLowLatency)), 1) // #nosec G103
		release(attrs)
	}
	var token uint32
	if r, _, _ := procMFCreateDXGIDeviceManger.Call(uintptr(unsafe.Pointer(&token)), uintptr(unsafe.Pointer(&e.manager))); hresult(r).failed() { // #nosec G103
		return nil, hresult(r).err("MFCreateDXGIDeviceManager")
	}
	if hr := vcall(e.manager, 7, d.dev, uintptr(token)); hr.failed() {
		return nil, hr.err("ResetDevice")
	}
	if hr := vcall(e.mft, 23, msgSetD3DManager, e.manager); hr.failed() && (info.hardware || uint32(hr) != eNotImpl) {
		return nil, hr.err("MFT_MESSAGE_SET_D3D_MANAGER")
	}
	e.codec, _ = queryInterface(e.mft, &iidICodecAPI)
	// Rate control before the media types: some encoders read it at type time.
	_ = e.setCodec(&codecRateControlMode, 0, false) // CBR
	_ = e.setCodec(&codecMeanBitRate, bitrate, false)
	_ = e.setCodec(&codecLowLatencyMode, 1, true)
	_ = e.setCodec(&codecBPictureCount, 0, false)
	_ = e.setCodec(&codecGOPSize, fps*300, false) // Long GOP: keyframes on demand (PLI).
	// Slices by macroblock rows, all rows: exactly one slice. The sender
	// rejects multi-slice access units, so an encoder refusing this is skipped.
	if err := e.setCodec(&codecSliceMode, 2, false); err != nil {
		return nil, err
	}
	if err := e.setCodec(&codecSliceSize, (height+15)/16, false); err != nil {
		return nil, err
	}
	outType, err := mediaType(mfVideoFormatH264, width, height, fps)
	if err != nil {
		return nil, err
	}
	defer release(outType)
	vcall(outType, 21, uintptr(unsafe.Pointer(&mfMTAvgBitrate)), uintptr(bitrate))      // #nosec G103
	vcall(outType, 21, uintptr(unsafe.Pointer(&mfMTMPEG2Profile)), h264ProfileBaseline) // #nosec G103
	if hr := vcall(e.mft, 16, 0, outType, 0); hr.failed() {
		return nil, hr.err("SetOutputType")
	}
	inType, err := mediaType(mfVideoFormatNV12, width, height, fps)
	if err != nil {
		return nil, err
	}
	defer release(inType)
	if hr := vcall(e.mft, 15, 0, inType, 0); hr.failed() {
		return nil, hr.err("SetInputType")
	}
	if e.async {
		if e.events, err = queryInterface(e.mft, &iidIMFMediaEventGen); err != nil {
			return nil, err
		}
	}
	vcall(e.mft, 23, msgBeginStreaming, 0)
	vcall(e.mft, 23, msgStartOfStream, 0)
	return e, nil
}

func (h hresult) succeeded() bool { return !h.failed() }

func mediaType(subtype guid, width, height, fps uint32) (uintptr, error) {
	var t uintptr
	if r, _, _ := procMFCreateMediaType.Call(uintptr(unsafe.Pointer(&t))); hresult(r).failed() { // #nosec G103
		return 0, hresult(r).err("MFCreateMediaType")
	}
	vcall(t, 24, uintptr(unsafe.Pointer(&mfMTMajorType)), uintptr(unsafe.Pointer(&mfMediaTypeVideo))) // #nosec G103 -- SetGUID
	vcall(t, 24, uintptr(unsafe.Pointer(&mfMTSubtype)), uintptr(unsafe.Pointer(&subtype)))            // #nosec G103
	vcall(t, 22, uintptr(unsafe.Pointer(&mfMTFrameSize)), uintptr(uint64(width)<<32|uint64(height)))  // #nosec G103 -- SetUINT64
	vcall(t, 22, uintptr(unsafe.Pointer(&mfMTFrameRate)), uintptr(uint64(fps)<<32|1))                 // #nosec G103
	vcall(t, 22, uintptr(unsafe.Pointer(&mfMTPixelAspectRatio)), uintptr(uint64(1)<<32|1))            // #nosec G103
	vcall(t, 21, uintptr(unsafe.Pointer(&mfMTInterlaceMode)), 2)                                      // #nosec G103 -- progressive
	vcall(t, 21, uintptr(unsafe.Pointer(&mfMTYUVMatrix)), 1)                                          // #nosec G103 -- BT.709
	vcall(t, 21, uintptr(unsafe.Pointer(&mfMTNominalRange)), 2)                                       // #nosec G103 -- 16-235
	return t, nil
}

// waitEvent blocks on generator (an IMFMediaEventGenerator the caller holds
// its own reference to, so close() cannot free it under a blocked GetEvent).
func waitEvent(generator uintptr) (uint32, error) {
	var ev uintptr
	if hr := vcall(generator, 3, 0, uintptr(unsafe.Pointer(&ev))); hr.failed() { // #nosec G103 -- GetEvent (blocking)
		return 0, hr.err("GetEvent")
	}
	defer release(ev)
	var kind uint32
	if hr := vcall(ev, 33, uintptr(unsafe.Pointer(&kind))); hr.failed() { // #nosec G103 -- GetType
		return 0, hr.err("IMFMediaEvent::GetType")
	}
	var status uint32
	if hr := vcall(ev, 35, uintptr(unsafe.Pointer(&status))); hr.failed() { // #nosec G103 -- GetStatus
		return 0, hr.err("IMFMediaEvent::GetStatus")
	}
	// MEError and failed operations carry their failure here, not in GetEvent.
	if err := hresult(status).err(fmt.Sprintf("encoder event %d", kind)); err != nil {
		return 0, err
	}
	return kind, nil
}

// submit hands one NV12 texture to the encoder (time in 100 ns units).
func (e *encoder) submit(nv12 uintptr, time, duration int64, keyframe bool) error {
	if keyframe {
		_ = e.setCodec(&codecForceKeyFrame, 1, false)
	}
	var buf, sample uintptr
	// #nosec G103 -- out pointers.
	if r, _, _ := procMFCreateDXGISurfaceBuf.Call(uintptr(unsafe.Pointer(&iidID3D11Texture2D)), nv12, 0, 0, uintptr(unsafe.Pointer(&buf))); hresult(r).failed() {
		return hresult(r).err("MFCreateDXGISurfaceBuffer")
	}
	defer release(buf)
	// Some encoder MFTs (Qualcomm) need a valid current length on the surface buffer.
	var length uint32
	if hr := vcall(buf, 7, uintptr(unsafe.Pointer(&length))); hr.succeeded() { // #nosec G103 -- GetMaxLength
		vcall(buf, 6, uintptr(length)) // SetCurrentLength
	}
	if r, _, _ := procMFCreateSample.Call(uintptr(unsafe.Pointer(&sample))); hresult(r).failed() { // #nosec G103
		return hresult(r).err("MFCreateSample")
	}
	defer release(sample)
	vcall(sample, 42, buf)
	vcall(sample, 36, uintptr(time))
	vcall(sample, 38, uintptr(duration))
	return vcall(e.mft, 24, 0, sample, 0).err("ProcessInput")
}

type mftOutputBuffer struct {
	streamID uint32
	sample   uintptr
	status   uint32
	events   uintptr
}

// collect pulls one encoded access unit and its sample time (100 ns; hasTS
// when the MFT set one); nil when the MFT needs more input.
func (e *encoder) collect() ([]byte, bool, int64, bool, error) {
	var info struct{ flags, size, align uint32 }
	vcall(e.mft, 7, 0, uintptr(unsafe.Pointer(&info))) // #nosec G103 -- GetOutputStreamInfo
	out := mftOutputBuffer{}
	if info.flags&(0x100|0x200) == 0 { // Caller must allocate (software MFT).
		var buf uintptr
		if r, _, _ := procMFCreateMemoryBuffer.Call(uintptr(max(info.size, 4<<20)), uintptr(unsafe.Pointer(&buf))); hresult(r).failed() { // #nosec G103
			return nil, false, 0, false, hresult(r).err("MFCreateMemoryBuffer")
		}
		_, _, _ = procMFCreateSample.Call(uintptr(unsafe.Pointer(&out.sample))) // #nosec G103
		vcall(out.sample, 42, buf)
		release(buf)
	}
	var status uint32
	hr := vcall(e.mft, 25, 0, 1, uintptr(unsafe.Pointer(&out)), uintptr(unsafe.Pointer(&status))) // #nosec G103
	release(out.events)
	if uint32(hr) == mfENeedMoreInput {
		release(out.sample)
		return nil, false, 0, false, nil
	}
	if uint32(hr) == mfEStreamChange {
		release(out.sample)
		return nil, false, 0, false, e.renegotiateOutput()
	}
	if hr.failed() {
		release(out.sample)
		return nil, false, 0, false, hr.err("ProcessOutput")
	}
	defer release(out.sample)
	var key uint32
	vcall(out.sample, 7, uintptr(unsafe.Pointer(&mfSampleCleanPoint)), uintptr(unsafe.Pointer(&key))) // #nosec G103
	var contiguous uintptr
	if hr := vcall(out.sample, 41, uintptr(unsafe.Pointer(&contiguous))); hr.failed() { // #nosec G103
		return nil, false, 0, false, hr.err("ConvertToContiguousBuffer")
	}
	defer release(contiguous)
	var p *byte
	var maxLen, cur uint32
	if hr := vcall(contiguous, 3, uintptr(unsafe.Pointer(&p)), uintptr(unsafe.Pointer(&maxLen)), uintptr(unsafe.Pointer(&cur))); hr.failed() { // #nosec G103
		return nil, false, 0, false, hr.err("Lock")
	}
	data := append([]byte(nil), unsafe.Slice(p, cur)...) // #nosec G103
	vcall(contiguous, 4)
	if e.seqHeader == nil {
		e.seqHeader = e.sequenceHeader()
	}
	var ts int64
	hasTS := vcall(out.sample, 35, uintptr(unsafe.Pointer(&ts))).succeeded() // #nosec G103 -- IMFSample::GetSampleTime
	return data, key != 0, ts, hasTS, nil
}

var procMFCreateMemoryBuffer = mfplat.NewProc("MFCreateMemoryBuffer")

func (e *encoder) renegotiateOutput() error {
	var t uintptr
	if hr := vcall(e.mft, 14, 0, 0, uintptr(unsafe.Pointer(&t))); hr.failed() { // #nosec G103 -- GetOutputAvailableType
		return hr.err("GetOutputAvailableType")
	}
	defer release(t)
	return vcall(e.mft, 16, 0, t, 0).err("SetOutputType")
}

// sequenceHeader returns the encoder's Annex-B SPS/PPS, if it reports one.
func (e *encoder) sequenceHeader() []byte {
	var t uintptr
	if hr := vcall(e.mft, 18, 0, uintptr(unsafe.Pointer(&t))); hr.failed() { // #nosec G103 -- GetOutputCurrentType
		return []byte{}
	}
	defer release(t)
	var p *byte
	var n uint32
	if hr := vcall(t, 16, uintptr(unsafe.Pointer(&mfMTSequenceHeader)), uintptr(unsafe.Pointer(&p)), uintptr(unsafe.Pointer(&n))); hr.failed() || p == nil { // #nosec G103 -- GetAllocatedBlob
		return []byte{}
	}
	defer func() { _, _, _ = procCoTaskMemFree.Call(uintptr(unsafe.Pointer(p))) }() // #nosec G103
	return append([]byte(nil), unsafe.Slice(p, n)...)                               // #nosec G103
}

func (e *encoder) close() {
	if e.mft != 0 {
		vcall(e.mft, 23, msgEndOfStream, 0)
		if e.events != 0 {
			// Unblocks GetEvent waiters: shutting down the generator ends them.
			if shutdown, err := queryInterface(e.mft, &iidIMFShutdown); err == nil {
				vcall(shutdown, 3)
				release(shutdown)
			}
		}
	}
	for _, p := range []*uintptr{&e.events, &e.codec, &e.mft, &e.manager} {
		release(*p)
		*p = 0
	}
}

var iidIMFShutdown = mustGUID("97ec2ea4-0e42-4937-97ac-9d6d328824e1")
