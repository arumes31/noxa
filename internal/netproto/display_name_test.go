package netproto

import (
	"strings"
	"testing"
)

func TestNormalizeDisplayName(t *testing.T) {
	for _, test := range []struct {
		name string
		want string
	}{
		{" Daniel ", "Daniel"},
		{strings.Repeat("ä", 64), strings.Repeat("ä", 64)},
		{"", ""}, {"  ", ""}, {"Daniel\n", ""}, {"Da\x00niel", ""},
		{strings.Repeat("ä", 65), ""}, {string([]byte{0xff}), ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := NormalizeDisplayName(test.name)
			if (err == nil) != (test.want != "") || got != test.want {
				t.Fatalf("NormalizeDisplayName = %q, %v; want %q", got, err, test.want)
			}
		})
	}
}
