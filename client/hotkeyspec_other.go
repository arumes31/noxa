//go:build !windows

package main

import "golang.design/x/hotkey"

// specModifiers maps modifier names to hotkey modifiers on non-Windows platforms (Linux/macOS).
var specModifiers = map[string]hotkey.Modifier{
	"ctrl":  hotkey.ModCtrl,
	"alt":   hotkey.Mod1,
	"shift": hotkey.ModShift,
	"win":   hotkey.Mod4,
}

// X11 keysyms. Physical punctuation codes need the Windows polling backend.
func platformSpecKey(name string) (hotkey.Key, bool) {
	keys := map[string]hotkey.Key{
		"backspace": 0xff08, "pageup": 0xff55, "pagedown": 0xff56, "home": 0xff50, "end": 0xff57,
		"insert": 0xff63, "capslock": 0xffe5, "numlock": 0xff7f, "scrolllock": 0xff14,
		"pause": 0xff13, "printscreen": 0xff61, "contextmenu": 0xff67,
		"numpad0": 0xffb0, "numpad1": 0xffb1, "numpad2": 0xffb2, "numpad3": 0xffb3, "numpad4": 0xffb4,
		"numpad5": 0xffb5, "numpad6": 0xffb6, "numpad7": 0xffb7, "numpad8": 0xffb8, "numpad9": 0xffb9,
		"numpadmultiply": 0xffaa, "numpadadd": 0xffab, "numpadsubtract": 0xffad, "numpaddecimal": 0xffae, "numpaddivide": 0xffaf,
		"f21": 0xffd2, "f22": 0xffd3, "f23": 0xffd4, "f24": 0xffd5,
	}
	key, ok := keys[name]
	return key, ok
}
