// Native visual notifications are silent. All audio follows the frontend bus.
package main

import (
	"sync"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	shellNotifyIcon = windows.NewLazySystemDLL("shell32.dll").NewProc("Shell_NotifyIconW")
	notificationMu  sync.Mutex
)

// notifyIconData matches NOTIFYICONDATAW including pointer-sized alignment.
type notifyIconData struct {
	Size                uint32
	Window              uintptr
	ID, Flags, Callback uint32
	Icon                uintptr
	Tip                 [128]uint16
	State, StateMask    uint32
	Info                [256]uint16
	Timeout             uint32
	Title               [64]uint16
	InfoFlags           uint32
	GUID                windows.GUID
	BalloonIcon         uintptr
}

const notificationNoSound = 0x10

func silentNotificationData(window uintptr, title, text string) notifyIconData {
	data := notifyIconData{
		Window: window, ID: 0x564f4943,
		Flags:     0x10 | 0x40, // NIF_INFO | NIF_REALTIME: discard stale balloons
		InfoFlags: 1 | notificationNoSound,
	}
	data.Size = uint32(unsafe.Sizeof(data))
	copy(data.Title[:63], utf16.Encode([]rune(title)))
	copy(data.Info[:255], utf16.Encode([]rune(text)))
	return data
}

// Notify posts a silent Windows balloon. Native bursts are coalesced rather
// than queued; the in-app notification center retains every notification.
func (a *App) Notify(title, text string) string {
	if !notificationMu.TryLock() {
		return ""
	}
	defer notificationMu.Unlock()
	guid, err := windows.GenerateGUID()
	if err != nil {
		return "native notification identifier unavailable"
	}
	// GUID identity also works while the Wails window is hidden in the tray.
	data := silentNotificationData(findMainWindow(), title, text)
	data.GUID = guid
	data.Flags |= 0x20                                                  // NIF_GUID; use the same identity for add and delete
	ok, _, _ := shellNotifyIcon.Call(0, uintptr(unsafe.Pointer(&data))) // NIM_ADD
	if ok == 0 {
		return "native notification failed"
	}
	defer func() {
		_, _, _ = shellNotifyIcon.Call(2, uintptr(unsafe.Pointer(&data))) // NIM_DELETE
	}()
	time.Sleep(3 * time.Second)
	return ""
}
