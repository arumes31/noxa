package cc

import (
	"testing"
	"time"

	"github.com/pion/rtcp"
	"github.com/stretchr/testify/require"
)

func TestTWCCEvictionPreservesDeltasAndSequenceNumbers(t *testing.T) {
	for _, vector := range []bool{false, true} {
		name := "run"
		if vector {
			name = "vector"
		}
		t.Run(name, func(t *testing.T) {
			adapter := NewFeedbackAdapter()
			// Exercise the actual bounded history: sequences zero through six
			// are evicted, covering an entire two-bit status vector.
			for i := uint16(0); i <= 256; i++ {
				adapter.history.add(Acknowledgment{SequenceNumber: i, Size: 1200, Departure: time.Unix(100, 0)})
			}
			var first rtcp.PacketStatusChunk = &rtcp.RunLengthChunk{RunLength: 7, PacketStatusSymbol: rtcp.TypeTCCPacketReceivedSmallDelta}
			if vector {
				first = &rtcp.StatusVectorChunk{SymbolList: []uint16{1, 1, 1, 1, 1, 1, 1}}
			}
			acks, err := adapter.OnTransportCCFeedback(time.Now(), &rtcp.TransportLayerCC{
				PacketStatusCount: 9, ReferenceTime: 1,
				PacketChunks: []rtcp.PacketStatusChunk{first, &rtcp.StatusVectorChunk{SymbolList: []uint16{
					rtcp.TypeTCCPacketNotReceived, rtcp.TypeTCCPacketReceivedLargeDelta,
				}}},
				RecvDeltas: []*rtcp.RecvDelta{
					{Delta: 1000}, {Delta: 1000}, {Delta: 1000}, {Delta: 1000},
					{Delta: 1000}, {Delta: 1000}, {Delta: 1000}, {Delta: -1000},
				},
			})
			require.NoError(t, err)
			require.Len(t, acks, 2, "eviction is not evidence of network loss")
			require.Equal(t, uint16(7), acks[0].SequenceNumber)
			require.True(t, acks[0].Arrival.IsZero(), "retain explicitly reported loss")
			require.Equal(t, uint16(8), acks[1].SequenceNumber)
			require.Equal(t, time.Time{}.Add(70*time.Millisecond), acks[1].Arrival)
		})
	}
}

func TestTWCCUnknownPacketStillRequiresReceiveDelta(t *testing.T) {
	for _, chunk := range []rtcp.PacketStatusChunk{
		&rtcp.RunLengthChunk{RunLength: 1, PacketStatusSymbol: rtcp.TypeTCCPacketReceivedSmallDelta},
		&rtcp.StatusVectorChunk{SymbolList: []uint16{rtcp.TypeTCCPacketReceivedSmallDelta}},
	} {
		adapter := NewFeedbackAdapter()
		acks, err := adapter.OnTransportCCFeedback(time.Now(), &rtcp.TransportLayerCC{
			PacketStatusCount: 1, PacketChunks: []rtcp.PacketStatusChunk{chunk},
		})
		require.ErrorIs(t, err, errInvalidFeedback)
		require.Empty(t, acks)
	}
}

func TestTWCCIgnoresStatusesBeyondReportedCount(t *testing.T) {
	for _, chunk := range []rtcp.PacketStatusChunk{
		&rtcp.RunLengthChunk{RunLength: 5, PacketStatusSymbol: rtcp.TypeTCCPacketNotReceived},
		&rtcp.StatusVectorChunk{SymbolList: []uint16{0, 0, 0, 0, 0, 0, 0}},
	} {
		adapter := NewFeedbackAdapter()
		for i := range uint16(7) {
			adapter.history.add(Acknowledgment{SequenceNumber: i, Size: 1200, Departure: time.Unix(100, 0)})
		}
		acks, err := adapter.OnTransportCCFeedback(time.Now(), &rtcp.TransportLayerCC{
			PacketStatusCount: 2, PacketChunks: []rtcp.PacketStatusChunk{chunk},
		})
		require.NoError(t, err)
		require.Len(t, acks, 2, "padding/in-flight packets must not count as lost")
	}
}
