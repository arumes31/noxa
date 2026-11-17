package framecrypt

import (
	"bytes"
	"errors"
)

var startCode = []byte{0, 0, 0, 1}

// SplitAnnexB returns the NAL units of an Annex-B stream without start codes
// or trailing_zero_8bits. It returns nil when data precedes the first start code.
func SplitAnnexB(b []byte) [][]byte {
	short := []byte{0, 0, 1}
	first := bytes.Index(b, short)
	if first < 0 || len(bytes.Trim(b[:first], "\x00")) != 0 {
		return nil
	}
	var nals [][]byte
	for rest := b[first+3:]; ; {
		next := bytes.Index(rest, short)
		end := next
		if next < 0 {
			end = len(rest)
		}
		// Baseline (CAVLC) slices end with the RBSP stop bit, so trailing
		// zero bytes are never NAL payload.
		if nal := bytes.TrimRight(rest[:end], "\x00"); len(nal) > 0 {
			nals = append(nals, nal)
		}
		if next < 0 {
			return nals
		}
		rest = rest[next+3:]
	}
}

// CanonicalH264 rewrites one encoder access unit into the form the SFU
// admits and receivers rebuild: 4-byte start codes, exactly one slice, and on
// a keyframe SPS, PPS, IDR (sps/pps fill in when the encoder omitted them).
// SEI, AUD, filler and prefix NALs are dropped: pion's payloader drops AUD and
// filler, so keeping them would change the authenticated clear header.
func CanonicalH264(au []byte, sps, pps []byte) (out []byte, keyframe bool, err error) {
	nals := SplitAnnexB(au)
	if nals == nil {
		return nil, false, errors.New("framecrypt: not an Annex-B access unit")
	}
	var slice, inSPS, inPPS []byte
	for _, nal := range nals {
		if nal[0]&0x80 != 0 {
			return nil, false, errors.New("framecrypt: forbidden_zero_bit set")
		}
		switch nal[0] & 0x1f {
		case 1, 5:
			if slice != nil {
				return nil, false, errors.New("framecrypt: more than one slice in access unit")
			}
			slice = nal
		case 7:
			if inSPS != nil {
				return nil, false, errors.New("framecrypt: duplicate SPS")
			}
			inSPS = nal
		case 8:
			if inPPS != nil {
				return nil, false, errors.New("framecrypt: duplicate PPS")
			}
			inPPS = nal
		case 6, 9, 12, 14:
		default:
			return nil, false, errors.New("framecrypt: unsupported H.264 NAL type")
		}
	}
	if slice == nil {
		return nil, false, errors.New("framecrypt: access unit has no slice")
	}
	if slice[0]&0x1f != 5 {
		return append(append([]byte(nil), startCode...), slice...), false, nil
	}
	if inSPS == nil {
		inSPS = bytes.TrimRight(sps, "\x00")
	}
	if inPPS == nil {
		inPPS = bytes.TrimRight(pps, "\x00")
	}
	if len(inSPS) == 0 || len(inPPS) == 0 || inSPS[0]&0x1f != 7 || inPPS[0]&0x1f != 8 {
		return nil, false, errors.New("framecrypt: keyframe without SPS/PPS")
	}
	out = make([]byte, 0, 12+len(inSPS)+len(inPPS)+len(slice))
	for _, nal := range [][]byte{inSPS, inPPS, slice} {
		out = append(append(out, startCode...), nal...)
	}
	return out, true, nil
}
