//go:build windows

package main

import (
	"slices"
	"testing"

	"golang.design/x/hotkey"
)

// A VkKeyScanExW shift state must become part of the chord: "!" on a US
// layout is Shift+1, never a plain "1".
func TestScannedCharacterKeyKeepsShiftState(t *testing.T) {
	tests := []struct {
		name string
		vk   uint16
		mods []hotkey.Modifier
		key  hotkey.Key
		ok   bool
	}{
		{"unshifted", 0x00BC, nil, 0xBC, true},
		{"shift", 0x0131, []hotkey.Modifier{hotkey.ModShift}, 0x31, true},
		{"altgr", 0x0645, []hotkey.Modifier{hotkey.ModCtrl, hotkey.ModAlt}, 0x45, true},
		{"unmapped", 0xFFFF, nil, 0, false},
		{"reserved state", 0x0831, nil, 0, false},
	}
	for _, tt := range tests {
		mods, key, ok := scannedCharacterKey(tt.vk)
		if ok != tt.ok || key != tt.key || !slices.Equal(mods, tt.mods) {
			t.Errorf("%s: scannedCharacterKey(%#x) = %v %#x %v, want %v %#x %v", tt.name, tt.vk, mods, key, ok, tt.mods, tt.key, tt.ok)
		}
	}
}
