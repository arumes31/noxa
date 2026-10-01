package cc

import (
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
)

func TestTransportFeedbackAccountsRTPPadding(t *testing.T) {
	adapter := NewFeedbackAdapter()
	header := rtp.Header{Version: 2, Padding: true, PaddingSize: 200}
	if err := header.SetExtension(1, []byte{0, 7}); err != nil {
		t.Fatal(err)
	}
	if err := adapter.OnSent(time.Now(), &header, 0, interceptor.Attributes{TwccExtensionAttributesKey: uint8(1)}); err != nil {
		t.Fatal(err)
	}
	acks, err := adapter.OnTransportCCFeedback(time.Now(), &rtcp.TransportLayerCC{
		BaseSequenceNumber: 7, PacketStatusCount: 1,
		PacketChunks: []rtcp.PacketStatusChunk{&rtcp.RunLengthChunk{PacketStatusSymbol: rtcp.TypeTCCPacketReceivedSmallDelta, RunLength: 1}},
		RecvDeltas:   []*rtcp.RecvDelta{{Type: rtcp.TypeTCCPacketReceivedSmallDelta, Delta: 250}},
	})
	if err != nil || len(acks) != 1 {
		t.Fatalf("feedback: %v, %v", acks, err)
	}
	if want := (&rtp.Packet{Header: header}).MarshalSize(); acks[0].Size != want {
		t.Fatalf("accounted %d bytes, wire uses %d", acks[0].Size, want)
	}
}
