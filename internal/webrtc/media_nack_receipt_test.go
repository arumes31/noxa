package webrtc

import (
	"errors"
	"reflect"
	"testing"

	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	pion "github.com/pion/webrtc/v4"
)

func receiptTestBinding(f *repairNACKFilter, ssrc uint32) (*interceptor.StreamInfo, interceptor.Attributes) {
	info := &interceptor.StreamInfo{SSRC: ssrc, MimeType: "video/H264"}
	reader := f.BindRemoteStream(info, interceptor.RTPReaderFunc(func(b []byte, a interceptor.Attributes) (int, interceptor.Attributes, error) { return len(b), a, nil }))
	_, attrs, _ := reader.Read([]byte{1}, nil)
	return info, attrs
}
func receiptTestNACK(ssrc uint32, sequences ...uint16) *rtcp.TransportLayerNack {
	return &rtcp.TransportLayerNack{SenderSSRC: 999, MediaSSRC: ssrc, Nacks: rtcp.NackPairsFromSequenceNumbers(sequences)}
}
func receiptTestFilter(f *repairNACKFilter, nack *rtcp.TransportLayerNack) []uint16 {
	var result []uint16
	w := f.BindRTCPWriter(interceptor.RTCPWriterFunc(func(ps []rtcp.Packet, _ interceptor.Attributes) (int, error) {
		for _, p := range ps {
			if n, ok := p.(*rtcp.TransportLayerNack); ok {
				for _, pair := range n.Nacks {
					pair.Range(func(s uint16) bool { result = append(result, s); return true })
				}
			}
		}
		return 7, nil
	}))
	_, _ = w.Write([]rtcp.Packet{nack}, nil)
	return result
}
func TestRepairReceiptWindowAndLifecycle(t *testing.T) {
	f := newRepairNACKFilter()
	info, attrs := receiptTestBinding(f, 42)
	var token *repairReceiptToken
	observe := func(seq uint16, a interceptor.Attributes) {
		observeRepairReceipt(&token, &rtp.Packet{Header: rtp.Header{SSRC: 42, SequenceNumber: seq}}, a)
	}
	observe(65534, attrs)
	observe(0, attrs)
	repair := interceptor.Attributes{pion.AttributeRtxSsrc: uint32(43)}
	observe(65535, repair)
	if got := receiptTestFilter(f, receiptTestNACK(42, 65534, 65535, 0, 1)); !reflect.DeepEqual(got, []uint16{1}) {
		t.Fatalf("wrap lost true gap:%v", got)
	}
	observe(600, attrs) // eviction, no old receipt may suppress a new request
	if got := receiptTestFilter(f, receiptTestNACK(42, 0, 600, 601)); !reflect.DeepEqual(got, []uint16{0, 601}) {
		t.Fatalf("window:%v", got)
	}
	observe(0, repair) // ambiguous old RTX invalidates, cannot establish a new window
	if got := receiptTestFilter(f, receiptTestNACK(42, 0, 600)); len(got) != 2 {
		t.Fatalf("ambiguous:%v", got)
	}
	observe(601, repair)
	if got := receiptTestFilter(f, receiptTestNACK(42, 601)); len(got) != 1 {
		t.Fatal("RTX established missing primary epoch")
	}
	observe(602, attrs)
	oldToken := token
	newInfo, newAttrs := receiptTestBinding(f, 42)
	observeRepairReceipt(&oldToken, &rtp.Packet{Header: rtp.Header{SSRC: 42, SequenceNumber: 603}}, attrs)
	if got := receiptTestFilter(f, receiptTestNACK(42, 602, 603)); len(got) != 2 {
		t.Fatal("old binding suppressed new source")
	}
	f.UnbindRemoteStream(info) // ambiguous stale unbind conservatively disables new binding too
	observe(604, newAttrs)
	if got := receiptTestFilter(f, receiptTestNACK(42, 604)); len(got) != 1 {
		t.Fatal("ambiguous unbind retained suppression")
	}
	if len(f.streams) != 0 {
		t.Fatal("ambiguous unbind retained ledger")
	}
	newInfo, newAttrs = receiptTestBinding(f, 42)
	observe(604, newAttrs)
	if got := receiptTestFilter(f, receiptTestNACK(42, 604)); len(got) != 0 {
		t.Fatal("fresh bind failed to restore filtering")
	}
	copiedInfo := *newInfo
	f.UnbindRemoteStream(&copiedInfo)
	if got := receiptTestFilter(f, receiptTestNACK(42, 604)); len(got) != 1 || len(f.streams) != 0 {
		t.Fatal("copied StreamInfo unbind retained acknowledgement")
	}
	_, newAttrs = receiptTestBinding(f, 42)
	observe(605, newAttrs)
	_ = f.Close()
	observe(606, newAttrs)
	if got := receiptTestFilter(f, receiptTestNACK(42, 605, 606)); len(got) != 2 {
		t.Fatal("close retained acknowledgement")
	}
}

func TestRepairReceiptMixedFeedbackImmutableAndErrors(t *testing.T) {
	f := newRepairNACKFilter()
	_, attrs := receiptTestBinding(f, 42)
	var token *repairReceiptToken
	observeRepairReceipt(&token, &rtp.Packet{Header: rtp.Header{SSRC: 42, SequenceNumber: 101}}, attrs)
	nack := receiptTestNACK(42, 100, 101, 102, 116)
	original, _ := nack.Marshal()
	pli := &rtcp.PictureLossIndication{MediaSSRC: 42}
	other := receiptTestNACK(99, 101)
	packets := []rtcp.Packet{pli, nack, other}
	writeErr := errors.New("synthetic writer failure")
	attributes := interceptor.Attributes{"unchanged": 1}
	calls := 0
	w := f.BindRTCPWriter(interceptor.RTCPWriterFunc(func(ps []rtcp.Packet, a interceptor.Attributes) (int, error) {
		calls++
		if len(ps) != 3 || ps[0] != pli || ps[2] != other || a["unchanged"] != 1 {
			t.Fatal("compound feedback or attrs changed")
		}
		var seqs []uint16
		for _, pair := range ps[1].(*rtcp.TransportLayerNack).Nacks {
			pair.Range(func(s uint16) bool { seqs = append(seqs, s); return true })
		}
		if !reflect.DeepEqual(seqs, []uint16{100, 102, 116}) {
			t.Fatalf("true gaps changed:%v", seqs)
		}
		return 19, writeErr
	}))
	if n, err := w.Write(packets, attributes); n != 19 || !errors.Is(err, writeErr) {
		t.Fatal("writer result changed")
	}
	after, _ := nack.Marshal()
	if !reflect.DeepEqual(original, after) || packets[1] != nack || calls != 1 {
		t.Fatal("caller packet mutated")
	}
	if n, err := w.Write([]rtcp.Packet{receiptTestNACK(42, 101)}, attributes); n != 0 || err != nil || calls != 1 {
		t.Fatal("fully repaired NACK sent")
	}
}

func TestRepairReceiptBoundsAndUnknownRepair(t *testing.T) {
	f := newRepairNACKFilter()
	for i := uint32(1); i <= repairReceiptStreams; i++ {
		receiptTestBinding(f, i)
	}
	_, attrs := receiptTestBinding(f, 999)
	if _, ok := attrs[repairReceiptAttribute]; ok || len(f.streams) != repairReceiptStreams {
		t.Fatal("binding bound exceeded")
	}
	var token *repairReceiptToken
	observeRepairReceipt(&token, &rtp.Packet{Header: rtp.Header{SSRC: 1, SequenceNumber: 5}}, interceptor.Attributes{pion.AttributeRtxSsrc: uint32(2)})
	if got := receiptTestFilter(f, receiptTestNACK(1, 5)); len(got) != 1 {
		t.Fatal("unknown RTX invented receipt")
	}
	_, attrs = receiptTestBinding(f, 1)
	observeRepairReceipt(&token, &rtp.Packet{Header: rtp.Header{SSRC: 2, SequenceNumber: 5}}, attrs)
	if token != nil {
		t.Fatal("foreign primary accepted token")
	}
}
