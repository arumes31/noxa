package webrtc

import (
	"bytes"
	"encoding/binary"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	pion "github.com/pion/webrtc/v4"
)

type reproRTXBinding struct {
	info   interceptor.StreamInfo
	writer interceptor.RTPWriter
}
type reproRTXFactory struct{ binding chan reproRTXBinding }

func (f *reproRTXFactory) NewInterceptor(string) (interceptor.Interceptor, error) {
	return &reproRTXInterceptor{binding: f.binding}, nil
}

type reproRTXInterceptor struct {
	interceptor.NoOp
	binding chan reproRTXBinding
}

func (i *reproRTXInterceptor) BindLocalStream(info *interceptor.StreamInfo, writer interceptor.RTPWriter) interceptor.RTPWriter {
	i.binding <- reproRTXBinding{info: *info, writer: writer}
	return writer
}

// Real loopback ICE/DTLS/SRTP and negotiated RTX. Sender intentionally has no
// automatic NACK responder so only the explicitly selected repair is sent.
// Receiver uses the repository's production interceptor registry unchanged.
func TestEngineRepairReceiptStopsNACKAfterNormalizedRepair(t *testing.T) {
	for _, repair := range []string{"primary-control", "rtx-filtered"} {
		t.Run(repair, func(t *testing.T) {
			settings := pion.SettingEngine{}
			settings.SetNetworkTypes([]pion.NetworkType{pion.NetworkTypeUDP4})
			settings.SetIncludeLoopbackCandidate(true)
			settings.SetIPFilter(func(ip net.IP) bool { return ip.IsLoopback() })
			media := &pion.MediaEngine{}
			if err := media.RegisterDefaultCodecs(); err != nil {
				t.Fatal(err)
			}
			bindings := make(chan reproRTXBinding, 4)
			sendRegistry := &interceptor.Registry{}
			sendRegistry.Add(&reproRTXFactory{binding: bindings})
			sender, err := pion.NewAPI(pion.WithMediaEngine(media), pion.WithSettingEngine(settings), pion.WithInterceptorRegistry(sendRegistry)).NewPeerConnection(pion.Configuration{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = sender.Close() })
			recvMedia, recvRegistry, err := newEngineMedia(false, VideoBounds{}, nil)
			if err != nil {
				t.Fatal(err)
			}
			createdFilter := make(chan *repairNACKFilter, 1)
			{
				base := recvRegistry
				recvRegistry = &interceptor.Registry{}
				recvRegistry.Add(receiptTestRegistryFactory{base: base, captured: createdFilter})
			}
			receiver, err := pion.NewAPI(pion.WithMediaEngine(recvMedia), pion.WithSettingEngine(settings), pion.WithInterceptorRegistry(recvRegistry)).NewPeerConnection(pion.Configuration{})
			if err != nil {
				t.Fatal(err)
			}
			readStarted := make(chan struct{})
			readDone := make(chan struct{})
			t.Cleanup(func() {
				_ = receiver.Close()
				select {
				case <-readStarted:
					select {
					case <-readDone:
					case <-time.After(time.Second):
						t.Error("ordered read did not stop after receiver close")
					}
				default:
				}
			})
			track, err := pion.NewTrackLocalStaticRTP(pion.RTPCodecCapability{MimeType: pion.MimeTypeVP8, ClockRate: 90000}, "synthetic", "synthetic")
			if err != nil {
				t.Fatal(err)
			}
			rtpSender, err := sender.AddTrack(track)
			if err != nil {
				t.Fatal(err)
			}
			nacks := make(chan uint16, 100)
			go func() {
				for {
					packets, _, err := rtpSender.ReadRTCP()
					if err != nil {
						return
					}
					for _, packet := range packets {
						if nack, ok := packet.(*rtcp.TransportLayerNack); ok {
							for _, pair := range nack.Nacks {
								pair.Range(func(seq uint16) bool {
									select {
									case nacks <- seq:
									default:
									}
									return true
								})
							}
						}
					}
				}
			}()
			received := make(chan *rtp.Packet, 10)
			activeReceiver := make(chan *pion.RTPReceiver, 1)
			receiver.OnTrack(func(remote *pion.TrackRemote, inbound *pion.RTPReceiver) {
				close(readStarted)
				activeReceiver <- inbound
				go func() {
					defer close(readDone)
					readOrderedVideo(remote, func() bool { return true }, func() string { return pion.MimeTypeVP8 }, func(packet *rtp.Packet, _ string) {
						select {
						case received <- packet.Clone():
						default:
						}
					}, nil)
				}()
			})
			connected := make(chan struct{})
			var once sync.Once
			sender.OnConnectionStateChange(func(state pion.PeerConnectionState) {
				if state == pion.PeerConnectionStateConnected {
					once.Do(func() { close(connected) })
				}
			})
			offer, err := sender.CreateOffer(nil)
			if err != nil {
				t.Fatal(err)
			}
			gather := pion.GatheringCompletePromise(sender)
			if err = sender.SetLocalDescription(offer); err != nil {
				t.Fatal(err)
			}
			select {
			case <-gather:
			case <-time.After(5 * time.Second):
				t.Fatal("sender gather timeout")
			}
			if err = receiver.SetRemoteDescription(*sender.LocalDescription()); err != nil {
				t.Fatal(err)
			}
			answer, err := receiver.CreateAnswer(nil)
			if err != nil {
				t.Fatal(err)
			}
			gather = pion.GatheringCompletePromise(receiver)
			if err = receiver.SetLocalDescription(answer); err != nil {
				t.Fatal(err)
			}
			select {
			case <-gather:
			case <-time.After(5 * time.Second):
				t.Fatal("receiver gather timeout")
			}
			if err = sender.SetRemoteDescription(*receiver.LocalDescription()); err != nil {
				t.Fatal(err)
			}
			select {
			case <-connected:
			case <-time.After(5 * time.Second):
				t.Fatal("connection timeout")
			}
			var binding reproRTXBinding
			select {
			case binding = <-bindings:
			case <-time.After(time.Second):
				t.Fatal("no binding")
			}
			if binding.info.SSRCRetransmission == 0 || binding.info.PayloadTypeRetransmission == 0 {
				t.Fatal("RTX was not negotiated")
			}
			payload := []byte{0x10, 1, 2, 3}
			writePrimary := func(seq uint16) {
				t.Helper()
				if err := track.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: seq, Timestamp: 9000, Marker: true}, Payload: payload}); err != nil {
					t.Fatal(err)
				}
			}
			writePrimary(100)
			writePrimary(102)
			select {
			case seq := <-nacks:
				if seq != 101 {
					t.Fatalf("wrong initial NACK %d", seq)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("missing initial NACK101")
			}
			if repair == "primary-control" {
				writePrimary(101)
			} else {
				rtxPayload := make([]byte, len(payload)+2)
				binary.BigEndian.PutUint16(rtxPayload, 101)
				copy(rtxPayload[2:], payload)
				_, err = binding.writer.Write(&rtp.Header{Version: 2, SSRC: binding.info.SSRCRetransmission, PayloadType: binding.info.PayloadTypeRetransmission, SequenceNumber: 700, Timestamp: 9000, Marker: true}, rtxPayload, nil)
				if err != nil {
					t.Fatal(err)
				}
			}
			timeout := time.NewTimer(time.Second)
			defer timeout.Stop()
			consumed := false
			for !consumed {
				select {
				case packet := <-received:
					if packet.SequenceNumber == 101 {
						if packet.SSRC != binding.info.SSRC || packet.Timestamp != 9000 || !bytes.Equal(packet.Payload, payload) {
							t.Fatal("normalized repair mismatch")
						}
						consumed = true
					}
				case <-timeout.C:
					t.Fatal("repair did not reach application")
				}
			}
			// Ignore any NACK already in flight before confirmed consumption.
			time.Sleep(120 * time.Millisecond)
			for len(nacks) > 0 {
				<-nacks
			}
			writePrimary(104) // fresh loss103 must remain repairable
			var repeated []uint16
			observation := time.NewTimer(350 * time.Millisecond)
			defer observation.Stop()
		observe:
			for {
				select {
				case seq := <-nacks:
					repeated = append(repeated, seq)
				case <-observation.C:
					break observe
				}
			}
			for _, seq := range repeated {
				expected := uint16(103)
				if seq != expected {
					t.Fatalf("unexpected NACK%d, expected%d", seq, expected)
				}
			}
			if len(repeated) == 0 {
				t.Fatal("filter suppressed fresh true gap103")
			}
			t.Logf("repair=%s normalized101_consumed=true subsequent_NACKs=%v", repair, repeated)
			{
				var filter *repairNACKFilter
				select {
				case filter = <-createdFilter:
				case <-time.After(time.Second):
					t.Fatal("production receipt filter was not observed")
				}
				var inbound *pion.RTPReceiver
				select {
				case inbound = <-activeReceiver:
				case <-time.After(time.Second):
					t.Fatal("active receiver was not observed")
				}
				filter.mu.Lock()
				before := len(filter.streams)
				filter.mu.Unlock()
				if before != 1 {
					t.Fatalf("expected one live primary binding, got%d", before)
				}
				if err := inbound.Stop(); err != nil {
					t.Fatal(err)
				}
				filter.mu.Lock()
				after := len(filter.streams)
				filter.mu.Unlock()
				if after != 0 {
					t.Fatalf("actual Pion receiver.Stop retained%d receipt bindings", after)
				}
				t.Log("actual Pion receiver.Stop removed receipt binding")
			}
		})
	}
}

// Captures the existing production token only to assert actual Pion Stop cleanup;
// it does not create state, acknowledge packets, or change the media pipeline.
type receiptTestRegistryFactory struct {
	base     *interceptor.Registry
	captured chan *repairNACKFilter
}

func (f receiptTestRegistryFactory) NewInterceptor(id string) (interceptor.Interceptor, error) {
	base, err := f.base.Build(id)
	if err != nil {
		return nil, err
	}
	return interceptor.NewChain([]interceptor.Interceptor{base, &receiptTestCapture{captured: f.captured}}), nil
}

type receiptTestCapture struct {
	interceptor.NoOp
	captured chan *repairNACKFilter
	once     sync.Once
}

func (c *receiptTestCapture) BindRemoteStream(info *interceptor.StreamInfo, reader interceptor.RTPReader) interceptor.RTPReader {
	return interceptor.RTPReaderFunc(func(b []byte, a interceptor.Attributes) (int, interceptor.Attributes, error) {
		n, attrs, err := reader.Read(b, a)
		if err == nil {
			if token, ok := attrs[repairReceiptAttribute].(*repairReceiptToken); ok {
				c.once.Do(func() { c.captured <- token.owner })
			}
		}
		return n, attrs, err
	})
}
