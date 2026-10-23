package netproto

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// ValidFileLeaf applies the same portable filename policy on every platform.
// In particular, Windows device paths and alternate data streams are never
// accepted as a remote name, even when the server runs on Linux.
func ValidFileLeaf(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > 255 || !utf8.ValidString(name) || strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") || strings.ContainsAny(name, `/\:<>"|?*`) {
		return false
	}
	if strings.ContainsFunc(name, func(r rune) bool { return unicode.IsControl(r) || DeceptiveNameRune(r) }) {
		return false
	}
	stem, _, _ := strings.Cut(name, ".")
	stem = strings.ToUpper(strings.TrimRight(stem, " ."))
	switch stem {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
		return false
	}
	for _, prefix := range []string{"COM", "LPT"} {
		if strings.HasPrefix(stem, prefix) {
			switch strings.TrimPrefix(stem, prefix) {
			case "1", "2", "3", "4", "5", "6", "7", "8", "9", "¹", "²", "³":
				return false
			}
		}
	}
	return true
}

// DeceptiveNameRune excludes bidi overrides/isolation and invisible separators
// from identifiers. ZWJ/ZWNJ remain valid for joined emoji and ordinary scripts.
func DeceptiveNameRune(r rune) bool {
	return (unicode.Is(unicode.Cf, r) && r != 0x200c && r != 0x200d) || r == 0x034f || r == 0x2028 || r == 0x2029
}

// ValidIdentifierText retains real Unicode names while excluding control and
// invisible formatting that can impersonate another displayed identifier.
func ValidIdentifierText(value string) bool {
	return utf8.ValidString(value) && !strings.ContainsFunc(value, func(r rune) bool { return unicode.IsControl(r) || DeceptiveNameRune(r) })
}
