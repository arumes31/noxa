//go:build windows

package webview2

import (
	"reflect"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// flattenVtbl lists a vtable struct's ComProc slots in memory order, following
// embedded base vtables the way the COM ABI lays out inherited methods.
func flattenVtbl(t reflect.Type) []string {
	var slots []string
	for n := 0; n < t.NumField(); n++ {
		f := t.Field(n)
		if f.Anonymous && f.Type.Kind() == reflect.Struct {
			slots = append(slots, flattenVtbl(f.Type)...)
			continue
		}
		if f.Type != reflect.TypeOf(ComProc(0)) {
			panic("unexpected vtable field " + t.Name() + "." + f.Name)
		}
		slots = append(slots, f.Name)
	}
	return slots
}

func TestVtableLayoutsMatchSDK(t *testing.T) {
	for name, want := range sdkVtableSizes {
		got := flattenVtbl(want.vtbl)
		if len(got) != want.slots {
			t.Errorf("%s: %d vtable slots, WebView2.h has %d (inherited methods missing?)", name, len(got), want.slots)
		}
		if size := want.vtbl.Size(); size != uintptr(want.slots)*unsafe.Sizeof(ComProc(0)) {
			t.Errorf("%s: vtable is %d bytes, want %d slots", name, size, want.slots)
		}
	}
	for _, tc := range sdkVtableSlots {
		got := flattenVtbl(sdkVtableSizes[tc.iface].vtbl)
		if tc.slot >= len(got) || got[tc.slot] != tc.method {
			t.Errorf("%s.%s: not at WebView2.h slot %d", tc.iface, tc.method, tc.slot)
		}
	}
}

// fakeSettingsArg records the raw BOOL argument a fake put_IsScriptEnabled
// receives.
var fakeSettingsArg uintptr

func TestBoolSettersPassScalarBOOL(t *testing.T) {
	vtbl := &ICoreWebView2SettingsVtbl{}
	vtbl.PutIsScriptEnabled = NewComProc(func(this, value uintptr) uintptr {
		fakeSettingsArg = value
		return uintptr(windows.S_OK)
	})
	settings := &ICoreWebView2Settings{Vtbl: vtbl}
	for _, want := range []bool{false, true} {
		fakeSettingsArg = 0xdead
		if err := settings.PutIsScriptEnabled(want); err != nil {
			t.Fatal(err)
		}
		if fakeSettingsArg != uintptr(boolToInt(want)) {
			t.Fatalf("PutIsScriptEnabled(%v) passed %#x, want scalar %d", want, fakeSettingsArg, boolToInt(want))
		}
	}
}

var fakeOriginsCount uintptr
var fakeOriginsArray **uint16
var fakeOrigins []string

func TestSetAllowedOriginsPassesCountAndPointerArray(t *testing.T) {
	vtbl := &ICoreWebView2CustomSchemeRegistrationVtbl{}
	vtbl.SetAllowedOrigins = NewComProc(func(this, count uintptr, array **uint16) uintptr {
		fakeOriginsCount, fakeOriginsArray = count, array
		fakeOrigins = nil
		if array != nil {
			for _, p := range unsafe.Slice(array, count) {
				fakeOrigins = append(fakeOrigins, UTF16PtrToString(p))
			}
		}
		return uintptr(windows.S_OK)
	})
	reg := &ICoreWebView2CustomSchemeRegistration{Vtbl: vtbl}
	want := []string{"https://a.example", "https://b.example"}
	if err := reg.SetAllowedOrigins(want); err != nil {
		t.Fatal(err)
	}
	if fakeOriginsCount != 2 || !reflect.DeepEqual(fakeOrigins, want) {
		t.Fatalf("native saw count=%d origins=%q", fakeOriginsCount, fakeOrigins)
	}
	if err := reg.SetAllowedOrigins(nil); err != nil {
		t.Fatal(err)
	}
	if fakeOriginsCount != 0 || fakeOriginsArray != nil {
		t.Fatalf("empty origins: count=%d array=%p", fakeOriginsCount, fakeOriginsArray)
	}
}
