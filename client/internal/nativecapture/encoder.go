package nativecapture

import (
	"cmp"
	"fmt"
	"strings"
	"sync"

	"noxa/internal/framecrypt"
)

// encoderInfo describes one H.264 encoder MFT candidate.
type encoderInfo struct {
	name, vendor string
	vendorID     string // MFT_ENUM_HARDWARE_VENDOR_ID_Attribute, e.g. "VEN_1002"
	hardware     bool
	async        bool
	luid         uint64 // MFT_ENUM_ADAPTER_LUID when the activate carries it
	onAdapter    bool   // the MFT belongs to the capture device's adapter
}

// matchesAdapter reports whether a hardware MFT belongs to the adapter
// (luid, PCI vendorID) of the capture device: by MFT_ENUM_ADAPTER_LUID when
// the activate carries one, else by its hardware vendor ID.
// shortcut: two adapters of one vendor without activate LUIDs (an AMD APU
// with a Radeon) both match; openEncoder then refuses a foreign device.
func matchesAdapter(info encoderInfo, luid uint64, vendorID uint32) bool {
	switch {
	case !info.hardware:
		return false
	case info.luid != 0:
		return info.luid == luid
	}
	return vendorID != 0 && strings.EqualFold(info.vendorID, fmt.Sprintf("VEN_%04X", vendorID))
}

func (i encoderInfo) String() string {
	kind := "sw"
	if i.hardware {
		kind = "hw"
	}
	s := fmt.Sprintf("%q vendor=%s %s async=%v", i.name, cmp.Or(i.vendor, "?"), kind, i.async)
	if i.luid != 0 {
		s += fmt.Sprintf(" luid=%016X", i.luid)
	}
	if i.onAdapter {
		s += " on-adapter"
	}
	return s
}

// rankEncoders is the order encoders are tried in: hardware MFTs of the
// capture device's adapter, other hardware MFTs, then software MFTs (the
// Microsoft H.264 encoder) as the last resort, each in Media Foundation's
// merit order. One that wrote multi-slice keyframes earlier is skipped.
func rankEncoders(infos []encoderInfo, rejected func(encoderInfo) bool) []int {
	tier := func(i encoderInfo) int {
		switch {
		case i.hardware && i.onAdapter:
			return 0
		case i.hardware:
			return 1
		}
		return 2
	}
	var order []int
	for t := range 3 {
		for i, info := range infos {
			if tier(info) == t && !rejected(info) {
				order = append(order, i)
			}
		}
	}
	return order
}

// multiSliceEncoders: hardware encoders (by name) whose keyframes came out
// in several slices; newEncoder skips them for the rest of the process.
var multiSliceEncoders sync.Map

// rejectMultiSlice records a hardware encoder that ignored the one-slice
// setting and reports whether its pipeline should reopen another encoder.
// A software encoder is the last resort and never rejected.
func rejectMultiSlice(info encoderInfo) bool {
	if !info.hardware {
		return false
	}
	multiSliceEncoders.Store(info.name, true)
	return true
}

func multiSliceRejected(info encoderInfo) bool {
	_, ok := multiSliceEncoders.Load(info.name)
	return ok && info.hardware
}

// sliceCount is the number of coded slices (NAL types 1 and 5) in an Annex-B access unit.
func sliceCount(au []byte) int {
	n := 0
	for _, nal := range framecrypt.SplitAnnexB(au) {
		if t := nal[0] & 0x1f; t == 1 || t == 5 {
			n++
		}
	}
	return n
}
