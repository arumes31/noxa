//go:build windows

package main

import (
	"testing"
	"time"
	"unsafe"

	"golang.design/x/hotkey"
	"golang.org/x/sys/windows"
)

func TestWindowsNumpadAndNavigationPolling(t *testing.T) {
	original := keyPressed
	t.Cleanup(func() { keyPressed = original })
	for _, tc := range []struct {
		spec string
		vk   uintptr
	}{
		{"PageUp", 0x21}, {"PageDown", 0x22}, {"Home", 0x24}, {"End", 0x23},
		{"Insert", 0x2D}, {"Backspace", 0x08}, {"F24", 0x87}, {"NumpadAdd", 0x6B},
		{"NumpadDecimal", 0x6E}, {"AudioVolumeMute", 0xAD},
	} {
		_, key, err := parseHotkeySpec(tc.spec)
		if err != nil {
			t.Fatal(err)
		}
		keyPressed = func(vk uintptr) bool { return vk == tc.vk }
		if !windowsChordPressed(nil, key) {
			t.Errorf("%s did not poll VK 0x%X", tc.spec, tc.vk)
		}
	}
	_, num, _ := parseHotkeySpec("Numpad7")
	_, row, _ := parseHotkeySpec("7")
	keyPressed = func(vk uintptr) bool { return vk == 0x67 }
	if !windowsChordPressed(nil, num) || windowsChordPressed(nil, row) {
		t.Fatal("numpad digit aliased the main number row")
	}
}

func TestWindowsPunctuationPositionsOnDEAndENLayouts(t *testing.T) {
	for _, tc := range []struct {
		layout string
		chars  map[string]rune
	}{
		{"00000407", map[string]rune{"Semicolon": 'ö', "Quote": 'ä', "BracketLeft": 'ü', "BracketRight": '+', "Backslash": '#', "Slash": '-', "Minus": 'ß', "Comma": ',', "Period": '.', "IntlBackslash": '<'}},
		{"00000409", map[string]rune{"Semicolon": ';', "Quote": '\'', "BracketLeft": '[', "BracketRight": ']', "Backslash": '\\', "Slash": '/', "Minus": '-', "Equal": '=', "Backquote": '`', "Comma": ',', "Period": '.'}},
	} {
		t.Run(tc.layout, func(t *testing.T) {
			name, err := windows.UTF16PtrFromString(tc.layout)
			if err != nil {
				t.Fatal(err)
			}
			// Loading without KLF_ACTIVATE leaves the user's active layout intact.
			layout, _, _ := hotkeyUser32.NewProc("LoadKeyboardLayoutW").Call(uintptr(unsafe.Pointer(name)), 0)
			if layout == 0 {
				t.Fatal("test keyboard layout unavailable")
			}
			for spec, char := range tc.chars {
				_, key, err := parseHotkeySpec(spec)
				if err != nil {
					t.Fatal(err)
				}
				vk, _, _ := hotkeyMapScan.Call(uintptr(key&^physicalScanKey), 3, layout)
				characterVK, _, _ := hotkeyScanCharacter.Call(uintptr(char), layout)
				if uint16(characterVK) == 0xFFFF || vk != characterVK&0xFF {
					t.Errorf("%s (%c) mapped to 0x%X, character maps to 0x%X", spec, char, vk, characterVK)
				}
			}
		})
	}
}

func TestWindowsChordPressedRequiresKeyAndModifiers(t *testing.T) {
	original := keyPressed
	t.Cleanup(func() { keyPressed = original })
	pressed := map[uintptr]bool{}
	keyPressed = func(key uintptr) bool { return pressed[key] }

	mods := []hotkey.Modifier{hotkey.ModCtrl, hotkey.ModShift}
	key := hotkey.KeyM
	vk, _ := windowsVirtualKey(key)
	if windowsChordPressed(mods, key) {
		t.Fatal("empty keyboard state matched the chord")
	}
	pressed[vk] = true
	if windowsChordPressed(mods, key) {
		t.Fatal("main key without modifiers matched the chord")
	}
	pressed[vkControl] = true
	if windowsChordPressed(mods, key) {
		t.Fatal("partially held modifiers matched the chord")
	}
	pressed[vkShift] = true
	if !windowsChordPressed(mods, key) {
		t.Fatal("fully held chord did not match")
	}
	delete(pressed, vk)
	if windowsChordPressed(mods, key) {
		t.Fatal("released main key still matched the chord")
	}
}

func TestWindowsChordPressedAcceptsEitherWinKey(t *testing.T) {
	original := keyPressed
	t.Cleanup(func() { keyPressed = original })
	tabVk, _ := windowsVirtualKey(hotkey.KeyTab)
	pressed := map[uintptr]bool{tabVk: true, vkRWin: true}
	keyPressed = func(key uintptr) bool { return pressed[key] }

	if !windowsChordPressed([]hotkey.Modifier{hotkey.ModWin}, hotkey.KeyTab) {
		t.Fatal("right Windows key did not satisfy Win modifier")
	}
}

func TestWindowsVirtualKeyMapping(t *testing.T) {
	tests := []struct {
		key    hotkey.Key
		wantVK uintptr
	}{
		{hotkey.KeyM, 0x4D},
		{hotkey.KeyTab, 0x09},
		{hotkey.KeyUp, 0x26},
		{hotkey.KeyDown, 0x28},
		{hotkey.KeyLeft, 0x25},
		{hotkey.KeyRight, 0x27},
		{hotkey.KeyF20, 0x83},
	}

	for _, tt := range tests {
		vk, ok := windowsVirtualKey(tt.key)
		if !ok {
			t.Errorf("windowsVirtualKey(%v) returned ok=false", tt.key)
			continue
		}
		if vk != tt.wantVK {
			t.Errorf("windowsVirtualKey(%v) = 0x%X, want 0x%X", tt.key, vk, tt.wantVK)
		}
	}
}

func TestPassiveHotkeyLoopStopsWhenUnbound(t *testing.T) {
	a := &App{hotkeys: map[string]*hotkeyReg{}}
	done := make(chan struct{})
	go func() {
		a.passiveHotkeyLoop("ptt", nil, hotkey.KeyF20, 0)
		close(done)
	}()

	deadline := time.Now().Add(time.Second)
	for {
		a.hkMu.Lock()
		_, registered := a.hotkeys["ptt"]
		a.hkMu.Unlock()
		if registered {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("passive hotkey did not start")
		}
		time.Sleep(time.Millisecond)
	}

	a.applyHotkey("ptt", "")
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("passive hotkey did not stop after unbind")
	}
}
