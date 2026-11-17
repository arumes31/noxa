//go:build windows

package nativecapture

import (
	"errors"
	"image"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procD3DCompile = windows.NewLazySystemDLL("d3dcompiler_47.dll").NewProc("D3DCompile")
)

const (
	dxgiFormatR8G8B8A8 = 28
	bindConstantBuffer = 0x4
)

// One textured (or solid) quad per draw: dst and uv are fractions of the
// target and of the texture; flags = (alpha, solid, opaque, unused).
// Straight alpha, as canvas and PNG overlays have it.
const compositorHLSL = `
cbuffer Quad : register(b0) { float4 dst; float4 uv; float4 color; float4 flags; };
struct V { float4 pos : SV_Position; float2 tex : TEXCOORD0; };
V vs(uint id : SV_VertexID) {
	float2 c = float2(id & 1, id >> 1);
	float2 p = dst.xy + c * dst.zw;
	V o;
	o.pos = float4(p.x * 2 - 1, 1 - p.y * 2, 0, 1);
	o.tex = uv.xy + c * uv.zw;
	return o;
}
Texture2D t : register(t0);
SamplerState s : register(s0);
float4 ps(V i) : SV_Target {
	float4 c = flags.y > 0.5 ? color : t.Sample(s, i.tex);
	if (flags.z > 0.5) c.a = 1;
	c.a *= flags.x;
	return c;
}`

// neutral is studio-compositor.js NEUTRAL (#11161c): a source without a picture.
var neutral = [4]float32{0x11 / 255.0, 0x16 / 255.0, 0x1c / 255.0, 1}

type quadConstants struct {
	dst, uv, color, flags [4]float32
}

// studioUpdate is a scene and its overlay images, handed from a binding
// goroutine to the pipeline thread.
type studioUpdate struct {
	scene  Scene
	images []*image.NRGBA
}

// studioInbox carries scene updates in and camera failures out.
type studioInbox struct {
	next      atomic.Pointer[studioUpdate]
	mu        sync.Mutex
	cameraErr string
}

func (b *studioInbox) setCameraErr(msg string) {
	b.mu.Lock()
	b.cameraErr = msg
	b.mu.Unlock()
}

func (b *studioInbox) cameraError() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cameraErr
}

// gpuTexture is a texture with its shader resource view.
type gpuTexture struct {
	tex, srv uintptr
	w, h     int
}

func (t *gpuTexture) close() {
	release(t.srv)
	release(t.tex)
	*t = gpuTexture{}
}

type onAir struct {
	scene    Scene
	overlays []gpuTexture
}

func (a *onAir) close() {
	for i := range a.overlays {
		a.overlays[i].close()
	}
}

type displayFeed struct {
	capt    *capturer
	pic     gpuTexture
	dead    bool // window closed: neutral from now on
	checked time.Time
}

type cameraFeed struct {
	cam cameraFrames
	pic gpuTexture
	seq uint64
}

// compositor renders the Studio program into a fixed-size BGRA canvas: each
// scene's layers bottom-up, a fade as two scene targets blended. It is a
// frameSource: latest renders one frame per capture tick.
type compositor struct {
	d                         *device
	w, h                      int
	inbox                     *studioInbox
	vs, ps, cb, sampler, blnd uintptr
	canvas                    gpuTexture
	canvasRTV                 uintptr
	layers                    [2]gpuTexture
	layerRTV                  [2]uintptr
	live, outgoing            *onAir
	fadeStart                 time.Time
	fadeMS                    int
	displays                  map[Source]*displayFeed
	cameras                   map[string]*cameraFeed
}

func compileShader(entry, target string) (uintptr, error) {
	src := []byte(compositorHLSL)
	e, _ := windows.BytePtrFromString(entry)
	tg, _ := windows.BytePtrFromString(target)
	var code, msgs uintptr
	// #nosec G103 -- source buffer, entry/target strings and out pointers.
	r, _, _ := procD3DCompile.Call(uintptr(unsafe.Pointer(&src[0])), uintptr(len(src)), 0, 0, 0,
		uintptr(unsafe.Pointer(e)), uintptr(unsafe.Pointer(tg)), 0, 0, uintptr(unsafe.Pointer(&code)), uintptr(unsafe.Pointer(&msgs)))
	release(msgs)
	if err := hresult(r).err("D3DCompile " + entry); err != nil {
		return 0, err
	}
	return code, nil
}

func blobBytes(blob uintptr) (uintptr, uintptr) {
	ptr := uintptr(vcall(blob, 3)) // GetBufferPointer
	n := uintptr(vcall(blob, 4))   // GetBufferSize
	return ptr, n
}

func newCompositor(d *device, w, h int, inbox *studioInbox) (*compositor, error) {
	if inbox == nil {
		return nil, errors.New("studio scene missing")
	}
	w, h = max(2, w&^1), max(2, h&^1)
	c := &compositor{d: d, w: w, h: h, inbox: inbox, displays: map[Source]*displayFeed{}, cameras: map[string]*cameraFeed{}}
	if err := c.init(); err != nil {
		c.close()
		return nil, err
	}
	return c, nil
}

func (c *compositor) init() error {
	vsBlob, err := compileShader("vs", "vs_4_0")
	if err != nil {
		return err
	}
	defer release(vsBlob)
	psBlob, err := compileShader("ps", "ps_4_0")
	if err != nil {
		return err
	}
	defer release(psBlob)
	ptr, n := blobBytes(vsBlob)
	if hr := vcall(c.d.dev, 12, ptr, n, 0, uintptr(unsafe.Pointer(&c.vs))); hr.failed() { // #nosec G103 -- CreateVertexShader
		return hr.err("CreateVertexShader")
	}
	ptr, n = blobBytes(psBlob)
	if hr := vcall(c.d.dev, 15, ptr, n, 0, uintptr(unsafe.Pointer(&c.ps))); hr.failed() { // #nosec G103 -- CreatePixelShader
		return hr.err("CreatePixelShader")
	}
	cbDesc := struct{ ByteWidth, Usage, BindFlags, CPUAccessFlags, MiscFlags, StructureByteStride uint32 }{
		ByteWidth: uint32(unsafe.Sizeof(quadConstants{})), BindFlags: bindConstantBuffer}
	if hr := vcall(c.d.dev, 3, uintptr(unsafe.Pointer(&cbDesc)), 0, uintptr(unsafe.Pointer(&c.cb))); hr.failed() { // #nosec G103 -- CreateBuffer
		return hr.err("CreateBuffer")
	}
	sampler := struct {
		Filter, AddressU, AddressV, AddressW uint32
		MipLODBias                           float32
		MaxAnisotropy, ComparisonFunc        uint32
		BorderColor                          [4]float32
		MinLOD, MaxLOD                       float32
	}{Filter: 0x15, AddressU: 3, AddressV: 3, AddressW: 3, MaxAnisotropy: 1, ComparisonFunc: 1, MaxLOD: 3.402823466e+38} // linear, clamp
	if hr := vcall(c.d.dev, 23, uintptr(unsafe.Pointer(&sampler)), uintptr(unsafe.Pointer(&c.sampler))); hr.failed() { // #nosec G103 -- CreateSamplerState
		return hr.err("CreateSamplerState")
	}
	type rtBlend struct {
		BlendEnable                                                           int32
		SrcBlend, DestBlend, BlendOp, SrcBlendAlpha, DestBlendAlpha, BlendOp2 uint32
		WriteMask                                                             uint8
		_                                                                     [3]uint8
	}
	blend := struct {
		AlphaToCoverage, Independent int32
		RT                           [8]rtBlend
	}{}
	// out = src*a + dst*(1-a); alpha: a + dst*(1-a).
	blend.RT[0] = rtBlend{BlendEnable: 1, SrcBlend: 5, DestBlend: 6, BlendOp: 1, SrcBlendAlpha: 2, DestBlendAlpha: 6, BlendOp2: 1, WriteMask: 0xF}
	if hr := vcall(c.d.dev, 20, uintptr(unsafe.Pointer(&blend)), uintptr(unsafe.Pointer(&c.blnd))); hr.failed() { // #nosec G103 -- CreateBlendState
		return hr.err("CreateBlendState")
	}
	var err2 error
	if c.canvas, c.canvasRTV, err2 = c.target(); err2 != nil {
		return err2
	}
	for i := range c.layers {
		if c.layers[i], c.layerRTV[i], err2 = c.target(); err2 != nil {
			return err2
		}
	}
	return nil
}

// target creates a w x h BGRA render target that can also be sampled.
func (c *compositor) target() (gpuTexture, uintptr, error) {
	tex, err := c.d.createTexture(uint32(c.w), uint32(c.h), dxgiFormatB8G8R8A8, bindRenderTarget|bindShaderResource)
	if err != nil {
		return gpuTexture{}, 0, err
	}
	t := gpuTexture{tex: tex, w: c.w, h: c.h}
	if t.srv, err = c.d.shaderView(tex); err != nil {
		t.close()
		return gpuTexture{}, 0, err
	}
	var rtv uintptr
	if hr := vcall(c.d.dev, 9, tex, 0, uintptr(unsafe.Pointer(&rtv))); hr.failed() { // #nosec G103 -- CreateRenderTargetView
		t.close()
		return gpuTexture{}, 0, hr.err("CreateRenderTargetView")
	}
	return t, rtv, nil
}

func (d *device) shaderView(tex uintptr) (uintptr, error) {
	var srv uintptr
	if hr := vcall(d.dev, 7, tex, 0, uintptr(unsafe.Pointer(&srv))); hr.failed() { // #nosec G103 -- CreateShaderResourceView
		return 0, hr.err("CreateShaderResourceView")
	}
	return srv, nil
}

// uploadTexture creates a sampled texture holding w x h pixels of format
// (4 bytes each, rows of pitch bytes).
func (d *device) uploadTexture(pix []byte, w, h, pitch int, format uint32) (gpuTexture, error) {
	if w <= 0 || h <= 0 || len(pix) < pitch*(h-1)+w*4 {
		return gpuTexture{}, errors.New("invalid picture")
	}
	desc := texture2DDesc{Width: uint32(w), Height: uint32(h), MipLevels: 1, ArraySize: 1, Format: format, SampleCount: 1, BindFlags: bindShaderResource}
	data := struct {
		mem          uintptr
		pitch, slice uint32
	}{uintptr(unsafe.Pointer(&pix[0])), uint32(pitch), 0} // #nosec G103 G115
	var tex uintptr
	if hr := vcall(d.dev, 5, uintptr(unsafe.Pointer(&desc)), uintptr(unsafe.Pointer(&data)), uintptr(unsafe.Pointer(&tex))); hr.failed() { // #nosec G103
		return gpuTexture{}, hr.err("CreateTexture2D")
	}
	runtime.KeepAlive(pix) // its address was passed inside data
	t := gpuTexture{tex: tex, w: w, h: h}
	var err error
	if t.srv, err = d.shaderView(tex); err != nil {
		t.close()
		return gpuTexture{}, err
	}
	return t, nil
}

// ensurePicture (re)creates a sampled BGRA texture of w x h in t.
func (c *compositor) ensurePicture(t *gpuTexture, w, h int) error {
	if t.tex != 0 && t.w == w && t.h == h {
		return nil
	}
	t.close()
	tex, err := c.d.createTexture(uint32(w), uint32(h), dxgiFormatB8G8R8A8, bindShaderResource)
	if err != nil {
		return err
	}
	*t = gpuTexture{tex: tex, w: w, h: h}
	if t.srv, err = c.d.shaderView(tex); err != nil {
		t.close()
		return err
	}
	return nil
}

// apply takes a pending scene update: a new Take goes on air (fading over
// the current program when asked); the live Take only swaps its overlays.
func (c *compositor) apply(now time.Time) {
	upd := c.inbox.next.Swap(nil)
	if upd == nil {
		return
	}
	overlays := make([]gpuTexture, len(upd.images))
	for i, img := range upd.images {
		if img == nil {
			continue
		}
		t, err := c.d.uploadTexture(img.Pix, img.Rect.Dx(), img.Rect.Dy(), img.Stride, dxgiFormatR8G8B8A8)
		if err == nil {
			overlays[i] = t
		}
	}
	next := &onAir{scene: upd.scene, overlays: overlays}
	if c.live != nil && c.live.scene.Take == upd.scene.Take {
		c.live.close()
		c.live = next
		return
	}
	if c.outgoing != nil {
		c.outgoing.close()
		c.outgoing = nil
	}
	if c.live != nil && upd.scene.Transition.Kind == "fade" && upd.scene.Transition.MS > 0 {
		c.outgoing, c.fadeStart, c.fadeMS = c.live, now, upd.scene.Transition.MS
	} else if c.live != nil {
		c.live.close()
	}
	c.live = next
}

// syncSources opens the display captures and cameras the scenes on air use
// and closes the rest.
func (c *compositor) syncSources() {
	var outgoing *Scene
	if c.outgoing != nil {
		outgoing = &c.outgoing.scene
	}
	var live *Scene
	if c.live != nil {
		live = &c.live.scene
	}
	displays, cameras := sceneSources(live, outgoing)
	want := map[Source]bool{}
	for _, src := range displays {
		want[src] = true
		if c.displays[src] == nil {
			feed := &displayFeed{}
			capt, err := newCapturer(c.d, src)
			if err != nil {
				feed.dead = true // closed window or missing monitor: neutral
			}
			feed.capt = capt
			c.displays[src] = feed
		}
	}
	for src, feed := range c.displays {
		if !want[src] {
			feed.close()
			delete(c.displays, src)
		}
	}
	wantCam := map[string]bool{}
	for _, id := range cameras {
		wantCam[id] = true
		if c.cameras[id] == nil {
			c.cameras[id] = &cameraFeed{cam: openCamera(id)}
		}
	}
	for id, feed := range c.cameras {
		if !wantCam[id] {
			feed.close()
			delete(c.cameras, id)
		}
	}
}

func (f *displayFeed) close() {
	if f.capt != nil {
		f.capt.close()
	}
	f.pic.close()
}

func (f *cameraFeed) close() {
	f.cam.close()
	f.pic.close()
}

// poll copies every source's newest picture into its own texture (a WGC
// frame is only valid inside latest's callback).
func (c *compositor) poll(now time.Time) {
	for src, feed := range c.displays {
		if feed.dead || feed.capt == nil {
			continue
		}
		if src.Window != 0 && now.Sub(feed.checked) >= time.Second {
			feed.checked = now
			if alive, _ := WindowState(src.Window); !alive {
				feed.capt.close()
				feed.capt, feed.dead = nil, true
				feed.pic.close()
				continue
			}
		}
		_, err := feed.capt.latest(func(tex uintptr, w, h uint32) {
			if c.ensurePicture(&feed.pic, int(w), int(h)) == nil {
				c.d.copyRegion(feed.pic.tex, tex, d3dBox{Right: w, Bottom: h, Back: 1})
			}
		})
		if err != nil {
			feed.capt.close()
			feed.capt, feed.dead = nil, true
			feed.pic.close()
		}
	}
	cameraErr := ""
	for _, feed := range c.cameras {
		pix, w, h, seq, err := feed.cam.latest(feed.seq)
		if err != nil {
			cameraErr = err.Error()
			feed.pic.close()
			continue
		}
		if pix == nil {
			continue
		}
		feed.seq = seq
		if feed.pic.w != w || feed.pic.h != h {
			feed.pic.close()
		}
		if feed.pic.tex == 0 {
			if t, err := c.d.uploadTexture(pix, w, h, w*4, dxgiFormatB8G8R8A8); err == nil {
				feed.pic = t
			}
			continue
		}
		vcall(c.d.ctx, 48, feed.pic.tex, 0, 0, uintptr(unsafe.Pointer(&pix[0])), uintptr(w*4), 0) // #nosec G103 -- UpdateSubresource
	}
	c.inbox.setCameraErr(cameraErr)
}

// latest renders the program and hands the canvas to use.
func (c *compositor) latest(use func(tex uintptr, w, h uint32)) (bool, error) {
	now := time.Now()
	c.apply(now)
	c.syncSources()
	c.poll(now)
	fade := 1.0
	if c.outgoing != nil {
		var done bool
		if fade, done = fadeProgress(c.fadeStart, now, c.fadeMS); done {
			c.outgoing.close()
			c.outgoing = nil
			c.syncSources()
		}
	}
	if c.outgoing != nil {
		c.renderScene(c.layerRTV[0], c.outgoing)
		c.renderScene(c.layerRTV[1], c.live)
		c.begin(c.canvasRTV)
		full := rectF{0, 0, 1, 1}
		c.draw(c.layers[0].srv, full, full, 1, true)
		c.draw(c.layers[1].srv, full, full, float32(fade), true)
	} else {
		c.renderScene(c.canvasRTV, c.live)
	}
	c.unbind()
	use(c.canvas.tex, uint32(c.w), uint32(c.h)) // #nosec G115
	return true, nil
}

func (c *compositor) begin(rtv uintptr) {
	ctx := c.d.ctx
	c.unbind()
	vcall(ctx, 33, 1, uintptr(unsafe.Pointer(&rtv)), 0) // #nosec G103 -- OMSetRenderTargets
	viewport := struct{ X, Y, W, H, MinZ, MaxZ float32 }{W: float32(c.w), H: float32(c.h), MaxZ: 1}
	vcall(ctx, 44, 1, uintptr(unsafe.Pointer(&viewport))) // #nosec G103 -- RSSetViewports
	black := [4]float32{0, 0, 0, 1}
	vcall(ctx, 50, rtv, uintptr(unsafe.Pointer(&black)))      // #nosec G103 -- ClearRenderTargetView
	vcall(ctx, 17, 0)                                         // IASetInputLayout(NULL)
	vcall(ctx, 24, 5)                                         // IASetPrimitiveTopology(TRIANGLESTRIP)
	vcall(ctx, 11, c.vs, 0, 0)                                // VSSetShader
	vcall(ctx, 9, c.ps, 0, 0)                                 // PSSetShader
	vcall(ctx, 7, 0, 1, uintptr(unsafe.Pointer(&c.cb)))       // #nosec G103 -- VSSetConstantBuffers
	vcall(ctx, 16, 0, 1, uintptr(unsafe.Pointer(&c.cb)))      // #nosec G103 -- PSSetConstantBuffers
	vcall(ctx, 10, 0, 1, uintptr(unsafe.Pointer(&c.sampler))) // #nosec G103 -- PSSetSamplers
	vcall(ctx, 35, c.blnd, 0, 0xFFFFFFFF)                     // OMSetBlendState
}

// unbind detaches the shader input and render target, so a scene target
// can switch between being drawn into and being sampled.
func (c *compositor) unbind() {
	var null uintptr
	vcall(c.d.ctx, 8, 0, 1, uintptr(unsafe.Pointer(&null))) // #nosec G103 -- PSSetShaderResources
	vcall(c.d.ctx, 33, 0, 0, 0)                             // OMSetRenderTargets(none)
}

func (c *compositor) quad(srv uintptr, q quadConstants) {
	vcall(c.d.ctx, 48, c.cb, 0, 0, uintptr(unsafe.Pointer(&q)), 0, 0) // #nosec G103 -- UpdateSubresource
	vcall(c.d.ctx, 8, 0, 1, uintptr(unsafe.Pointer(&srv)))            // #nosec G103 -- PSSetShaderResources
	vcall(c.d.ctx, 13, 4, 0)                                          // Draw
}

func f4(r rectF) [4]float32 {
	return [4]float32{float32(r.X), float32(r.Y), float32(r.W), float32(r.H)}
}

func (c *compositor) draw(srv uintptr, dst, uv rectF, alpha float32, opaque bool) {
	flags := [4]float32{alpha, 0, 0, 0}
	if opaque {
		flags[2] = 1
	}
	c.quad(srv, quadConstants{dst: f4(dst), uv: f4(uv), flags: flags})
}

func (c *compositor) fill(dst rectF, color [4]float32) {
	c.quad(0, quadConstants{dst: f4(dst), color: color, flags: [4]float32{1, 1, 1, 0}})
}

func (c *compositor) renderScene(rtv uintptr, a *onAir) {
	c.begin(rtv)
	if a == nil {
		return
	}
	for _, l := range a.scene.Layers {
		var pic gpuTexture
		switch l.Kind {
		case LayerOverlay:
			if l.Overlay < len(a.overlays) && a.overlays[l.Overlay].srv != 0 {
				full := rectF{0, 0, 1, 1}
				c.draw(a.overlays[l.Overlay].srv, full, full, 1, false)
			}
			continue
		case LayerDisplay:
			if feed := c.displays[l.Source]; feed != nil {
				pic = feed.pic
			}
		case LayerCamera:
			if feed := c.cameras[l.Camera]; feed != nil {
				pic = feed.pic
			}
		}
		if pic.srv == 0 {
			c.fill(rectF{l.X, l.Y, l.W, l.H}, neutral)
			continue
		}
		dst, uv := placement(l, c.w, c.h, pic.w, pic.h)
		c.draw(pic.srv, dst, uv, 1, true)
	}
}

func (c *compositor) close() {
	if c.live != nil {
		c.live.close()
	}
	if c.outgoing != nil {
		c.outgoing.close()
	}
	for _, f := range c.displays {
		f.close()
	}
	for _, f := range c.cameras {
		f.close()
	}
	c.canvas.close()
	release(c.canvasRTV)
	for i := range c.layers {
		c.layers[i].close()
		release(c.layerRTV[i])
	}
	for _, p := range []*uintptr{&c.blnd, &c.sampler, &c.cb, &c.ps, &c.vs} {
		release(*p)
		*p = 0
	}
}
