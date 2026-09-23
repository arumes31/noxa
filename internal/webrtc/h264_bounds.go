package webrtc

import (
	"bytes"
	"encoding/binary"
	"errors"
	"time"

	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	"github.com/pion/rtp"
)

const (
	h264AccessUnitBytes   = 1024 * 1024
	h264AccessUnitPackets = 1024
	h264AccessUnitNALs    = 128
	h264ParameterBytes    = 4096
	h264AccessUnitWait    = 500 * time.Millisecond
)

var errH264Bounds = errors.New("invalid, incomplete or out-of-bounds H.264 access unit")

type h264SPS struct {
	raw                        []byte
	width, height, macroblocks int
	syntax                     h264.SPS
}

type h264PPS struct {
	raw                   []byte
	sps                   uint32
	bottomPOC, deblocking bool
	initialQP             int32
}

// h264AccessUnit retains encoded RTP; validation never decodes or transcodes.
// Configuration belongs to the referenced PPS/SPS, including IDRs that omit it.
type h264AccessUnit struct {
	packets       []*rtp.Packet
	configuration [][]byte
	keyframe      bool
	width, height int
}

// One inspector belongs to one publication layer. Reordering must run first.
// Nothing is emitted before the entire unit is validated, so a malformed last
// STAP entry or fragment cannot expose an earlier unvalidated parameter change.
// This plaintext inspector must not be used to claim encrypted-frame validation.
type h264BoundsInspector struct {
	bounds       VideoBounds
	seen, known  bool
	referencePPS uint32
	sequence     uint16
	ssrc         uint32
	timestamp    uint32
	started      time.Time
	packets      []*rtp.Packet
	nals         [][]byte
	fragment     []byte
	bytes        int
	sps          map[uint32]h264SPS
	pps          map[uint32]h264PPS
}

func (v *h264BoundsInspector) clearUnit() {
	v.packets, v.nals, v.fragment = nil, nil, nil
	v.bytes = 0
	v.started = time.Time{}
}

func (v *h264BoundsInspector) reject() (*h264AccessUnit, error) {
	v.clearUnit()
	v.sps, v.pps, v.known = nil, nil, false
	return nil, errH264Bounds
}

func (v *h264BoundsInspector) push(packet *rtp.Packet, now time.Time) (*h264AccessUnit, error) {
	if packet == nil {
		return v.reject()
	}
	if v.seen && int16(packet.SequenceNumber-v.sequence) <= 0 {
		return nil, nil
	}
	gap := v.seen && (packet.SequenceNumber != v.sequence+1 || packet.SSRC != v.ssrc)
	v.sequence, v.ssrc, v.seen = packet.SequenceNumber, packet.SSRC, true
	if gap {
		// Missing packets can contain new parameter sets with reused IDs.
		v.clearUnit()
		v.sps, v.pps, v.known = nil, nil, false
	}
	if len(v.packets) > 0 && ((!isVideoPadding(packet) && packet.Timestamp != v.timestamp) || now.Sub(v.started) > h264AccessUnitWait) {
		v.clearUnit()
		v.sps, v.pps, v.known = nil, nil, false
	}
	if isVideoPadding(packet) {
		return nil, nil
	}
	if len(packet.Payload) == 0 || packet.Payload[0]&0x80 != 0 || packet.MarshalSize() > h264AccessUnitBytes-v.bytes || len(v.packets) >= h264AccessUnitPackets {
		return v.reject()
	}
	if len(v.packets) == 0 {
		v.timestamp, v.started = packet.Timestamp, now
	}
	v.bytes += packet.MarshalSize()
	payload := packet.Payload
	typ := payload[0] & 31
	switch typ {
	case 24: // STAP-A; nested/interleaved aggregates are forbidden.
		if len(v.fragment) != 0 {
			return v.reject()
		}
		data := payload[1:]
		if len(data) == 0 {
			return v.reject()
		}
		for len(data) > 0 {
			if len(data) < 2 {
				return v.reject()
			}
			size := int(binary.BigEndian.Uint16(data))
			data = data[2:]
			if size == 0 || size > len(data) || !v.addNAL(data[:size]) {
				return v.reject()
			}
			data = data[size:]
		}
	case 28: // FU-A; require one ordered, uninterrupted NAL.
		if len(payload) < 3 || payload[1]&0x20 != 0 {
			return v.reject()
		}
		start, end := payload[1]&0x80 != 0, payload[1]&0x40 != 0
		header := payload[0]&0xe0 | payload[1]&31
		if start && end || header&31 == 0 || header&31 >= 24 {
			return v.reject()
		}
		if start {
			if len(v.fragment) != 0 {
				return v.reject()
			}
			v.fragment = []byte{header}
		} else if len(v.fragment) == 0 || v.fragment[0] != header {
			return v.reject()
		}
		v.fragment = append(v.fragment, payload[2:]...)
		if end {
			if !v.addNAL(v.fragment) {
				return v.reject()
			}
			v.fragment = nil
		}
	default:
		if len(v.fragment) != 0 || !v.addNAL(payload) {
			return v.reject()
		}
	}
	v.packets = append(v.packets, packet.Clone())
	if !packet.Marker {
		return nil, nil
	}
	if len(v.fragment) != 0 {
		return v.reject()
	}
	unit, err := v.finish()
	if err != nil {
		return v.reject()
	}
	v.clearUnit()
	return unit, nil
}

func (v *h264BoundsInspector) addNAL(nal []byte) bool {
	if len(nal) < 2 || nal[0]&0x80 != 0 || len(v.nals) >= h264AccessUnitNALs {
		return false
	}
	switch nal[0] & 31 {
	case 1, 5, 6, 7, 8, 9, 12:
		v.nals = append(v.nals, bytes.Clone(nal))
		return true
	default:
		return false
	}
}

func (v *h264BoundsInspector) finish() (*h264AccessUnit, error) {
	var unit *h264AccessUnit
	var picturePPS uint32
	var picture h264Picture
	var previousMB uint32
	for _, nal := range v.nals {
		switch nal[0] & 31 {
		case 7:
			if unit != nil {
				return nil, errH264Bounds
			}
			id, configuration, err := parseH264SPS(nal, v.bounds)
			if err != nil {
				return nil, err
			}
			if !bytes.Equal(v.sps[id].raw, nal) {
				v.known = false
			}
			if v.sps == nil {
				v.sps = make(map[uint32]h264SPS)
			}
			v.sps[id] = configuration
		case 8:
			if unit != nil || len(nal) > h264ParameterBytes || nal[0]&0x60 == 0 {
				return nil, errH264Bounds
			}
			id, pps, err := parseH264PPS(nal)
			if err != nil || v.sps[pps.sps].raw == nil {
				return nil, errH264Bounds
			}
			if !bytes.Equal(v.pps[id].raw, nal) {
				v.known = false
			}
			if v.pps == nil {
				v.pps = make(map[uint32]h264PPS)
			}
			v.pps[id] = pps
		case 1, 5:
			slice, err := parseH264Slice(nal, v.pps, v.sps)
			if err != nil {
				return nil, errH264Bounds
			}
			ppsID, key := slice.pps, slice.key
			pps := v.pps[ppsID]
			sps := v.sps[pps.sps]
			if !key && (!v.known || v.referencePPS != ppsID) {
				return nil, errH264Bounds
			}
			if unit == nil {
				if slice.firstMB != 0 {
					return nil, errH264Bounds
				}
				picturePPS = ppsID
				picture = slice.picture
				unit = &h264AccessUnit{packets: v.packets, keyframe: key, width: sps.width, height: sps.height,
					configuration: [][]byte{bytes.Clone(sps.raw), bytes.Clone(pps.raw)}}
			} else if unit.keyframe != key || picturePPS != ppsID || picture != slice.picture || slice.firstMB <= previousMB {
				return nil, errH264Bounds
			}
			previousMB = slice.firstMB
		}
	}
	if unit != nil {
		v.known = true
		v.referencePPS = picturePPS
	}
	return unit, nil
}

func parseH264SPS(nal []byte, bounds VideoBounds) (uint32, h264SPS, error) {
	if len(nal) < 5 || len(nal) > h264ParameterBytes || nal[0]&0x60 == 0 || nal[1] != 66 || nal[2]&0x40 == 0 || nal[2]&3 != 0 || !preflightH264SPS(nal) {
		return 0, h264SPS{}, errH264Bounds
	}
	var sps h264.SPS
	if err := sps.Unmarshal(nal); err != nil || sps.ID > 31 || !sps.FrameMbsOnlyFlag || sps.ChromaFormatIdc != 1 {
		return 0, h264SPS{}, errH264Bounds
	}
	maxWidth, maxHeight := bounds.Width, bounds.Height
	if bounds == (VideoBounds{}) {
		maxWidth, maxHeight = 16383, 16383
	}
	// Validate the coded size before cropping and before any uint32 arithmetic
	// in convenience dimension helpers; excessive cropping cannot bypass caps.
	w, h := (uint64(sps.PicWidthInMbsMinus1)+1)*16, (uint64(sps.PicHeightInMapUnitsMinus1)+1)*16
	if w > uint64((maxWidth+15)/16*16) || h > uint64((maxHeight+15)/16*16) {
		return 0, h264SPS{}, errH264Bounds
	}
	macroblocks := int(w / 16 * h / 16)
	if crop := sps.FrameCropping; crop != nil {
		x := (uint64(crop.LeftOffset) + uint64(crop.RightOffset)) * 2
		y := (uint64(crop.TopOffset) + uint64(crop.BottomOffset)) * 2
		if x >= w || y >= h {
			return 0, h264SPS{}, errH264Bounds
		}
		w -= x
		h -= y
	}
	if w == 0 || h == 0 || w > uint64(maxWidth) || h > uint64(maxHeight) {
		return 0, h264SPS{}, errH264Bounds
	}
	return sps.ID, h264SPS{bytes.Clone(nal), int(w), int(h), macroblocks, sps}, nil
}
