package nativecapture

import (
	"slices"
	"testing"
)

func TestRankEncodersPrefersTheCaptureAdapter(t *testing.T) {
	// A hybrid laptop capturing the Radeon's screen: one AMD MFT per adapter.
	infos := []encoderInfo{
		{name: "NVIDIA NVENC", hardware: true},
		{name: "AMD AMF", hardware: true, luid: 1}, // the APU
		{name: "AMD AMF", hardware: true, luid: 7, onAdapter: true},
		{name: "Microsoft H.264 Encoder"},
	}
	none := func(encoderInfo) bool { return false }
	if got, want := rankEncoders(infos, none), []int{2, 0, 1, 3}; !slices.Equal(got, want) {
		t.Fatalf("order %v, want %v", got, want)
	}
	// A hardware encoder that wrote multi-slice keyframes is skipped next time;
	// the software encoder stays the last resort.
	amd := func(i encoderInfo) bool { return i.name == "AMD AMF" }
	if got, want := rankEncoders(infos, amd), []int{0, 3}; !slices.Equal(got, want) {
		t.Fatalf("order without AMD %v, want %v", got, want)
	}
	if got := rankEncoders([]encoderInfo{{name: "Microsoft H.264 Encoder"}}, none); !slices.Equal(got, []int{0}) {
		t.Fatalf("software only: %v", got)
	}
}

func TestMatchesAdapter(t *testing.T) {
	const luid, intel, amd = 0xD881, 0x8086, 0x1002
	cases := []struct {
		name string
		info encoderInfo
		want bool
	}{
		{"activate LUID matches", encoderInfo{hardware: true, luid: luid, vendorID: "VEN_1002"}, true},
		{"activate LUID of another GPU wins over the vendor", encoderInfo{hardware: true, luid: 0x1234, vendorID: "VEN_8086"}, false},
		{"no LUID: vendor ID matches", encoderInfo{hardware: true, vendorID: "ven_8086"}, true},
		{"no LUID: another vendor", encoderInfo{hardware: true, vendorID: "VEN_10DE"}, false},
		{"software never belongs to an adapter", encoderInfo{vendorID: "VEN_8086"}, false},
	}
	for _, c := range cases {
		if got := matchesAdapter(c.info, luid, intel, true); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	if matchesAdapter(encoderInfo{hardware: true, vendorID: "VEN_8086"}, luid, amd, true) {
		t.Error("Intel MFT matched an AMD device")
	}
	// With the per-adapter enumeration available, only a LUID matches.
	if matchesAdapter(encoderInfo{hardware: true, vendorID: "VEN_8086"}, luid, intel, false) {
		t.Error("vendor match used although MFTEnum2 listed the adapter's MFTs")
	}
}

func TestMultiSliceReopenDecision(t *testing.T) {
	nal := func(header byte, body ...byte) []byte { return append([]byte{0, 0, 0, 1, header}, body...) }
	one := slices.Concat(nal(0x67, 1), nal(0x68, 2), nal(0x65, 3))
	two := slices.Concat(nal(0x67, 1), nal(0x68, 2), nal(0x65, 3), nal(0x65, 4))
	if sliceCount(one) != 1 || sliceCount(two) != 2 || sliceCount(nal(0x41, 5)) != 1 {
		t.Fatalf("slice counts %d %d", sliceCount(one), sliceCount(two))
	}
	hw := encoderInfo{name: "test multi-slice hw", hardware: true}
	if multiSliceRejected(hw) {
		t.Fatal("rejected before any output")
	}
	if !rejectMultiSlice(hw) || !multiSliceRejected(hw) {
		t.Fatal("a multi-slice hardware encoder is not reopened")
	}
	sw := encoderInfo{name: "test multi-slice sw"}
	if rejectMultiSlice(sw) || multiSliceRejected(sw) {
		t.Fatal("the software last resort was rejected")
	}
}
