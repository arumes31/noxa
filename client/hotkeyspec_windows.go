//go:build windows

package main

import (
	"strings"
	"unicode/utf8"

	"golang.design/x/hotkey"
	"golang.org/x/sys/windows"
)

// Physical punctuation positions are resolved against the foreground layout
// when polled, rather than assuming US OEM virtual-key assignments.
const physicalScanKey hotkey.Key = 1 << 16

var windowsSpecKeys = map[string]hotkey.Key{
	"backspace": 0x08, "pageup": 0x21, "pagedown": 0x22, "end": 0x23, "home": 0x24,
	"insert": 0x2D, "capslock": 0x14, "numlock": 0x90, "scrolllock": 0x91,
	"pause": 0x13, "printscreen": 0x2C, "contextmenu": 0x5D,
	"numpad0": 0x60, "numpad1": 0x61, "numpad2": 0x62, "numpad3": 0x63, "numpad4": 0x64,
	"numpad5": 0x65, "numpad6": 0x66, "numpad7": 0x67, "numpad8": 0x68, "numpad9": 0x69,
	"numpadmultiply": 0x6A, "numpadadd": 0x6B, "numpadsubtract": 0x6D,
	"numpaddecimal": 0x6E, "numpaddivide": 0x6F,
	"f21": 0x84, "f22": 0x85, "f23": 0x86, "f24": 0x87,
	"backquote": physicalScanKey | 0x29, "minus": physicalScanKey | 0x0C,
	"equal": physicalScanKey | 0x0D, "bracketleft": physicalScanKey | 0x1A,
	"bracketright": physicalScanKey | 0x1B, "backslash": physicalScanKey | 0x2B,
	"semicolon": physicalScanKey | 0x27, "quote": physicalScanKey | 0x28,
	"intlbackslash": physicalScanKey | 0x56, "comma": physicalScanKey | 0x33,
	"period": physicalScanKey | 0x34, "slash": physicalScanKey | 0x35,
}

var hotkeyUser32 = windows.NewLazySystemDLL("user32.dll")
var hotkeyMapScan = hotkeyUser32.NewProc("MapVirtualKeyExW")
var hotkeyGetLayout = hotkeyUser32.NewProc("GetKeyboardLayout")
var hotkeyGetForeground = hotkeyUser32.NewProc("GetForegroundWindow")
var hotkeyGetWindowThread = hotkeyUser32.NewProc("GetWindowThreadProcessId")
var hotkeyScanCharacter = hotkeyUser32.NewProc("VkKeyScanExW")

func foregroundKeyboardLayout() uintptr {
	window, _, _ := hotkeyGetForeground.Call()
	thread, _, _ := hotkeyGetWindowThread.Call(window, 0)
	layout, _, _ := hotkeyGetLayout.Call(thread)
	return layout
}

func platformSpecKey(name string) ([]hotkey.Modifier, hotkey.Key, bool) {
	if key, ok := windowsSpecKeys[name]; ok {
		return nil, key, true
	}
	// Accept old hand-authored DE/EN character bindings as well as new codes.
	if utf8.RuneCountInString(name) == 1 && strings.ContainsRune("öäüß+-.,#;:!?\"'[]{}()=<>/\\`~^&*%$@€|", []rune(name)[0]) {
		vk, _, _ := hotkeyScanCharacter.Call(uintptr([]rune(name)[0]), foregroundKeyboardLayout())
		return scannedCharacterKey(uint16(vk))
	}
	return nil, 0, false
}

// scannedCharacterKey splits a VkKeyScanExW result: the low byte is the
// virtual key and the high byte the shift state the character needs (1 =
// Shift, 2 = Ctrl, 4 = Alt; Ctrl+Alt is AltGr). Dropping that state would
// bind "!" to a plain "1" and fire the action on ordinary typing.
func scannedCharacterKey(vk uint16) ([]hotkey.Modifier, hotkey.Key, bool) {
	if vk == 0xFFFF {
		return nil, 0, false
	}
	state := vk >> 8
	if state&^7 != 0 {
		return nil, 0, false // Hankaku or a reserved state: not a bindable chord
	}
	var mods []hotkey.Modifier
	if state&1 != 0 {
		mods = append(mods, hotkey.ModShift)
	}
	if state&2 != 0 {
		mods = append(mods, hotkey.ModCtrl)
	}
	if state&4 != 0 {
		mods = append(mods, hotkey.ModAlt)
	}
	return mods, hotkey.Key(vk & 0xFF), true
}

// specModifiers maps modifier names to hotkey modifiers on Windows.
var specModifiers = map[string]hotkey.Modifier{
	"ctrl":  hotkey.ModCtrl,
	"alt":   hotkey.ModAlt,
	"shift": hotkey.ModShift,
	"win":   hotkey.ModWin,
}
