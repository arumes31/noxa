package netproto

import "testing"

func intPointer(value int) *int { return &value }

func TestMediaLimitsSetRequiresEveryField(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input MediaLimitsSet
		want  MediaLimits
		valid bool
	}{
		{name: "explicit unlimited", input: MediaLimitsSet{intPointer(0), intPointer(0), intPointer(0)}, valid: true},
		{name: "bounded", input: MediaLimitsSet{intPointer(800000), intPointer(640), intPointer(360)}, want: MediaLimits{800000, 640, 360}, valid: true},
		{name: "missing bitrate", input: MediaLimitsSet{VideoMaxWidth: intPointer(0), VideoMaxHeight: intPointer(0)}},
		{name: "missing width", input: MediaLimitsSet{VideoMaxBitrate: intPointer(0), VideoMaxHeight: intPointer(0)}},
		{name: "missing height", input: MediaLimitsSet{VideoMaxBitrate: intPointer(0), VideoMaxWidth: intPointer(0)}},
		{name: "partial dimensions", input: MediaLimitsSet{intPointer(0), intPointer(640), intPointer(0)}, want: MediaLimits{VideoMaxWidth: 640}},
		{name: "excess bitrate", input: MediaLimitsSet{intPointer(100000001), intPointer(0), intPointer(0)}, want: MediaLimits{VideoMaxBitrate: 100000001}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, valid := tc.input.Limits()
			if valid != tc.valid || got != tc.want {
				t.Fatalf("Limits() = %+v, %v; want %+v, %v", got, valid, tc.want, tc.valid)
			}
		})
	}
}
