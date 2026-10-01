package netproto

import (
	"strings"
	"testing"
)

func TestCustomMetadataValidation(t *testing.T) {
	empty, text := "", "value\nwith lines"
	oversized := strings.Repeat("x", MaxCustomMetadataValueBytes+1)
	for _, tc := range []struct {
		name    string
		request CustomMetadataChange
		valid   bool
	}{
		{"set", CustomMetadataChange{UniqueID: "uid", Key: "role", Value: &text}, true},
		{"empty", CustomMetadataChange{UniqueID: "uid", Key: "role", Value: &empty}, true},
		{"delete", CustomMetadataChange{UniqueID: "uid", Key: "role", Delete: true}, true},
		{"ambiguous", CustomMetadataChange{UniqueID: "uid", Key: "role", Value: &empty, Delete: true}, false},
		{"missing action", CustomMetadataChange{UniqueID: "uid", Key: "role"}, false},
		{"missing target", CustomMetadataChange{Key: "role", Value: &text}, false},
		{"key control", CustomMetadataChange{UniqueID: "uid", Key: "role\n", Value: &text}, false},
		{"oversized", CustomMetadataChange{UniqueID: "uid", Key: "role", Value: &oversized}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.request.Valid() != tc.valid {
				t.Fatalf("valid=%v", tc.request.Valid())
			}
		})
	}
	if !(CustomMetadataQuery{UniqueID: "uid"}).Valid() || (CustomMetadataQuery{UniqueID: "uid", Limit: 101}).Valid() || (CustomMetadataQuery{UniqueID: "uid", AfterKey: "\x00"}).Valid() {
		t.Fatal("invalid page limits")
	}
}
