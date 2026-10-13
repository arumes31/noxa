package webrtc

import (
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/nack"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	pion "github.com/pion/webrtc/v4"
)

// Use the production Pion chain: a reorder-only test cannot detect the NACK
// generator forgetting loss after its independent receive history fills.
func TestEngineNACKRetainsLossBeyond512Packets(t *testing.T) {
	for _, defaultHistory := range []bool{true, false} {
		t.Run(fmt.Sprintf("default512=%v", defaultHistory), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				_, registry, err := newEngineMedia(false, VideoBounds{}, nil)
				if err != nil {
					t.Fatal(err)
				}
				if defaultHistory {
					factory, err := nack.NewGeneratorInterceptor(nack.GeneratorMaxNacksPerPacket(26))
					if err != nil {
						t.Fatal(err)
					}
					registry = &interceptor.Registry{}
					registry.Add(factory)
				}
				chain, err := registry.Build("long-video-repair")
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = chain.Close() }()
				requests := make(chan uint16, 128)
				chain.BindRTCPWriter(interceptor.RTCPWriterFunc(func(packets []rtcp.Packet, _ interceptor.Attributes) (int, error) {
					for _, packet := range packets {
						if nack, ok := packet.(*rtcp.TransportLayerNack); ok {
							for _, pair := range nack.Nacks {
								pair.Range(func(sequence uint16) bool { requests <- sequence; return true })
							}
						}
					}
					return len(packets), nil
				}))
				var incoming []byte
				reader := chain.BindRemoteStream(&interceptor.StreamInfo{SSRC: 42, ClockRate: 90000, MimeType: "video/H264",
					RTCPFeedback: []interceptor.RTCPFeedback{{Type: "nack"}}},
					interceptor.RTPReaderFunc(func(buffer []byte, attrs interceptor.Attributes) (int, interceptor.Attributes, error) {
						return copy(buffer, incoming), attrs, nil
					}))
				receive := func(sequence uint16) {
					t.Helper()
					packet := &rtp.Packet{Header: rtp.Header{Version: 2, SSRC: 42, SequenceNumber: sequence}, Payload: []byte{0x41, 1}}
					incoming, err = packet.Marshal()
					if err != nil {
						t.Fatal(err)
					}
					if _, _, err := reader.Read(make([]byte, 1500), nil); err != nil {
						t.Fatal(err)
					}
				}
				receive(100)
				for sequence := uint16(102); sequence <= 850; sequence++ {
					receive(sequence)
				}
				synctest.Wait()
				if defaultHistory {
					time.Sleep(100 * time.Millisecond)
					synctest.Wait()
					select {
					case sequence := <-requests:
						t.Fatalf("default512 control unexpectedly retained old loss: %d", sequence)
					default:
					}
					return
				}
				for attempt := 1; attempt <= 26; attempt++ {
					time.Sleep(100 * time.Millisecond)
					synctest.Wait()
					select {
					case sequence := <-requests:
						if sequence != 101 {
							t.Fatalf("attempt %d requested received sequence %d", attempt, sequence)
						}
					default:
						t.Fatalf("missing sequence forgotten before attempt %d after 749 later packets", attempt)
					}
				}
				time.Sleep(time.Second)
				synctest.Wait()
				select {
				case sequence := <-requests:
					t.Fatalf("finite retry budget exceeded for %d", sequence)
				default:
				}
				receive(852)
				time.Sleep(100 * time.Millisecond)
				synctest.Wait()
				select {
				case sequence := <-requests:
					if sequence != 851 {
						t.Fatalf("fresh loss obscured by exhausted history: %d", sequence)
					}
				default:
					t.Fatal("fresh loss after exhausted retry budget was not requested")
				}
			})
		})
	}
}

func TestRepairReceiptSuppressesLateRepairBeyond512Packets(t *testing.T) {
	f := newRepairNACKFilter()
	_, attrs := receiptTestBinding(f, 42)
	var token *repairReceiptToken
	for sequence := uint16(100); sequence <= 850; sequence++ {
		if sequence != 101 {
			observeRepairReceipt(&token, &rtp.Packet{Header: rtp.Header{SSRC: 42, SequenceNumber: sequence}}, attrs)
		}
	}
	// A normalized RTX packet is acknowledged only after the current reader
	// actually returned it; the older missing packet must remain in history.
	observeRepairReceipt(&token, &rtp.Packet{Header: rtp.Header{SSRC: 42, SequenceNumber: 101}},
		interceptor.Attributes{pion.AttributeRtxSsrc: uint32(43)})
	if got := receiptTestFilter(f, receiptTestNACK(42, 101, 851)); len(got) != 1 || got[0] != 851 {
		t.Fatalf("late repair was requested again or fresh gap suppressed: %v", got)
	}
}

func TestRepairReceiptLargeFeedbackKeepsIndependentWorkBound(t *testing.T) {
	f := newRepairNACKFilter()
	_, attrs := receiptTestBinding(f, 42)
	var token *repairReceiptToken
	observeRepairReceipt(&token, &rtp.Packet{Header: rtp.Header{SSRC: 42, SequenceNumber: 101}}, attrs)
	feedback := &rtcp.TransportLayerNack{MediaSSRC: 42, Nacks: make([]rtcp.NackPair, 513)}
	for i := range feedback.Nacks {
		feedback.Nacks[i].PacketID = 101
	}
	called := false
	writer := f.BindRTCPWriter(interceptor.RTCPWriterFunc(func(packets []rtcp.Packet, _ interceptor.Attributes) (int, error) {
		called = true
		if len(packets) != 1 || packets[0] != feedback {
			t.Fatal("oversized feedback was expanded or changed")
		}
		return 1, nil
	}))
	if _, err := writer.Write([]rtcp.Packet{feedback}, nil); err != nil || !called {
		t.Fatalf("oversized feedback did not pass through: %v", err)
	}
}

func TestEngineNACKExpansionDoesNotEnableAudioFeedback(t *testing.T) {
	media, registry, err := newEngineMedia(false, VideoBounds{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	pc, err := pion.NewAPI(pion.WithMediaEngine(media), pion.WithInterceptorRegistry(registry)).NewPeerConnection(pion.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pc.Close() }()
	transceiver, err := pc.AddTransceiverFromKind(pion.RTPCodecTypeAudio)
	if err != nil {
		t.Fatal(err)
	}
	codecs := transceiver.Receiver().GetParameters().Codecs
	if len(codecs) == 0 {
		t.Fatal("audio registration missing")
	}
	for _, codec := range codecs {
		for _, feedback := range codec.RTCPFeedback {
			if feedback.Type == "nack" {
				t.Fatal("video repair configuration enabled audio NACK")
			}
		}
	}
}
