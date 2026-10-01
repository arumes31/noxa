package webrtc

import (
	"strings"

	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/cc"
	"github.com/pion/rtcp"
	"github.com/pion/sdp/v3"
)

// The pinned adapter retains 250 sends and reads padded status symbols. Neither
// padding nor evicted history is evidence of loss; give GCC a bounded copy.
type mediaBandwidthEstimator struct {
	cc.BandwidthEstimator
	pacer *mediaPacer
}

func (e *mediaBandwidthEstimator) AddStream(info *interceptor.StreamInfo, writer interceptor.RTPWriter) interceptor.RTPWriter {
	// Voice has a separate, unpaced lane. Its sparse traffic cannot measure
	// the capacity of the paced video lane, especially before viewing starts.
	if strings.EqualFold(info.MimeType, "audio/opus") {
		e.pacer.AddStream(info.SSRC, writer)
		return e.pacer
	}
	for _, extension := range info.RTPHeaderExtensions {
		if extension.URI == sdp.TransportCCURI && extension.ID > 0 && extension.ID < 256 {
			return e.BandwidthEstimator.AddStream(info, writer)
		}
	}
	// Preserve guarded pacing without inserting incompatible RFC8888 entries
	// into the same bounded history used by negotiated TWCC streams.
	e.pacer.AddStream(info.SSRC, writer)
	return e.pacer
}

func (e *mediaBandwidthEstimator) WriteRTCP(packets []rtcp.Packet, attrs interceptor.Attributes) error {
	e.pacer.historyMu.Lock()
	defer e.pacer.historyMu.Unlock()
	normalized := make([]rtcp.Packet, 0, len(packets))
	for _, packet := range packets {
		if feedback, ok := packet.(*rtcp.TransportLayerCC); ok {
			if bounded := boundedTransportFeedback(feedback, e.pacer.sent); bounded != nil {
				normalized = append(normalized, bounded)
			}
		} else {
			normalized = append(normalized, packet)
		}
	}
	return e.BandwidthEstimator.WriteRTCP(normalized, attrs)
}

func boundedTransportFeedback(feedback *rtcp.TransportLayerCC, sent uint64) *rtcp.TransportLayerCC {
	if sent == 0 || feedback.PacketStatusCount == 0 || feedback.PacketStatusCount > 250 {
		return nil
	}
	// Subtraction intentionally follows the wire's 16-bit sequence space.
	distance := uint16((sent-1)&0xffff) - feedback.BaseSequenceNumber
	if uint64(distance) >= min(sent, 250) || feedback.PacketStatusCount > distance+1 {
		return nil
	}
	out := *feedback
	out.PacketChunks = nil
	remaining := int(feedback.PacketStatusCount)
	for _, chunk := range feedback.PacketChunks {
		if remaining == 0 {
			break
		}
		switch value := chunk.(type) {
		case *rtcp.RunLengthChunk:
			copy := *value
			copy.RunLength = min(uint16(remaining&0xffff), value.RunLength)
			remaining -= int(copy.RunLength)
			out.PacketChunks = append(out.PacketChunks, &copy)
		case *rtcp.StatusVectorChunk:
			copy := *value
			n := min(remaining, len(value.SymbolList))
			copy.SymbolList = append([]uint16(nil), value.SymbolList[:n]...)
			remaining -= n
			out.PacketChunks = append(out.PacketChunks, &copy)
		default:
			return nil
		}
	}
	if remaining != 0 {
		return nil
	}
	return &out
}
