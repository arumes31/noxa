package webrtc

import (
	"bytes"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/twcc"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/sdp/v3"
)

type upstreamTWCCTestTicker struct{ ticks chan time.Time }

func (t *upstreamTWCCTestTicker) Ch() <-chan time.Time { return t.ticks }
func (*upstreamTWCCTestTicker) Stop()                  {}

// This characterizes the pinned dependency; it is not evidence that a native
// stall had this cause. No sockets, payload capture, or production changes.
func TestPinnedUpstreamTWCCWriterBlocksMediaRead(t *testing.T) {
	for _, failWrite := range []bool{false, true} {
		name := "successful_feedback"
		if failWrite {
			name = "failed_feedback"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ticker := &upstreamTWCCTestTicker{ticks: make(chan time.Time, 1)}
				factory, err := twcc.NewSenderInterceptor(twcc.SendTicker(func(time.Duration) twcc.Ticker { return ticker }))
				if err != nil {
					t.Fatal(err)
				}
				instance, err := factory.NewInterceptor("synthetic-upstream")
				if err != nil {
					t.Fatal(err)
				}
				entered, release := make(chan struct{}), make(chan struct{})
				var releaseOnce sync.Once
				defer func() { releaseOnce.Do(func() { close(release) }); _ = instance.Close() }()
				reports := make(chan *rtcp.TransportLayerCC, 4)
				writes := 0
				instance.BindRTCPWriter(interceptor.RTCPWriterFunc(func(packets []rtcp.Packet, _ interceptor.Attributes) (int, error) {
					for _, packet := range packets {
						if feedback, ok := packet.(*rtcp.TransportLayerCC); ok {
							reports <- feedback
						}
					}
					writes++
					if writes == 1 {
						close(entered)
						<-release
					}
					if failWrite {
						return 0, errors.New("synthetic feedback write failure")
					}
					return len(packets), nil
				}))
				incoming := make(chan []byte, 3)
				underlyingRead := make(chan struct{}, 3)
				info := &interceptor.StreamInfo{SSRC: 42, MimeType: "video/H264", RTPHeaderExtensions: []interceptor.RTPHeaderExtension{{ID: 3, URI: sdp.TransportCCURI}}}
				reader := instance.BindRemoteStream(info, interceptor.RTPReaderFunc(func(out []byte, attrs interceptor.Attributes) (int, interceptor.Attributes, error) {
					n := copy(out, <-incoming)
					underlyingRead <- struct{}{}
					return n, attrs, nil
				}))
				packet := func(sequence uint16) []byte {
					t.Helper()
					pkt := &rtp.Packet{Header: rtp.Header{Version: 2, SSRC: 42, SequenceNumber: sequence, Timestamp: 9000}, Payload: []byte{0x61, 1}}
					ext, marshalErr := (&rtp.TransportCCExtension{TransportSequence: sequence}).Marshal()
					if marshalErr != nil {
						t.Fatal(marshalErr)
					}
					if extensionErr := pkt.SetExtension(3, ext); extensionErr != nil {
						t.Fatal(extensionErr)
					}
					raw, marshalErr := pkt.Marshal()
					if marshalErr != nil {
						t.Fatal(marshalErr)
					}
					return raw
				}
				read := func() error { _, _, readErr := reader.Read(make([]byte, 1500), nil); return readErr }
				incoming <- packet(1)
				if err = read(); err != nil {
					t.Fatal(err)
				}
				<-underlyingRead
				time.Sleep(10 * time.Millisecond)
				ticker.ticks <- time.Now()
				<-entered
				first := <-reports
				if first.BaseSequenceNumber != 1 || first.PacketStatusCount != 1 {
					t.Fatalf("unexpected first feedback: %+v", first)
				}
				second, third := packet(2), packet(3)
				incoming <- second
				incoming <- third // Both are already available before the feedback stall.
				done := make(chan error, 1)
				go func() { done <- read() }()
				<-underlyingRead
				synctest.Wait()
				select {
				case err = <-done:
					t.Fatalf("media read returned during blocked feedback write: %v", err)
				default:
				}

				// A stream without negotiated TWCC does not acquire this dependency.
				control := instance.BindRemoteStream(&interceptor.StreamInfo{SSRC: 43}, interceptor.RTPReaderFunc(func(out []byte, attrs interceptor.Attributes) (int, interceptor.Attributes, error) {
					return copy(out, third), attrs, nil
				}))
				controlBuffer := make([]byte, 1500)
				controlN, _, controlErr := control.Read(controlBuffer, nil)
				if controlErr != nil || !bytes.Equal(controlBuffer[:controlN], third) {
					t.Fatalf("non-TWCC control changed: %v", controlErr)
				}

				time.Sleep(100 * time.Millisecond)
				select {
				case err = <-done:
					t.Fatalf("media read escaped feedback stall: %v", err)
				default:
				}
				releaseOnce.Do(func() { close(release) })
				if err = <-done; err != nil {
					t.Fatal(err)
				}
				if err = read(); err != nil {
					t.Fatal(err)
				}
				<-underlyingRead
				ticker.ticks <- time.Now()
				recovered := <-reports
				if recovered.BaseSequenceNumber != 2 || recovered.PacketStatusCount != 2 || len(recovered.RecvDeltas) != 2 {
					t.Fatalf("missing feedback coverage after release: %+v", recovered)
				}
				// Packet2 was timestamped before waiting; packet3's application read and
				// timestamp happen after release although its bytes were ready beforehand.
				if got := recovered.RecvDeltas[1].Delta; got != 100_000 {
					t.Fatalf("reported interarrival delta = %dus, want virtual100ms stall", got)
				}
				t.Log("Pinned TWCC blocks a completed underlying RTP read for100ms and reports100ms between two already-available packets; writer release restores progress")
			})
		})
	}
}
