//go:build windows

package main

import (
	"fmt"
	"log"
	"os"
	"time"
	"unsafe"
)

// The verified replacement runs this helper while its authenticated parent is
// still alive. This also supports updates FROM a version without window handoff.
func inheritSupervisorUpdateWindow(parent uint32) {
	if _, err := decodeUpdateWindow(os.Getenv(updateWindowEnv), time.Now()); err == nil {
		return
	}
	p, err := captureUpdateWindowHandle(findWindowForProcess(parent, "wailsWindow", true))
	if err != nil {
		log.Printf("update parent window capture: %v", err)
		return
	}
	storeUpdateWindow(p)
}

type updateWindowPoint struct{ X, Y int32 }
type nativeUpdateWindowPlacement struct {
	Length, Flags, ShowCmd   uint32
	MinPosition, MaxPosition updateWindowPoint
	NormalPosition           struct{ Left, Top, Right, Bottom int32 }
}

func captureUpdateWindowHandle(hwnd uintptr) (*updateWindowPlacement, error) {
	if hwnd == 0 {
		return nil, nil
	}
	p := nativeUpdateWindowPlacement{}
	p.Length = uint32(unsafe.Sizeof(p))
	ok, _, err := user32.NewProc("GetWindowPlacement").Call(hwnd, uintptr(unsafe.Pointer(&p)))
	if ok == 0 {
		return nil, fmt.Errorf("GetWindowPlacement: %w", err)
	}
	r := p.NormalPosition
	return &updateWindowPlacement{Left: r.Left, Top: r.Top, Right: r.Right, Bottom: r.Bottom,
		Maximized: p.ShowCmd == 3 || p.Flags&2 != 0}, nil
}

func applyUpdateWindowHandle(hwnd uintptr, saved updateWindowPlacement) error {
	if hwnd == 0 {
		return fmt.Errorf("main window unavailable")
	}
	p := nativeUpdateWindowPlacement{}
	p.Length = uint32(unsafe.Sizeof(p))
	p.ShowCmd = 1
	if saved.Maximized {
		p.ShowCmd = 3
	}
	p.NormalPosition.Left, p.NormalPosition.Top = saved.Left, saved.Top
	p.NormalPosition.Right, p.NormalPosition.Bottom = saved.Right, saved.Bottom
	// Win32 restores workspace coordinates, the normal rectangle and maximized
	// state together. It moves an off-screen rectangle onto an available monitor
	// if the original display was disconnected during the update.
	ok, _, err := user32.NewProc("SetWindowPlacement").Call(hwnd, uintptr(unsafe.Pointer(&p)))
	if ok == 0 {
		return fmt.Errorf("SetWindowPlacement: %w", err)
	}
	return nil
}

func platformCaptureUpdateWindow() (*updateWindowPlacement, error) {
	return captureUpdateWindowHandle(findOpacityWindow())
}
func platformApplyUpdateWindow(saved updateWindowPlacement) error {
	return applyUpdateWindowHandle(findOpacityWindow(), saved)
}
