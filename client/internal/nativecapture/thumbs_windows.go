//go:build windows

package nativecapture

import (
	"bytes"
	"errors"
	"image"
	"image/jpeg"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Picker thumbnails use GDI, not Windows Graphics Capture: one PrintWindow
// (PW_RENDERFULLCONTENT, which renders DirectX/DWM content) or screen
// StretchBlt per source, without a capture session, border or D3D device.

var (
	gdi32                  = windows.NewLazySystemDLL("gdi32.dll")
	procGetDC              = user32.NewProc("GetDC")
	procReleaseDC          = user32.NewProc("ReleaseDC")
	procPrintWindow        = user32.NewProc("PrintWindow")
	procIsHungAppWindow    = user32.NewProc("IsHungAppWindow")
	procCreateCompatibleDC = gdi32.NewProc("CreateCompatibleDC")
	procCreateCompatibleBM = gdi32.NewProc("CreateCompatibleBitmap")
	procCreateDIBSection   = gdi32.NewProc("CreateDIBSection")
	procSelectObject       = gdi32.NewProc("SelectObject")
	procDeleteObject       = gdi32.NewProc("DeleteObject")
	procDeleteDC           = gdi32.NewProc("DeleteDC")
	procStretchBlt         = gdi32.NewProc("StretchBlt")
	procSetStretchBltMode  = gdi32.NewProc("SetStretchBltMode")
	procSetBrushOrgEx      = gdi32.NewProc("SetBrushOrgEx")
	procGdiFlush           = gdi32.NewProc("GdiFlush")
)

const (
	srcCopy             = 0x00CC0020
	halftone            = 4
	pwRenderFullContent = 2
)

// call invokes a GDI/user32 procedure; GDI reports failure in the result only.
func call(p *windows.LazyProc, args ...uintptr) uintptr {
	r, _, _ := p.Call(args...)
	return r
}

// Thumbnail renders src scaled into maxW x maxH as a JPEG. A minimized or
// hung window has no thumbnail.
func Thumbnail(src Source, maxW, maxH int) ([]byte, error) {
	var r rect
	var srcDC uintptr
	var release func()
	if src.Window != 0 {
		alive, minimized := WindowState(src.Window)
		if hung := call(procIsHungAppWindow, uintptr(src.Window)); !alive || minimized || hung != 0 {
			return nil, errors.New("window not drawable")
		}
		if ok := call(procGetWindowRect, uintptr(src.Window), uintptr(unsafe.Pointer(&r))); ok == 0 { // #nosec G103
			return nil, errors.New("GetWindowRect failed")
		}
		w, h := int(r.Right-r.Left), int(r.Bottom-r.Top)
		if w <= 0 || h <= 0 || w > 16384 || h > 16384 {
			return nil, errors.New("window has no area")
		}
		screen := call(procGetDC, 0)
		defer call(procReleaseDC, 0, screen)
		mem := call(procCreateCompatibleDC, screen)
		bitmap := call(procCreateCompatibleBM, screen, uintptr(w), uintptr(h))
		if mem == 0 || bitmap == 0 {
			call(procDeleteObject, bitmap)
			call(procDeleteDC, mem)
			return nil, errors.New("GDI allocation failed")
		}
		old := call(procSelectObject, mem, bitmap)
		release = func() { call(procSelectObject, mem, old); call(procDeleteObject, bitmap); call(procDeleteDC, mem) }
		if ok := call(procPrintWindow, uintptr(src.Window), mem, pwRenderFullContent); ok == 0 {
			release()
			return nil, errors.New("PrintWindow failed")
		}
		srcDC, r = mem, rect{Right: int32(w), Bottom: int32(h)} // #nosec G115
	} else {
		ms := monitors()
		if src.Monitor < 0 || src.Monitor >= len(ms) {
			return nil, errors.New("no such monitor")
		}
		r = ms[src.Monitor].rect
		screen := call(procGetDC, 0)
		srcDC, release = screen, func() { call(procReleaseDC, 0, screen) }
	}
	defer release()
	w, h := outputSize(int(r.Right-r.Left), int(r.Bottom-r.Top), maxW, maxH)
	return stretchJPEG(srcDC, r, w, h)
}

// stretchJPEG scales the src rectangle of dc into a w x h top-down BGRA DIB
// with HALFTONE filtering and encodes it.
func stretchJPEG(dc uintptr, src rect, w, h int) ([]byte, error) {
	info := struct {
		Size                    uint32
		Width, Height           int32
		Planes, BitCount        uint16
		Compression, SizeImage  uint32
		XPels, YPels, Used, Imp int32
	}{Size: 40, Width: int32(w), Height: -int32(h), Planes: 1, BitCount: 32} // #nosec G115
	var bits uintptr
	dib := call(procCreateDIBSection, dc, uintptr(unsafe.Pointer(&info)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0) // #nosec G103
	if dib == 0 || bits == 0 {
		return nil, errors.New("CreateDIBSection failed")
	}
	defer call(procDeleteObject, dib)
	mem := call(procCreateCompatibleDC, dc)
	if mem == 0 {
		return nil, errors.New("CreateCompatibleDC failed")
	}
	defer call(procDeleteDC, mem)
	old := call(procSelectObject, mem, dib)
	defer call(procSelectObject, mem, old)
	call(procSetStretchBltMode, mem, halftone)
	call(procSetBrushOrgEx, mem, 0, 0, 0)
	if ok := call(procStretchBlt, mem, 0, 0, uintptr(w), uintptr(h), dc, uintptr(src.Left), uintptr(src.Top),
		uintptr(src.Right-src.Left), uintptr(src.Bottom-src.Top), srcCopy); ok == 0 {
		return nil, errors.New("StretchBlt failed")
	}
	call(procGdiFlush)
	pixels := unsafe.Slice((*byte)(unsafe.Pointer(bits)), w*h*4) //nolint:govet // #nosec G103 -- DIB section owned until DeleteObject
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(pixels); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = pixels[i+2], pixels[i+1], pixels[i], 255
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: 70}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// Thumbnails renders sources on a few goroutines and returns what is done
// by the budget: a window stuck in PrintWindow delays only its own worker,
// never the picker refresh.
func Thumbnails(sources []Source, maxW, maxH int, budget time.Duration) map[Source][]byte {
	type result struct {
		src Source
		jpg []byte
	}
	results := make(chan result, len(sources)) // late workers never block
	next := make(chan Source, len(sources))
	for _, src := range sources {
		next <- src
	}
	close(next)
	for range min(4, len(sources)) {
		go func() {
			for src := range next {
				jpg, _ := Thumbnail(src, maxW, maxH)
				results <- result{src, jpg}
			}
		}()
	}
	out := make(map[Source][]byte, len(sources))
	timeout := time.After(budget)
	for range sources {
		select {
		case r := <-results:
			if len(r.jpg) > 0 {
				out[r.src] = r.jpg
			}
		case <-timeout:
			return out
		}
	}
	return out
}
