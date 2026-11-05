package query

import (
	"strings"
	"testing"
	"unicode"
)

const maxFuzzQueryInput = 64 << 10

func FuzzEscapeUnescape(f *testing.F) {
	for _, input := range []string{
		"",
		"plain",
		"all of\\them|mixed /up\n\r\t",
		`unknown\zescape\`,
		"nul\x00byte",
		"Grüße 世界",
		string([]byte{0xff, 0xfe, '\\', 's'}),
	} {
		f.Add(input)
	}

	f.Fuzz(func(t *testing.T, input string) {
		if len(input) > maxFuzzQueryInput {
			t.Skip()
		}

		encoded := escape(input)
		if len(encoded) > 2*len(input) {
			t.Fatalf("escape expanded %d bytes to %d bytes", len(input), len(encoded))
		}
		// Control characters without a protocol escape are dropped.
		// Control characters without a protocol escape are dropped, so only
		// inputs free of them must round-trip exactly.
		if !strings.ContainsFunc(input, func(r rune) bool { return unicode.IsControl(r) && !strings.ContainsRune(escapedControls, r) }) {
			if got := unescape(encoded); got != input {
				t.Fatalf("unescape(escape(%q)) = %q", input, got)
			}
		}
		if strings.IndexFunc(encoded, unicode.IsControl) >= 0 {
			t.Fatalf("escape(%q) = %q carries a raw control character", input, encoded)
		}

		// unescape is also called directly on untrusted query tokens. It may
		// remove escape markers, but it must never expand the input.
		if got := unescape(input); len(got) > len(input) {
			t.Fatalf("unescape expanded %d bytes to %d bytes", len(input), len(got))
		}
	})
}

const escapedControls = "\n\r\t"
