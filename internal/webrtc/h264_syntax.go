package webrtc

import "github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"

// Checked reads reject overflowing Exp-Golomb codes before narrowing. This
// preflight protects fields read by the third-party SPS parser as well.
type h264Bits struct {
	data   []byte
	pos    int
	failed bool
}

func (r *h264Bits) read(n int) uint32 {
	if r.failed || n < 0 || n > 32 || n > len(r.data)*8-r.pos {
		r.failed = true
		return 0
	}
	var value uint32
	for range n {
		value = value<<1 | uint32(r.data[r.pos/8]>>(7-r.pos%8)&1)
		r.pos++
	}
	return value
}

func (r *h264Bits) ue(maximum uint32) uint32 {
	zeros := 0
	for !r.failed && r.read(1) == 0 {
		zeros++
		if zeros >= 32 {
			r.failed = true
			return 0
		}
	}
	value := uint32(1<<zeros) - 1 + r.read(zeros)
	if value > maximum {
		r.failed = true
	}
	return value
}

func (r *h264Bits) se(minimum, maximum int32) int32 {
	u := r.ue(^uint32(0) - 1)
	value := -int32(u / 2)
	if u&1 != 0 {
		value = int32(u/2) + 1
	}
	if value < minimum || value > maximum {
		r.failed = true
	}
	return value
}

func (r *h264Bits) more() bool {
	n := len(r.data)*8 - r.pos
	if r.failed || n <= 0 {
		return false
	}
	if n > 8 {
		return true
	}
	copy := *r
	return copy.read(n) != 1<<(n-1)
}

func (r *h264Bits) end() bool {
	n := len(r.data)*8 - r.pos
	return !r.failed && n > 0 && n <= 8 && r.read(n) == 1<<(n-1)
}

// Check every variable-width SPS field through frame cropping. Optional VUI
// does not change coded dimensions and is parsed by mediacommon afterwards.
func preflightH264SPS(nal []byte) bool {
	r := h264Bits{data: h264.EmulationPreventionRemove(nal[1:])}
	r.read(24)
	r.ue(31)
	r.ue(12)
	switch r.ue(2) {
	case 0:
		r.ue(12)
	case 1:
		r.read(1)
		r.se(-2147483647, 2147483647)
		r.se(-2147483647, 2147483647)
		count := r.ue(255)
		for i := uint32(0); !r.failed && i < count; i++ {
			r.se(-2147483647, 2147483647)
		}
	}
	r.ue(16)
	r.read(1)
	r.ue(1023)
	r.ue(1023)
	if r.read(1) != 1 {
		return false
	} // progressive pictures only
	r.read(1)
	if r.read(1) != 0 {
		for range 4 {
			r.ue(8192)
		}
	}
	if r.read(1) != 0 {
		readH264VUI(&r)
	}
	return r.end()
}

func readH264VUI(r *h264Bits) {
	if r.read(1) != 0 && r.read(8) == 255 {
		r.read(16)
		r.read(16)
	}
	if r.read(1) != 0 {
		r.read(1)
	}
	if r.read(1) != 0 {
		r.read(3)
		r.read(1)
		if r.read(1) != 0 {
			r.read(24)
		}
	}
	if r.read(1) != 0 {
		r.ue(5)
		r.ue(5)
	}
	if r.read(1) != 0 {
		tick, scale := r.read(32), r.read(32)
		if tick == 0 || scale == 0 {
			r.failed = true
		}
		r.read(1)
	}
	nalHRD := r.read(1) != 0
	if nalHRD {
		readH264HRD(r)
	}
	vclHRD := r.read(1) != 0
	if vclHRD {
		readH264HRD(r)
	}
	if nalHRD || vclHRD {
		r.read(1)
	}
	r.read(1)
	if r.read(1) != 0 {
		r.read(1)
		for range 4 {
			r.ue(16)
		}
		r.ue(16)
		r.ue(16)
	}
}

func readH264HRD(r *h264Bits) {
	count := r.ue(31) + 1
	r.read(8)
	for i := uint32(0); !r.failed && i < count; i++ {
		r.ue(^uint32(0) - 1)
		r.ue(^uint32(0) - 1)
		r.read(1)
	}
	r.read(20)
}

func parseH264PPS(nal []byte) (uint32, h264PPS, error) {
	if len(nal) < 2 || len(nal) > h264ParameterBytes || nal[0]&0x60 == 0 {
		return 0, h264PPS{}, errH264Bounds
	}
	r := h264Bits{data: h264.EmulationPreventionRemove(nal[1:])}
	id := r.ue(255)
	pps := h264PPS{raw: append([]byte(nil), nal...), sps: r.ue(31)}
	if r.read(1) != 0 {
		return 0, h264PPS{}, errH264Bounds
	} // no CABAC in baseline
	pps.bottomPOC = r.read(1) != 0
	if r.ue(0) != 0 {
		return 0, h264PPS{}, errH264Bounds
	} // no FMO in constrained baseline
	r.ue(31)
	r.ue(31)
	if r.read(1) != 0 || r.read(2) != 0 {
		return 0, h264PPS{}, errH264Bounds
	}
	pps.initialQP = 26 + r.se(-26, 25)
	r.se(-26, 25)
	r.se(-12, 12)
	pps.deblocking = r.read(1) != 0
	r.read(1)
	if r.read(1) != 0 {
		return 0, h264PPS{}, errH264Bounds
	} // no redundant pictures
	if r.more() {
		if r.read(1) != 0 || r.read(1) != 0 {
			return 0, h264PPS{}, errH264Bounds
		}
		r.se(-12, 12)
	}
	if !r.end() {
		return 0, h264PPS{}, errH264Bounds
	}
	return id, pps, nil
}

type h264Slice struct {
	pps, firstMB uint32
	key          bool
	picture      h264Picture
}

type h264Picture struct {
	frameNum, idrID, pocLSB     uint32
	deltaBottom, delta0, delta1 int32
	reference                   bool
}

// Check the complete supported slice header, without entropy-decoding any
// macroblocks. The decoder remains responsible for validating slice data.
func parseH264Slice(nal []byte, ppsSets map[uint32]h264PPS, spsSets map[uint32]h264SPS) (h264Slice, error) {
	if len(nal) < 2 {
		return h264Slice{}, errH264Bounds
	}
	r := h264Bits{data: h264.EmulationPreventionRemove(nal[1:min(len(nal), h264ParameterBytes+1)])}
	out := h264Slice{firstMB: r.ue(1024*1024 - 1), key: nal[0]&31 == 5}
	typ := r.ue(9) % 5
	if typ != 0 && typ != 2 {
		return out, errH264Bounds
	}
	out.pps = r.ue(255)
	pps, ok := ppsSets[out.pps]
	if !ok {
		return out, errH264Bounds
	}
	sps, ok := spsSets[pps.sps]
	if !ok || out.firstMB >= uint32(sps.macroblocks) || out.key && (typ != 2 || nal[0]&0x60 == 0) {
		return out, errH264Bounds
	}
	out.picture.frameNum = r.read(int(sps.syntax.Log2MaxFrameNumMinus4) + 4)
	out.picture.reference = nal[0]&0x60 != 0
	if out.key {
		if out.picture.frameNum != 0 {
			return out, errH264Bounds
		}
		out.picture.idrID = r.ue(65535)
	}
	if sps.syntax.PicOrderCntType == 0 {
		out.picture.pocLSB = r.read(int(sps.syntax.Log2MaxPicOrderCntLsbMinus4) + 4)
		if pps.bottomPOC {
			out.picture.deltaBottom = r.se(-2147483647, 2147483647)
		}
	} else if sps.syntax.PicOrderCntType == 1 && !sps.syntax.DeltaPicOrderAlwaysZeroFlag {
		out.picture.delta0 = r.se(-2147483647, 2147483647)
		if pps.bottomPOC {
			out.picture.delta1 = r.se(-2147483647, 2147483647)
		}
	}
	if typ == 0 {
		if r.read(1) != 0 {
			r.ue(31)
		}
		if r.read(1) != 0 {
			for count := 0; ; count++ {
				if r.failed || count >= 64 {
					return out, errH264Bounds
				}
				operation := r.ue(3)
				if operation == 3 {
					break
				}
				r.ue(65535)
			}
		}
	}
	if nal[0]&0x60 != 0 {
		if out.key {
			r.read(2)
		} else if r.read(1) != 0 {
			for count := 0; ; count++ {
				if r.failed || count >= 64 {
					return out, errH264Bounds
				}
				operation := r.ue(6)
				if operation == 0 {
					break
				}
				if operation == 1 || operation == 3 {
					r.ue(65535)
				}
				if operation == 2 {
					r.ue(65535)
				}
				if operation == 3 || operation == 6 {
					r.ue(65535)
				}
				if operation == 4 {
					r.ue(65536)
				}
			}
		}
	}
	r.se(-pps.initialQP, 51-pps.initialQP)
	if pps.deblocking && r.ue(2) != 1 {
		r.se(-6, 6)
		r.se(-6, 6)
	}
	if r.failed || !r.more() {
		return out, errH264Bounds
	}
	return out, nil
}
