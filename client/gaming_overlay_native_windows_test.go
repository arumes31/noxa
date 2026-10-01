//go:build windows

package main

import (
	"os"
	"testing"
	"time"
	"unsafe"
)

// This opt-in desktop test creates only the owned, click-through overlay.
func TestNativeGamingOverlay(t *testing.T) {
	if os.Getenv("NOXA_TEST_NATIVE_OVERLAY") != "1" {
		t.Skip("requires an interactive Windows desktop")
	}
	window, err := newNativeGamingOverlay()
	if err != nil {
		t.Fatal(err)
	}
	defer window.Close()
	window.Update(normalizeGamingOverlay(GamingOverlaySnapshot{Active: true, Title: "Overlay UI test", Status: "Muted", Speakers: []GamingOverlaySpeaker{{Name: "Alice", Speaking: true}}}))
	var hwnd uintptr
	overlayWindows.Range(func(key, _ any) bool { hwnd = key.(uintptr); return false })
	if hwnd == 0 {
		t.Fatal("overlay window not registered")
	}
	visible := func() bool { value, _, _ := procIsWindowVisible.Call(hwnd); return value != 0 }
	deadline := time.Now().Add(2 * time.Second)
	for !visible() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !visible() {
		t.Fatal("active overlay did not become visible")
	}
	style, _, _ := user32.NewProc("GetWindowLongW").Call(hwnd, ^uintptr(19))
	if style&0x080800A8 != 0x080800A8 {
		t.Fatalf("overlay is not topmost/click-through/noactivate: %#x", style)
	}
	monitors := nativeGamingOverlayMonitors()
	if len(monitors) == 0 {
		t.Fatal("no Windows display found")
	}
	preview := normalizeGamingOverlay(GamingOverlaySnapshot{Active: true, Title: "Preview", Monitor: monitors[0].ID, Position: "custom", X: 100, Y: 100, Scale: 150, Opacity: 40})
	window.Preview(preview)
	window.Update(GamingOverlaySnapshot{}) // routine idle poll must not hide a preview
	x, y, width, height := gamingOverlayPlacement(preview, monitors[0])
	deadline = time.Now().Add(2 * time.Second)
	var rect overlayRect
	for time.Now().Before(deadline) {
		_, _, _ = user32.NewProc("GetWindowRect").Call(hwnd, uintptr(unsafe.Pointer(&rect)))
		if rect.Left == x && rect.Top == y && rect.Right-rect.Left == width && rect.Bottom-rect.Top == height {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !visible() || rect.Left != x || rect.Top != y || rect.Right-rect.Left != width || rect.Bottom-rect.Top != height {
		t.Fatalf("preview layout did not reach native window: %+v", rect)
	}
	var alpha byte
	_, _, _ = user32.NewProc("GetLayeredWindowAttributes").Call(hwnd, 0, uintptr(unsafe.Pointer(&alpha)), 0)
	if alpha != 102 {
		t.Fatalf("native opacity = %d, want 102", alpha)
	}
	// Expiry restores the regular inactive snapshot without a UI heartbeat.
	native := window.(*nativeOverlay)
	native.mu.Lock()
	native.previewUntil = time.Now().Add(-time.Second)
	native.mu.Unlock()
	window.Update(GamingOverlaySnapshot{})
	deadline = time.Now().Add(2 * time.Second)
	for visible() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if visible() {
		t.Fatal("inactive overlay did not hide")
	}
}
