package netproto

import (
	"strings"
	"testing"
)

func TestServerTextBoundariesAndPresence(t *testing.T) {
	for _, tc := range []struct {
		key, value string
		valid      bool
	}{
		{"server_name", "", true}, {"server_name", strings.Repeat("é", 128), true},
		{"server_name", strings.Repeat("é", 129), false}, {"server_name", "a\nb", false}, {"server_name", "a\rb", false},
		{"motd", strings.Repeat("é", 32768), true}, {"announcement", strings.Repeat("é", 32769), false},
		{"server_rules", "Rules\nwith lines", true}, {"motd", "a\x00b", false}, {"motd", "\xff", false}, {"owner_id", "1", false},
	} {
		if got := (ServerTextSet{Key: tc.key, Value: &tc.value}).Valid(); got != tc.valid {
			t.Fatalf("%s bytes=%d: valid=%v want=%v", tc.key, len(tc.value), got, tc.valid)
		}
	}
	if (ServerTextSet{Key: "motd"}).Valid() {
		t.Fatal("omitted value accepted as clear")
	}
}
