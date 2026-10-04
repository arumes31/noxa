//go:build windows

package main

import (
	"fmt"
	"image"
	"unsafe"

	"golang.org/x/sys/windows"
	"noxa/internal/safecast"
)

var (
	overlayCreateDC  = overlayGDI.NewProc("CreateCompatibleDC")
	overlayDeleteDC  = overlayGDI.NewProc("DeleteDC")
	overlayCreateDIB = overlayGDI.NewProc("CreateDIBSection")
	overlayFlush     = overlayGDI.NewProc("GdiFlush")
	overlayLayered   = user32.NewProc("UpdateLayeredWindow")
)

type overlayBitmapInfo struct {
	Size                        uint32
	Width, Height               int32
	Planes, BitCount            uint16
	Compression, ImageSize      uint32
	XPixels, YPixels            int32
	ColorsUsed, ColorsImportant uint32
}

// The OS thread owns the DIB and its mapped pixels until close. It is never
// shared with the frontend or retained by the Win32 drawing calls.
type overlaySurface struct {
	dc, bitmap, previous uintptr
	pixels               []byte
	width, height        int
}

func newOverlaySurface(width, height int) (*overlaySurface, error) {
	if width < 1 || height < 1 || width > 560 || height > 1216 {
		return nil, fmt.Errorf("invalid overlay surface size %dx%d", width, height)
	}
	w, _ := safecast.IntToInt32(width)
	h, _ := safecast.IntToInt32(height)
	info := overlayBitmapInfo{Size: 40, Width: w, Height: -h, Planes: 1, BitCount: 32}
	s := &overlaySurface{width: width, height: height}
	s.dc, _, _ = overlayCreateDC.Call(0)
	if s.dc == 0 {
		return nil, fmt.Errorf("create overlay device context")
	}
	var pixels unsafe.Pointer
	// #nosec G103 -- DIB returns bounded native memory, released only after deselection.
	s.bitmap, _, _ = overlayCreateDIB.Call(s.dc, uintptr(unsafe.Pointer(&info)), 0, uintptr(unsafe.Pointer(&pixels)), 0, 0)
	if s.bitmap == 0 || pixels == nil {
		s.close()
		return nil, fmt.Errorf("create overlay bitmap")
	}
	s.previous, _, _ = overlaySelect.Call(s.dc, s.bitmap)
	if s.previous == 0 || s.previous == ^uintptr(0) {
		s.previous = 0
		s.close()
		return nil, fmt.Errorf("select overlay bitmap")
	}
	// #nosec G103 -- Width/height are bounded above; this is exactly the allocated 32-bit DIB, owned until close.
	s.pixels = unsafe.Slice((*byte)(pixels), width*height*4)
	return s, nil
}

func (s *overlaySurface) close() {
	if s.previous != 0 {
		_, _, _ = overlaySelect.Call(s.dc, s.previous)
	}
	if s.bitmap != 0 {
		_, _, _ = overlayDelete.Call(s.bitmap)
	}
	if s.dc != 0 {
		_, _, _ = overlayDeleteDC.Call(s.dc)
	}
	s.pixels = nil
}

func nativeOverlayLabel(text string, width, height, fontSize int, centered bool) (*image.Alpha, error) {
	surface, err := newOverlaySurface(width, height)
	if err != nil {
		return nil, err
	}
	defer surface.close()
	clear(surface.pixels)
	face, _ := windows.UTF16PtrFromString("Segoe UI")
	fontHeight, _ := safecast.IntToInt32(-fontSize)
	// ANTIALIASED_QUALITY produces grayscale coverage, avoiding ClearType fringes
	// when compositing transparent text over an unknown desktop background.
	// #nosec G103 G115 -- Win32 reads the retained UTF-16 face synchronously and interprets the low 32 bits as a signed font height.
	font, _, _ := overlayFont.Call(uintptr(fontHeight), 0, 0, 0, 500, 0, 0, 0, 1, 0, 0, 4, 0, uintptr(unsafe.Pointer(face)))
	if font == 0 {
		return nil, fmt.Errorf("create overlay font")
	}
	defer func() { _, _, _ = overlayDelete.Call(font) }()
	previous, _, _ := overlaySelect.Call(surface.dc, font)
	defer func() { _, _, _ = overlaySelect.Call(surface.dc, previous) }()
	_, _, _ = overlayColor.Call(surface.dc, 0x00ffffff)
	_, _, _ = overlayBackground.Call(surface.dc, 1)
	chars, err := windows.UTF16FromString(text)
	if err != nil {
		return nil, fmt.Errorf("overlay label: %w", err)
	}
	w, _ := safecast.IntToInt32(width)
	h, _ := safecast.IntToInt32(height)
	rect := overlayRect{Right: w, Bottom: h}
	flags := uintptr(0x8824) // SINGLELINE | VCENTER | NOPREFIX | END_ELLIPSIS
	if centered {
		flags |= 1
	}
	// #nosec G103 -- DrawText reads the bounded UTF-16 buffer and rectangle synchronously; neither pointer is retained.
	_, _, _ = overlayDraw.Call(surface.dc, uintptr(unsafe.Pointer(&chars[0])), uintptr(len(chars)-1), uintptr(unsafe.Pointer(&rect)), flags)
	_, _, _ = overlayFlush.Call() // Finish GDI writes before accessing the DIB.
	mask := image.NewAlpha(image.Rect(0, 0, width, height))
	for i := range mask.Pix {
		mask.Pix[i] = max(surface.pixels[i*4], surface.pixels[i*4+1], surface.pixels[i*4+2])
	}
	return mask, nil
}

func (s *overlaySurface) present(hwnd uintptr, frame *image.RGBA, x, y int32, opacity int) error {
	for i := 0; i < len(s.pixels); i += 4 {
		// Go RGBA and Win32 both use premultiplied alpha; only channel order differs.
		s.pixels[i], s.pixels[i+1], s.pixels[i+2], s.pixels[i+3] = frame.Pix[i+2], frame.Pix[i+1], frame.Pix[i], frame.Pix[i+3]
	}
	destination := struct{ X, Y int32 }{x, y}
	source := struct{ X, Y int32 }{}
	w, _ := safecast.IntToInt32(s.width)
	h, _ := safecast.IntToInt32(s.height)
	size := struct{ Width, Height int32 }{w, h}
	alpha, _ := safecast.IntToUint8(clampSetting(opacity, 20, 100) * 255 / 100)
	blend := struct{ Operation, Flags, Alpha, Format byte }{Alpha: alpha, Format: 1}
	// #nosec G103 -- UpdateLayeredWindow synchronously consumes these Win32 structs.
	ok, _, err := overlayLayered.Call(hwnd, 0, uintptr(unsafe.Pointer(&destination)), uintptr(unsafe.Pointer(&size)), s.dc, uintptr(unsafe.Pointer(&source)), 0, uintptr(unsafe.Pointer(&blend)), 2)
	if ok == 0 {
		return fmt.Errorf("present overlay: %w", err)
	}
	return nil
}
