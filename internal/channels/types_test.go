package channels

import "testing"

func TestChannelTypeValid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		typeValue ChannelType
		want      bool
	}{
		{name: "temporary", typeValue: ChannelTypeTemporary, want: true},
		{name: "semi-permanent", typeValue: ChannelTypeSemiPermanent, want: true},
		{name: "permanent", typeValue: ChannelTypePermanent, want: true},
		{name: "negative", typeValue: -1},
		{name: "above range", typeValue: ChannelTypePermanent + 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := test.typeValue.Valid(); got != test.want {
				t.Errorf("ChannelType(%d).Valid() = %t, want %t", test.typeValue, got, test.want)
			}
		})
	}
}

func TestParseChannelType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		value     int
		expected  ChannelType
		shouldErr bool
	}{
		{name: "temporary", value: 0, expected: ChannelTypeTemporary},
		{name: "semi-permanent", value: 1, expected: ChannelTypeSemiPermanent},
		{name: "permanent", value: 2, expected: ChannelTypePermanent},
		{name: "negative", value: -1, shouldErr: true},
		{name: "narrowing wraparound", value: 256, shouldErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseChannelType(test.value)
			if test.shouldErr {
				if err == nil {
					t.Fatalf("ParseChannelType(%d) unexpectedly succeeded with %d", test.value, got)
				}
				return
			}
			if err != nil || got != test.expected {
				t.Fatalf("ParseChannelType(%d) = (%d, %v), want (%d, nil)", test.value, got, err, test.expected)
			}
		})
	}
}
