package webrtc

import (
	"strconv"
	"strings"

	"github.com/pion/webrtc/v4"
)

// singleFormatParameter does not allocate on the per-packet admission path.
// Duplicate admission parameters (including case variants) are ambiguous even
// when their values agree. Other optional fmtp parameters remain untouched.
func singleFormatParameter(line, name string) (string, bool) {
	var value string
	found := false
	for line != "" {
		var parameter string
		parameter, line, _ = strings.Cut(line, ";")
		key, candidate, hasValue := strings.Cut(strings.TrimSpace(parameter), "=")
		if !strings.EqualFold(strings.TrimSpace(key), name) {
			continue
		}
		if found || !hasValue {
			return "", false
		}
		value, found = strings.TrimSpace(candidate), true
	}
	return value, found
}

// The existing h264 capability means Baseline 4200xx, mode 1, 90 kHz.
// Pion's MIME-only fallback is not a profile admission check. Keep the level
// byte and optional parameters unchanged; this is not bitstream validation.
func admittedH264Codec(codec webrtc.RTPCodecCapability) bool {
	if !strings.EqualFold(codec.MimeType, webrtc.MimeTypeH264) || codec.ClockRate != 90000 {
		return false
	}
	mode, ok := singleFormatParameter(codec.SDPFmtpLine, "packetization-mode")
	if !ok || mode != "1" {
		return false
	}
	profile, ok := singleFormatParameter(codec.SDPFmtpLine, "profile-level-id")
	if !ok || len(profile) != 6 || !strings.EqualFold(profile[:4], "4200") {
		return false
	}
	for i := 4; i < 6; i++ {
		c := profile[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

// Repair is retained only when its unambiguous apt names a retained primary.
func repairFormatPayload(codec webrtc.RTPCodecCapability) (webrtc.PayloadType, bool) {
	apt, ok := singleFormatParameter(codec.SDPFmtpLine, "apt")
	if !ok || apt == "" || codec.ClockRate != 90000 {
		return 0, false
	}
	for i := range len(apt) {
		if apt[i] < '0' || apt[i] > '9' {
			return 0, false
		}
	}
	payload, err := strconv.ParseUint(apt, 10, 7)
	return webrtc.PayloadType(payload), err == nil
}

// Owns no shared slices; the caller clones Pion's parameters before filtering.
func admittedVideoFormats(codecs []webrtc.RTPCodecParameters, vp8Only bool) []webrtc.RTPCodecParameters {
	primary := make(map[webrtc.PayloadType]bool, len(codecs))
	counts := make(map[webrtc.PayloadType]int, len(codecs))
	for _, codec := range codecs {
		counts[codec.PayloadType]++
	}
	for _, codec := range codecs {
		if counts[codec.PayloadType] != 1 {
			continue
		}
		switch {
		case strings.EqualFold(codec.MimeType, webrtc.MimeTypeRTX):
		case vp8Only && !strings.EqualFold(codec.MimeType, webrtc.MimeTypeVP8):
		case strings.EqualFold(codec.MimeType, webrtc.MimeTypeH264) && !admittedH264Codec(codec.RTPCodecCapability):
		case strings.EqualFold(codec.MimeType, webrtc.MimeTypeH265):
			_, primary[codec.PayloadType] = admittedH265Level(codec.RTPCodecCapability)
		default:
			primary[codec.PayloadType] = true
		}
	}
	retained := codecs[:0]
	for _, codec := range codecs {
		if counts[codec.PayloadType] != 1 {
			continue
		}
		if strings.EqualFold(codec.MimeType, webrtc.MimeTypeRTX) {
			if apt, ok := repairFormatPayload(codec.RTPCodecCapability); ok && primary[apt] {
				retained = append(retained, codec)
			}
		} else if primary[codec.PayloadType] {
			retained = append(retained, codec)
		}
	}
	return retained
}
