// protocol.go implements the TS3-style text encoding of the ServerQuery
// protocol: value escaping and the error-line format.
package query

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ErrTokenNotFound is returned when a token key does not exist.
var ErrTokenNotFound = fmt.Errorf("token not found")

// Error IDs (TS3-flavored).
const (
	errOK                      = 0
	errUnknownCommand          = 256
	errInvalidParameter        = 512
	errLoginFailed             = 520
	errRoleConflict            = 521
	errServerError             = 1024
	errInsufficientPermissions = 2568
	errTooManyConnections      = 1539
	errChannelFull             = 777 // TS3 ERROR_channel_maxclients_reached (0x0309)
)

// escapeReplacer maps the special characters to their TS3-style escapes.
// Backslash must be replaced first (strings.NewReplacer handles this by
// matching left-to-right with earliest match, and our patterns don't overlap).
var escapeReplacer = strings.NewReplacer(
	`\`, `\\`,
	` `, `\s`,
	`|`, `\p`,
	`/`, `\/`,
	"\n", `\n`,
	"\r", `\r`,
	"\t", `\t`,
)

// escape encodes a value for the query protocol. Control characters without
// a protocol escape (ESC, BEL, DEL, C1 controls ...) are dropped: values such
// as channel topics are user-controlled and reach administrators' terminals,
// where raw escape sequences would be interpreted.
func escape(s string) string {
	s = escapeReplacer.Replace(s)
	// Removing a control can join the bytes around it into a new one (an
	// invalid lead byte followed by a continuation byte), so repeat until
	// none remain; every pass shrinks s.
	for strings.IndexFunc(s, unicode.IsControl) >= 0 {
		s = dropControls(s)
	}
	return s
}

// dropControls removes control characters, keeping invalid UTF-8 bytes as
// they are (strings.Map would widen each to U+FFFD).
func dropControls(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError || !unicode.IsControl(r) {
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String()
}

// unescape decodes a TS3-style escaped value. Unknown escape sequences keep
// the character after the backslash.
func unescape(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 's':
			b.WriteByte(' ')
		case 'p':
			b.WriteByte('|')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case '/', '\\':
			b.WriteByte(s[i])
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// errorLine formats the terminating status line of a response.
func errorLine(id int, msg string) string {
	return fmt.Sprintf("error id=%d msg=%s\n", id, escape(msg))
}
