package webrtc

import (
	"github.com/pion/webrtc/v4"
	"strconv"
	"strings"
)

func validHEVCLevel(level uint8) bool {
	switch level {
	case 30, 60, 63, 90, 93, 120, 123, 150, 153, 156, 180, 183, 186:
		return true
	}
	return false
}

// The application admits only Main 8-bit, Main tier, SRST without DONL. Pion's
// MIME fallback and level-ignoring H265 matcher are not capability validation.
func admittedH265Level(codec webrtc.RTPCodecCapability) (uint8, bool) {
	if !strings.EqualFold(codec.MimeType, webrtc.MimeTypeH265) || codec.ClockRate != 90000 || len(codec.SDPFmtpLine) > 512 {
		return 0, false
	}
	// A fixed allowlist and bitset keep packet admission allocation-free.
	line := codec.SDPFmtpLine
	var seen uint8
	var level uint8
	for {
		field, rest, more := strings.Cut(line, ";")
		key, value, ok := strings.Cut(strings.TrimSpace(field), "=")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if !ok || key == "" || value == "" {
			return 0, false
		}
		var bit uint8
		switch {
		case strings.EqualFold(key, "profile-id"):
			bit = 1
			if value != "1" {
				return 0, false
			}
		case strings.EqualFold(key, "tier-flag"):
			bit = 2
			if value != "0" {
				return 0, false
			}
		case strings.EqualFold(key, "tx-mode"):
			bit = 4
			if value != "SRST" {
				return 0, false
			}
		case strings.EqualFold(key, "sprop-max-don-diff"):
			bit = 8
			if value != "0" {
				return 0, false
			}
		case strings.EqualFold(key, "interop-constraints"):
			bit = 16
			if len(value) != 12 {
				return 0, false
			}
			for i := range len(value) {
				c := value[i]
				if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
					return 0, false
				}
			}
		case strings.EqualFold(key, "level-id"):
			bit = 32
			if len(value) < 2 || len(value) > 3 || value[0] == '0' {
				return 0, false
			}
			for i := range len(value) {
				if value[i] < '0' || value[i] > '9' {
					return 0, false
				}
			}
			parsed, err := strconv.ParseUint(value, 10, 8)
			if err != nil || !validHEVCLevel(uint8(parsed)) {
				return 0, false
			}
			level = uint8(parsed)
		default:
			return 0, false
		}
		if seen&bit != 0 {
			return 0, false
		}
		seen |= bit
		if !more {
			break
		}
		line = rest
	}
	return level, seen&32 != 0
}
