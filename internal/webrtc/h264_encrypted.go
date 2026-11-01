package webrtc

import "bytes"

// Only the Annex-B prefix through the first slice header remains clear under
// the maintained frame cipher. Ciphertext must never enter the plaintext
// slice syntax parser. Header checks enforce declared dimensions, not media
// authenticity; only receiving clients can authenticate the encrypted frame.
func validH264TemporalPrefix(nal []byte) bool {
	return len(nal) >= 5 && len(nal) <= 16 && nal[0]&0x80 == 0 && nal[0]&31 == 14 &&
		nal[1]&0x80 != 0 && nal[2] == 0x80 && nal[3]&0x10 == 0 && nal[3]&3 == 3 && nal[3]>>5 <= 2
}

func (v *h264BoundsInspector) finishEncrypted() (*h264AccessUnit, error) {
	var unit *h264AccessUnit
	var prefix []byte
	spsSeen, ppsSeen := false, false
	for _, nal := range v.nals {
		switch nal[0] & 31 {
		case 7:
			if unit != nil || spsSeen || len(nal) > h264ParameterBytes {
				return nil, errH264Bounds
			}
			// Annex-B trailing_zero_8bits are not SPS syntax. Inspect a
			// trimmed view, but retain the exact authenticated header bytes.
			id, config, err := parseH264SPS(bytes.TrimRight(nal, "\x00"), v.bounds)
			if err != nil {
				return nil, err
			}
			config.raw = bytes.Clone(nal)
			if !bytes.Equal(v.sps[id].raw, nal) {
				v.known = false
			}
			v.sps = map[uint32]h264SPS{id: config}
			spsSeen = true
		case 8:
			if unit != nil || ppsSeen || len(nal) > h264ParameterBytes {
				return nil, errH264Bounds
			}
			id, config, err := parseH264PPS(bytes.TrimRight(nal, "\x00"))
			if err != nil || v.sps[config.sps].raw == nil {
				return nil, errH264Bounds
			}
			config.raw = bytes.Clone(nal)
			if !bytes.Equal(v.pps[id].raw, nal) {
				v.known = false
			}
			v.pps = map[uint32]h264PPS{id: config}
			ppsSeen = true
		case 14:
			if unit != nil || prefix != nil || !validH264TemporalPrefix(nal) {
				return nil, errH264Bounds
			}
			prefix = nal
		case 1, 5:
			// LiveKit leaves two slice bytes clear and appends GCM tag, IV and trailer.
			// The slice payload is a single escaped ciphertext tail, not slice syntax.
			if unit != nil || len(nal) < 32 || nal[len(nal)-2] != 12 || nal[len(nal)-1] > 15 || len(v.pps) != 1 {
				return nil, errH264Bounds
			}
			key := nal[0]&31 == 5
			if prefix != nil && (prefix[1]&0x40 != 0) != key {
				return nil, errH264Bounds
			}
			if key && (!spsSeen || !ppsSeen) || !key && !v.known {
				return nil, errH264Bounds
			}
			for _, pps := range v.pps {
				sps, ok := v.sps[pps.sps]
				if !ok {
					return nil, errH264Bounds
				}
				unit = &h264AccessUnit{packets: v.packets, keyframe: key, width: sps.width, height: sps.height, configuration: [][]byte{bytes.Clone(sps.raw), bytes.Clone(pps.raw)}}
			}
		case 6, 9, 12:
			if unit != nil {
				return nil, errH264Bounds
			}
		default:
			return nil, errH264Bounds
		}
	}
	if unit != nil {
		v.known = true
	}
	return unit, nil
}
