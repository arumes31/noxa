package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestTrayIconsLoadAtWindowsTraySize(t *testing.T) {
	user32 := windows.NewLazySystemDLL("user32.dll")
	loadImage := user32.NewProc("LoadImageW")
	destroyIcon := user32.NewProc("DestroyIcon")
	dir := t.TempDir()
	for mode, data := range trayIcons() {
		path := filepath.Join(dir, strconv.Itoa(int(mode))+".ico")
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		name, err := windows.UTF16PtrFromString(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, size := range []uintptr{16, 20, 24, 32} {
			// IMAGE_ICON + LR_LOADFROMFILE, matching the native tray loader.
			handle, _, err := loadImage.Call(0, uintptr(unsafe.Pointer(name)), 1, size, size, 0x10)
			if handle == 0 {
				t.Fatalf("load tray state %d at %dpx: %v", mode, size, err)
			}
			if ok, _, err := destroyIcon.Call(handle); ok == 0 {
				t.Fatalf("destroy tray state %d at %dpx: %v", mode, size, err)
			}
		}
	}
}
