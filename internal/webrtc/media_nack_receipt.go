package webrtc

import (
	"strings"
	"sync"

	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	pion "github.com/pion/webrtc/v4"
)

const repairReceiptAttribute = "noxa-repair-receipt-v1"
const repairReceiptWindow = videoReorderPackets
const repairReceiptStreams = 64
const repairFeedbackPairs = 512

// Receipt state is local to one interceptor/PC and invalidated on bind
// replacement, matching-SSRC unbind, or close. No synthetic packet enters an interceptor.
type repairReceiptToken struct {
	owner        *repairNACKFilter
	ssrc         uint32
	active, seen bool
	high         uint16
	received     [repairReceiptWindow]bool
}
type repairNACKFilter struct {
	interceptor.NoOp
	mu      sync.Mutex
	closed  bool
	streams map[uint32]*repairReceiptToken
}

func newRepairNACKFilter() *repairNACKFilter {
	return &repairNACKFilter{streams: make(map[uint32]*repairReceiptToken)}
}

func (f *repairNACKFilter) BindRemoteStream(info *interceptor.StreamInfo, reader interceptor.RTPReader) interceptor.RTPReader {
	if !strings.HasPrefix(strings.ToLower(info.MimeType), "video/") || strings.EqualFold(info.MimeType, "video/rtx") {
		return reader
	}
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return reader
	}
	old := f.streams[info.SSRC]
	if old == nil && len(f.streams) >= repairReceiptStreams {
		f.mu.Unlock()
		return reader
	}
	if old != nil {
		old.active = false
	}
	token := &repairReceiptToken{owner: f, ssrc: info.SSRC, active: true}
	f.streams[info.SSRC] = token
	f.mu.Unlock()
	return interceptor.RTPReaderFunc(func(b []byte, a interceptor.Attributes) (int, interceptor.Attributes, error) {
		n, attrs, err := reader.Read(b, a)
		if err != nil {
			return n, attrs, err
		}
		if attrs == nil {
			attrs = make(interceptor.Attributes)
		}
		attrs[repairReceiptAttribute] = token
		return n, attrs, nil
	})
}

// Called only after TrackRemote.ReadRTP returned actual normalized bytes. The
// first primary read supplies the exact binding token; RTX cannot invent one.
func observeRepairReceipt(current **repairReceiptToken, packet *rtp.Packet, attrs interceptor.Attributes) {
	if packet == nil {
		return
	}
	primary := false
	if token, ok := attrs[repairReceiptAttribute].(*repairReceiptToken); ok {
		if token.ssrc != packet.SSRC {
			return
		}
		*current = token
		primary = true
	} else {
		rtx, ok := attrs[pion.AttributeRtxSsrc].(uint32)
		if !ok || rtx == 0 {
			return
		}
	}
	token := *current
	if token == nil || token.ssrc != packet.SSRC {
		return
	}
	f := token.owner
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed || !token.active || f.streams[packet.SSRC] != token {
		return
	}
	seq := packet.SequenceNumber
	if !token.seen {
		if !primary {
			return
		}
		token.seen = true
		token.high = seq
	} else {
		delta := int16(seq - token.high)
		if delta <= -repairReceiptWindow {
			// Ambiguous old arrival or large sequence jump: forget evidence and
			// pass future requests until a fresh primary establishes a window.
			clear(token.received[:])
			token.seen = false
			return
		}
		if delta > 0 {
			if delta >= repairReceiptWindow {
				clear(token.received[:])
			} else {
				for n := 1; n <= int(delta); n++ {
					token.received[(int(token.high)+n)%repairReceiptWindow] = false
				}
			}
			token.high = seq
		}
	}
	token.received[int(seq)%repairReceiptWindow] = true
}

func (f *repairNACKFilter) UnbindRemoteStream(info *interceptor.StreamInfo) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Pion binds a value-copy of StreamInfo and unbinds the original pointer;
	// remote IDs are empty. It provides no unique generation identity here.
	// A stale same-SSRC unbind therefore conservatively disables filtering for
	// the current binding too. Extra NACKs are safe; stale receipt suppression
	// across ambiguous lifetimes is not. Only a later bind restores filtering.
	if token := f.streams[info.SSRC]; token != nil {
		token.active = false
		delete(f.streams, info.SSRC)
	}
}
func (f *repairNACKFilter) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	for _, token := range f.streams {
		token.active = false
	}
	clear(f.streams)
	return nil
}

func (f *repairNACKFilter) BindRTCPWriter(writer interceptor.RTCPWriter) interceptor.RTCPWriter {
	return interceptor.RTCPWriterFunc(func(packets []rtcp.Packet, attrs interceptor.Attributes) (int, error) {
		var output []rtcp.Packet
		changed := false
		f.mu.Lock()
		for index, packet := range packets {
			nack, ok := packet.(*rtcp.TransportLayerNack)
			var token *repairReceiptToken
			if ok && !f.closed {
				token = f.streams[nack.MediaSSRC]
			}
			if token == nil || !token.active || !token.seen {
				if changed {
					output = append(output, packet)
				}
				continue
			}
			// Do not expand untrusted feedback into an unbounded OSN list.
			if len(nack.Nacks) > repairFeedbackPairs {
				if changed {
					output = append(output, packet)
				}
				continue
			}
			kept := make([]rtcp.NackPair, 0, len(nack.Nacks))
			removed := false
			for _, pair := range nack.Nacks {
				var pending rtcp.NackPair
				have := false
				pair.Range(func(seq uint16) bool {
					distance := uint16(token.high - seq)
					if distance < repairReceiptWindow && token.received[int(seq)%repairReceiptWindow] {
						removed = true
						return true
					}
					if !have {
						pending.PacketID = seq
						have = true
					} else {
						pending.LostPackets |= 1 << (seq - pending.PacketID - 1)
					}
					return true
				})
				if have {
					kept = append(kept, pending)
				}
			}
			if !removed {
				if changed {
					output = append(output, packet)
				}
				continue
			}
			if !changed {
				output = append(make([]rtcp.Packet, 0, len(packets)), packets[:index]...)
				changed = true
			}
			if len(kept) > 0 {
				copyNACK := *nack
				copyNACK.Nacks = kept
				output = append(output, &copyNACK)
			}
		}
		f.mu.Unlock()
		if !changed {
			return writer.Write(packets, attrs)
		}
		if len(output) == 0 {
			return 0, nil
		}
		return writer.Write(output, attrs)
	})
}

type repairNACKFactory struct{}

func (repairNACKFactory) NewInterceptor(string) (interceptor.Interceptor, error) {
	return newRepairNACKFilter(), nil
}
