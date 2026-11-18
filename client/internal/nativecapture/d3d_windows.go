//go:build windows

package nativecapture

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	d3d11                                    = windows.NewLazySystemDLL("d3d11.dll")
	procD3D11CreateDevice                    = d3d11.NewProc("D3D11CreateDevice")
	procCreateDirect3D11DeviceFromDXGIDevice = d3d11.NewProc("CreateDirect3D11DeviceFromDXGIDevice")
	procCreateDXGIFactory1                   = windows.NewLazySystemDLL("dxgi.dll").NewProc("CreateDXGIFactory1")

	iidIDXGIDevice         = mustGUID("54ec77fa-1377-44e6-8c32-88fd5f44c84c")
	iidIDXGIFactory1       = mustGUID("770aae78-f26f-4dba-a829-253c83d1b387")
	iidID3D10Multithread   = mustGUID("9B7E4E00-342C-4106-A19F-4F2704F689F0")
	iidID3D11VideoDevice   = mustGUID("10EC4D5B-975A-4689-B9E4-D0AAC30FE333")
	iidID3D11VideoContext  = mustGUID("61F21C45-3C0E-4a74-9CEA-67100D9AD5E4")
	iidID3D11Texture2D     = mustGUID("6f15aaf2-d208-4e89-9ab4-489535d34f9c")
	iidIDirect3DDevice     = mustGUID("a37624ab-8d5f-4650-9d3e-9eae3d9bc670")
	iidIDxgiInterfaceAcces = mustGUID("A9B3D012-3DF2-4EE3-B8D1-8695F457D3C1")
)

const (
	dxgiFormatB8G8R8A8 = 87
	dxgiFormatNV12     = 103
	bindRenderTarget   = 0x20
	bindShaderResource = 0x8
	bindVideoEncoder   = 0x400 // D3D11_BIND_VIDEO_ENCODER (D3D 11.1): input of the video encoder
)

type texture2DDesc struct {
	Width, Height, MipLevels, ArraySize, Format uint32
	SampleCount, SampleQuality                  uint32
	Usage, BindFlags, CPUAccessFlags, MiscFlags uint32
}

type d3dBox struct{ Left, Top, Front, Right, Bottom, Back uint32 }

// device is one hardware D3D11 device shared by capture, conversion and the
// encoder (through IMFDXGIDeviceManager); multithread protection is on.
type device struct {
	dev, ctx, videoDev, videoCtx uintptr
	winrt                        uintptr // WinRT IDirect3DDevice for the frame pool
	adapter                      string  // DXGI adapter description, vendor/device ID and LUID (diagnostics)
	luid                         uint64  // the adapter LUID as MFT_ENUM_ADAPTER_LUID holds it (0 unknown)
	vendorID                     uint32  // the adapter's PCI vendor ID (0 unknown)
}

// dxgiAdapterDesc is DXGI_ADAPTER_DESC.
type dxgiAdapterDesc struct {
	Description                                   [128]uint16
	VendorID, DeviceID, SubSysID, Revision        uint32
	DedicatedVideo, DedicatedSystem, SharedSystem uintptr
	LUIDLow                                       uint32
	LUIDHigh                                      int32
}

// describeAdapter fills d.adapter (diagnostics), d.luid and d.vendorID from the adapter behind an IDXGIDevice.
func (d *device) describeAdapter(dxgi uintptr) {
	d.adapter = "unknown"
	var adapter uintptr
	if hr := vcall(dxgi, 7, uintptr(unsafe.Pointer(&adapter))); hr.failed() { // #nosec G103 -- IDXGIDevice::GetAdapter
		return
	}
	defer release(adapter)
	var desc dxgiAdapterDesc
	if hr := vcall(adapter, 8, uintptr(unsafe.Pointer(&desc))); hr.failed() { // #nosec G103 -- IDXGIAdapter::GetDesc
		return
	}
	d.luid = uint64(uint32(desc.LUIDHigh))<<32 | uint64(desc.LUIDLow) // #nosec G115 -- LUID bits
	d.vendorID = desc.VendorID
	d.adapter = fmt.Sprintf("%q vendor=0x%04X device=0x%04X luid=%016X", windows.UTF16ToString(desc.Description[:]),
		desc.VendorID, desc.DeviceID, d.luid)
}

// dxgiOutputDesc is DXGI_OUTPUT_DESC.
type dxgiOutputDesc struct {
	DeviceName [32]uint16
	Desktop    rect
	Attached   int32
	Rotation   uint32
	Monitor    uintptr
}

// monitorAdapter returns the DXGI adapter (AddRef'd) with an output on
// monitor, or 0: the capture device then sits on the GPU that scans out the
// captured screen, so WGC frames need no cross-adapter copy.
func monitorAdapter(monitor uintptr) uintptr {
	if monitor == 0 {
		return 0
	}
	var factory uintptr
	if r, _, _ := procCreateDXGIFactory1.Call(uintptr(unsafe.Pointer(&iidIDXGIFactory1)), uintptr(unsafe.Pointer(&factory))); hresult(r).failed() { // #nosec G103
		return 0
	}
	defer release(factory)
	for i := uintptr(0); ; i++ {
		var adapter uintptr
		if hr := vcall(factory, 12, i, uintptr(unsafe.Pointer(&adapter))); hr.failed() { // #nosec G103 -- IDXGIFactory1::EnumAdapters1 (DXGI_ERROR_NOT_FOUND ends)
			return 0
		}
		for j := uintptr(0); ; j++ {
			var output uintptr
			if hr := vcall(adapter, 7, j, uintptr(unsafe.Pointer(&output))); hr.failed() { // #nosec G103 -- IDXGIAdapter::EnumOutputs
				break
			}
			var desc dxgiOutputDesc
			ok := vcall(output, 7, uintptr(unsafe.Pointer(&desc))).succeeded() && desc.Monitor == monitor // #nosec G103 -- IDXGIOutput::GetDesc
			release(output)
			if ok {
				return adapter
			}
		}
		release(adapter)
	}
}

// newDevice creates the hardware device on the adapter driving monitor (0:
// the default adapter, also the fallback when that adapter refuses).
func newDevice(monitor uintptr) (*device, error) {
	d := &device{}
	var level uint32
	create := func(adapter, driverType uintptr) error {
		// BGRA_SUPPORT|VIDEO_SUPPORT, D3D11_SDK_VERSION.
		// #nosec G103 -- out pointers.
		r, _, _ := procD3D11CreateDevice.Call(adapter, driverType, 0, 0x20|0x800, 0, 0, 7,
			uintptr(unsafe.Pointer(&d.dev)), uintptr(unsafe.Pointer(&level)), uintptr(unsafe.Pointer(&d.ctx)))
		return hresult(r).err("D3D11CreateDevice")
	}
	onMonitor := false
	if adapter := monitorAdapter(monitor); adapter != 0 {
		onMonitor = create(adapter, 0) == nil // D3D_DRIVER_TYPE_UNKNOWN: required with an explicit adapter
		release(adapter)
	}
	if !onMonitor {
		if err := create(0, 1); err != nil { // D3D_DRIVER_TYPE_HARDWARE on the default adapter
			return nil, err
		}
	}
	var err error
	if mt, qerr := queryInterface(d.dev, &iidID3D10Multithread); qerr == nil {
		vcall(mt, 5, 1) // SetMultithreadProtected(TRUE): MF uses the device from its own threads.
		release(mt)
	}
	if d.videoDev, err = queryInterface(d.dev, &iidID3D11VideoDevice); err != nil {
		d.close()
		return nil, errors.New("D3D11 video device unavailable")
	}
	if d.videoCtx, err = queryInterface(d.ctx, &iidID3D11VideoContext); err != nil {
		d.close()
		return nil, errors.New("D3D11 video context unavailable")
	}
	dxgi, err := queryInterface(d.dev, &iidIDXGIDevice)
	if err != nil {
		d.close()
		return nil, err
	}
	defer release(dxgi)
	d.describeAdapter(dxgi)
	if onMonitor {
		d.adapter += " (captured monitor's adapter)"
	} else {
		d.adapter += " (default adapter)"
	}
	// Capture is latency critical; positive values may be refused without
	// privilege, which keeps the default.
	vcall(dxgi, 10, 7) // IDXGIDevice::SetGPUThreadPriority
	var inspectable uintptr
	// #nosec G103 -- out pointer.
	if r, _, _ := procCreateDirect3D11DeviceFromDXGIDevice.Call(dxgi, uintptr(unsafe.Pointer(&inspectable))); hresult(r).failed() {
		d.close()
		return nil, hresult(r).err("CreateDirect3D11DeviceFromDXGIDevice")
	}
	d.winrt, err = queryInterface(inspectable, &iidIDirect3DDevice)
	release(inspectable)
	if err != nil {
		d.close()
		return nil, err
	}
	return d, nil
}

func (d *device) close() {
	for _, p := range []*uintptr{&d.winrt, &d.videoCtx, &d.videoDev, &d.ctx, &d.dev} {
		release(*p)
		*p = 0
	}
}

func (d *device) createTexture(w, h, format, bind uint32) (uintptr, error) {
	desc := texture2DDesc{Width: w, Height: h, MipLevels: 1, ArraySize: 1, Format: format, SampleCount: 1, BindFlags: bind}
	var tex uintptr
	if hr := vcall(d.dev, 5, uintptr(unsafe.Pointer(&desc)), 0, uintptr(unsafe.Pointer(&tex))); hr.failed() { // #nosec G103
		return 0, hr.err("CreateTexture2D")
	}
	return tex, nil
}

// copyRegion copies box of src into (0,0) of dst (GPU only).
func (d *device) copyRegion(dst, src uintptr, box d3dBox) {
	vcall(d.ctx, 46, dst, 0, 0, 0, 0, src, 0, uintptr(unsafe.Pointer(&box))) // #nosec G103
}

// converter scales a BGRA texture into NV12 with the D3D11 video processor:
// colour conversion (BT.709 limited range) and scaling stay on the GPU.
type converter struct {
	d                       *device
	inW, inH, outW, outH    uint32
	enum, vp, input, inView uintptr
}

type vpContentDesc struct {
	InputFrameFormat           uint32
	InNum, InDen, InW, InH     uint32
	OutNum, OutDen, OutW, OutH uint32
	Usage                      uint32
}

type vpStream struct {
	Enable, OutputIndex, InputFrameOrField, PastFrames, FutureFrames uint32
	past, input, future, pastRight, inputRight, futureRight          uintptr
}

func (d *device) newConverter(inW, inH, outW, outH uint32) (*converter, error) {
	c := &converter{d: d, inW: inW, inH: inH, outW: outW, outH: outH}
	desc := vpContentDesc{InNum: 60, InDen: 1, InW: inW, InH: inH, OutNum: 60, OutDen: 1, OutW: outW, OutH: outH}
	if hr := vcall(d.videoDev, 10, uintptr(unsafe.Pointer(&desc)), uintptr(unsafe.Pointer(&c.enum))); hr.failed() { // #nosec G103
		return nil, hr.err("CreateVideoProcessorEnumerator")
	}
	if hr := vcall(d.videoDev, 4, c.enum, 0, uintptr(unsafe.Pointer(&c.vp))); hr.failed() { // #nosec G103
		c.close()
		return nil, hr.err("CreateVideoProcessor")
	}
	var err error
	if c.input, err = d.createTexture(inW, inH, dxgiFormatB8G8R8A8, bindRenderTarget|bindShaderResource); err != nil {
		c.close()
		return nil, err
	}
	inDesc := struct{ FourCC, ViewDimension, MipSlice, ArraySlice uint32 }{ViewDimension: 1}
	if hr := vcall(d.videoDev, 8, c.input, c.enum, uintptr(unsafe.Pointer(&inDesc)), uintptr(unsafe.Pointer(&c.inView))); hr.failed() { // #nosec G103
		c.close()
		return nil, hr.err("CreateVideoProcessorInputView")
	}
	rgbFull, yuv709Limited := uint32(0), uint32(0x4|0x10)                // YCbCr_Matrix=BT.709, Nominal_Range=16-235
	vcall(d.videoCtx, 15, c.vp, uintptr(unsafe.Pointer(&yuv709Limited))) // #nosec G103 -- SetOutputColorSpace
	vcall(d.videoCtx, 27, c.vp, 0, 0)                                    // SetStreamFrameFormat progressive
	vcall(d.videoCtx, 28, c.vp, 0, uintptr(unsafe.Pointer(&rgbFull)))    // #nosec G103 -- SetStreamColorSpace
	vcall(d.videoCtx, 37, c.vp, 0, 0)                                    // SetStreamAutoProcessingMode(FALSE)
	return c, nil
}

// outputView creates the NV12 render-target view for one encoder input texture.
func (c *converter) outputView(nv12 uintptr) (uintptr, error) {
	desc := struct{ ViewDimension, MipSlice, a, b uint32 }{ViewDimension: 1}
	var view uintptr
	if hr := vcall(c.d.videoDev, 9, nv12, c.enum, uintptr(unsafe.Pointer(&desc)), uintptr(unsafe.Pointer(&view))); hr.failed() { // #nosec G103
		return 0, hr.err("CreateVideoProcessorOutputView")
	}
	return view, nil
}

// blt scales the top-left w x h of c.input into the NV12 texture behind
// outView. Output sizes keep the source aspect ratio (outputSize), so no
// letterbox is needed.
func (c *converter) blt(outView uintptr, w, h uint32) error {
	src := rect{0, 0, int32(w), int32(h)}
	full := rect{0, 0, int32(c.outW), int32(c.outH)}
	vcall(c.d.videoCtx, 13, c.vp, 1, uintptr(unsafe.Pointer(&full)))    // #nosec G103 -- SetOutputTargetRect
	vcall(c.d.videoCtx, 30, c.vp, 0, 1, uintptr(unsafe.Pointer(&src)))  // #nosec G103 -- SetStreamSourceRect
	vcall(c.d.videoCtx, 31, c.vp, 0, 1, uintptr(unsafe.Pointer(&full))) // #nosec G103 -- SetStreamDestRect
	stream := vpStream{Enable: 1, input: c.inView}
	if hr := vcall(c.d.videoCtx, 53, c.vp, outView, 0, 1, uintptr(unsafe.Pointer(&stream))); hr.failed() { // #nosec G103
		return hr.err("VideoProcessorBlt")
	}
	return nil
}

func (c *converter) close() {
	for _, p := range []*uintptr{&c.inView, &c.input, &c.vp, &c.enum} {
		release(*p)
		*p = 0
	}
}

type rect struct{ Left, Top, Right, Bottom int32 }
