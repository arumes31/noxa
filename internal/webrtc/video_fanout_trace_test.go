//go:build integration

package webrtc

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
)

// Source read plus admission/final for two viewers exceeds 65k events over
// the retained 16-second interval at typical 1080p packet rates.
const browserTracePackets = 131072
const browserTraceFeedback = 1024
const browserTracePairs = 64
const browserTraceTail = 8 * time.Second

// All storage is fixed-size; no payload, addresses, SDP, CSRC authority tickets,
// or arbitrary strings are retained. The opt-in fixture admits synthetic peers
// only. Role: 1 source, 2 healthy control, 3 impaired receiver.
// Stage: 1 source read, 2 primary admission, 3 final successful write, 4 failed write.
type browserTraceRTP struct {
	AtNS         int64  `json:"at_ns"`
	SSRC         uint32 `json:"ssrc"`
	Timestamp    uint32 `json:"timestamp"`
	NALTypes     uint32 `json:"nal_types"`
	Sequence     uint16 `json:"sequence"`
	OSN          uint16 `json:"osn"`
	PayloadBytes uint16 `json:"payload_bytes"`
	Role         uint8  `json:"role"`
	Stage        uint8  `json:"stage"`
	PT           uint8  `json:"pt"`
	Flags        uint8  `json:"flags"` // marker1, padding2, RTX4, FUstart8, FUend16, malformed32, truncated64
}

type browserTraceRTCP struct {
	AtNS      int64                            `json:"at_ns"`
	MediaSSRC uint32                           `json:"media_ssrc"`
	Role      uint8                            `json:"role"`
	Kind      uint8                            `json:"kind"` // received NACK1, received PLI2, received FIR3, sent PLI4
	Count     uint8                            `json:"pair_count"`
	Truncated bool                             `json:"truncated"`
	Pairs     [browserTracePairs]rtcp.NackPair `json:"pairs"`
}

type browserMediaTrace struct {
	mu                                                       sync.Mutex
	origin                                                   time.Time
	roles                                                    map[string]uint8
	videoSSRC                                                map[string]map[uint32]struct{}
	packets                                                  [browserTracePackets]browserTraceRTP
	feedback                                                 [browserTraceFeedback]browserTraceRTCP
	packetHead, packetCount, feedbackHead, feedbackCount     int
	active, stopped                                          bool
	timedOut                                                 bool
	deadline                                                 time.Time
	clearRequest, clearApplied, clearResponse                int64
	packetOverflow, feedbackOverflow, truncated, parseErrors uint64
}

func newBrowserMediaTrace(profile browserFanoutProfile, enabled string) (*browserMediaTrace, error) {
	if enabled == "" {
		return nil, nil
	}
	if enabled != "1" || !profile.Capacity || profile.Viewers != 5 || profile.Codec != "h264" || profile.SlowWriteMS == 0 {
		return nil, fmt.Errorf("media trace requires opt-in H264 five-viewer capacity with bounded impairment")
	}
	return &browserMediaTrace{origin: time.Now(), roles: make(map[string]uint8), videoSSRC: make(map[string]map[uint32]struct{})}, nil
}

func (t *browserMediaTrace) bindVideo(pcID string, ssrc uint32) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.roles[pcID] == 0 {
		return
	}
	if t.videoSSRC[pcID] == nil {
		t.videoSSRC[pcID] = make(map[uint32]struct{})
	}
	if _, exists := t.videoSSRC[pcID][ssrc]; exists {
		return
	}
	if len(t.videoSSRC[pcID]) >= 16 {
		t.truncated++
		return
	}
	t.videoSSRC[pcID][ssrc] = struct{}{}
}

func (t *browserMediaTrace) assignPeer(pcID, fixtureID string) {
	if t == nil {
		return
	}
	var role uint8
	switch fixtureID {
	case "a":
		role = 1
	case "viewer":
		role = 2
	case "viewer5":
		role = 3
	default:
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.roles) < 3 {
		t.roles[pcID] = role
	}
}

func (t *browserMediaTrace) impairment(active bool, phase uint8) {
	if t == nil {
		return
	}
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	if active {
		if !t.active && !t.stopped {
			t.active = true
			t.deadline = now.Add(90 * time.Second)
		}
		return
	}
	at := now.Sub(t.origin).Nanoseconds()
	switch phase {
	case 1:
		t.clearRequest = at
	case 2:
		t.clearApplied = at
	case 3:
		t.clearResponse = at
	}
}

func (t *browserMediaTrace) roleLocked(pcID string, now time.Time) uint8 {
	if !t.active || t.stopped {
		return 0
	}
	if !now.Before(t.deadline) {
		t.timedOut = true
		t.stopped = true
		return 0
	}
	if t.clearApplied != 0 && now.Sub(t.origin).Nanoseconds() > t.clearApplied+int64(browserTraceTail) {
		t.stopped = true
		return 0
	}
	return t.roles[pcID]
}

func (t *browserMediaTrace) parseError(pcID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.roleLocked(pcID, time.Now()) != 0 {
		t.parseErrors++
	}
}

// Parse only structural NAL headers. Never retain or decode the media payload.
// Bounds are deliberately small even if a malformed synthetic packet arrives.
func browserTraceNAL(payload []byte) (types uint32, flags uint8) {
	if len(payload) == 0 {
		return 0, 0
	}
	typ := payload[0] & 31
	if typ == 28 {
		if len(payload) < 2 {
			return 0, 32
		}
		types = 1 << (payload[1] & 31)
		if payload[1]&0x80 != 0 {
			flags |= 8
		}
		if payload[1]&0x40 != 0 {
			flags |= 16
		}
		return types, flags
	}
	if typ != 24 {
		return 1 << typ, 0
	}
	payload = payload[1:]
	for n := 0; len(payload) > 0; n++ {
		if n == 64 {
			return types, 64
		}
		if len(payload) < 2 {
			return types, 32
		}
		size := int(binary.BigEndian.Uint16(payload[:2]))
		payload = payload[2:]
		if size == 0 || size > len(payload) {
			return types, 32
		}
		types |= 1 << (payload[0] & 31)
		payload = payload[size:]
	}
	return types, 0
}

func (t *browserMediaTrace) recordRTP(pcID string, stage uint8, rtx uint32, h *rtp.Header, payload []byte) {
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	role := t.roleLocked(pcID, now)
	if role == 0 || (stage == 1 && role != 1) || (stage != 1 && role == 1) {
		return
	}
	e := browserTraceRTP{AtNS: now.Sub(t.origin).Nanoseconds(), Role: role, Stage: stage, SSRC: h.SSRC, Timestamp: h.Timestamp, Sequence: h.SequenceNumber, PT: h.PayloadType}
	if h.Marker {
		e.Flags |= 1
	}
	if h.Padding {
		e.Flags |= 2
	}
	if len(payload) > 65535 {
		e.Flags |= 64
	} else {
		e.PayloadBytes = uint16(len(payload))
	}
	if rtx != 0 && h.SSRC == rtx {
		e.Flags |= 4
		if len(payload) < 2 {
			e.Flags |= 32
		} else {
			e.OSN = binary.BigEndian.Uint16(payload[:2])
			payload = payload[2:]
		}
	}
	types, flags := browserTraceNAL(payload)
	e.NALTypes = types
	e.Flags |= flags
	if e.Flags&64 != 0 {
		t.truncated++
	}
	if t.packetCount == browserTracePackets {
		cutoff := e.AtNS - int64(browserTraceTail)
		if t.clearRequest != 0 {
			cutoff = t.clearRequest - int64(browserTraceTail)
		}
		if t.packets[t.packetHead].AtNS >= cutoff {
			t.packetOverflow++
		}
		t.packetHead = (t.packetHead + 1) % browserTracePackets
		t.packetCount--
	}
	t.packets[(t.packetHead+t.packetCount)%browserTracePackets] = e
	t.packetCount++
}

func (t *browserMediaTrace) recordRTCP(pcID string, packets []rtcp.Packet, outgoing bool) {
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	role := t.roleLocked(pcID, now)
	if role == 0 || (outgoing && role != 1) || (!outgoing && role == 1) {
		return
	}
	for _, p := range packets {
		e := browserTraceRTCP{AtNS: now.Sub(t.origin).Nanoseconds(), Role: role}
		switch p := p.(type) {
		case *rtcp.TransportLayerNack:
			if outgoing {
				continue
			}
			e.Kind = 1
			e.MediaSSRC = p.MediaSSRC
			e.Count = uint8(min(len(p.Nacks), browserTracePairs))
			copy(e.Pairs[:], p.Nacks[:e.Count])
			e.Truncated = len(p.Nacks) > browserTracePairs
		case *rtcp.PictureLossIndication:
			e.Kind = 2
			e.MediaSSRC = p.MediaSSRC
			if outgoing {
				e.Kind = 4
			}
		case *rtcp.FullIntraRequest:
			if outgoing || len(p.FIR) == 0 {
				continue
			}
			e.Kind = 3
			e.MediaSSRC = p.FIR[0].SSRC
			e.Truncated = len(p.FIR) > 1
		default:
			continue
		}
		if _, ok := t.videoSSRC[pcID][e.MediaSSRC]; !ok {
			continue
		}
		if e.Truncated {
			t.truncated++
		}
		if t.feedbackCount == browserTraceFeedback {
			cutoff := e.AtNS - int64(browserTraceTail)
			if t.clearRequest != 0 {
				cutoff = t.clearRequest - int64(browserTraceTail)
			}
			if t.feedback[t.feedbackHead].AtNS >= cutoff {
				t.feedbackOverflow++
			}
			t.feedbackHead = (t.feedbackHead + 1) % browserTraceFeedback
			t.feedbackCount--
		}
		t.feedback[(t.feedbackHead+t.feedbackCount)%browserTraceFeedback] = e
		t.feedbackCount++
	}
}

type browserTraceInterceptor struct {
	interceptor.NoOp
	trace *browserMediaTrace
	pcID  string
	final bool
}

func (i *browserTraceInterceptor) BindLocalStream(info *interceptor.StreamInfo, writer interceptor.RTPWriter) interceptor.RTPWriter {
	if !strings.EqualFold(info.MimeType, "video/H264") {
		return writer
	}
	i.trace.bindVideo(i.pcID, info.SSRC)
	return interceptor.RTPWriterFunc(func(h *rtp.Header, payload []byte, attrs interceptor.Attributes) (int, error) {
		if !i.final {
			i.trace.recordRTP(i.pcID, 2, info.SSRCRetransmission, h, payload)
		}
		n, err := writer.Write(h, payload, attrs)
		if i.final {
			stage := uint8(4)
			if n > 0 && err == nil {
				stage = 3
			}
			i.trace.recordRTP(i.pcID, stage, info.SSRCRetransmission, h, payload)
		}
		return n, err
	})
}

func (i *browserTraceInterceptor) BindRemoteStream(info *interceptor.StreamInfo, reader interceptor.RTPReader) interceptor.RTPReader {
	if !i.final || !strings.EqualFold(info.MimeType, "video/H264") {
		return reader
	}
	i.trace.bindVideo(i.pcID, info.SSRC)
	return interceptor.RTPReaderFunc(func(buf []byte, attrs interceptor.Attributes) (int, interceptor.Attributes, error) {
		n, attrs, err := reader.Read(buf, attrs)
		if err == nil && n > 0 {
			var p rtp.Packet
			if p.Unmarshal(buf[:n]) == nil {
				i.trace.recordRTP(i.pcID, 1, info.SSRCRetransmission, &p.Header, p.Payload)
			} else {
				i.trace.parseError(i.pcID)
			}
		}
		return n, attrs, err
	})
}

func (i *browserTraceInterceptor) BindRTCPReader(reader interceptor.RTCPReader) interceptor.RTCPReader {
	if !i.final {
		return reader
	}
	return interceptor.RTCPReaderFunc(func(buf []byte, attrs interceptor.Attributes) (int, interceptor.Attributes, error) {
		n, attrs, err := reader.Read(buf, attrs)
		if err == nil && n > 0 {
			if packets, parseErr := rtcp.Unmarshal(buf[:n]); parseErr == nil {
				i.trace.recordRTCP(i.pcID, packets, false)
			} else {
				i.trace.parseError(i.pcID)
			}
		}
		return n, attrs, err
	})
}

func (i *browserTraceInterceptor) BindRTCPWriter(writer interceptor.RTCPWriter) interceptor.RTCPWriter {
	if !i.final {
		return writer
	}
	return interceptor.RTCPWriterFunc(func(packets []rtcp.Packet, attrs interceptor.Attributes) (int, error) {
		n, err := writer.Write(packets, attrs)
		if n > 0 && err == nil {
			i.trace.recordRTCP(i.pcID, packets, true)
		}
		return n, err
	})
}

func (t *browserMediaTrace) save(path string) error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stopped = true
	cutoff := t.clearRequest - int64(browserTraceTail)
	packets := make([]browserTraceRTP, 0, t.packetCount)
	var stages [4]uint8
	for n := 0; n < t.packetCount; n++ {
		e := t.packets[(t.packetHead+n)%browserTracePackets]
		if e.AtNS >= cutoff {
			packets = append(packets, e)
			stages[e.Role] |= 1 << e.Stage
		}
	}
	feedback := make([]browserTraceRTCP, 0, t.feedbackCount)
	for n := 0; n < t.feedbackCount; n++ {
		e := t.feedback[(t.feedbackHead+n)%browserTraceFeedback]
		if e.AtNS >= cutoff {
			feedback = append(feedback, e)
		}
	}
	complete := t.clearRequest > 0 && t.clearApplied > 0 && t.clearResponse > 0 &&
		time.Since(t.origin).Nanoseconds() >= t.clearApplied+int64(browserTraceTail) &&
		stages[1]&(1<<1) != 0 && stages[2]&(1<<2|1<<3) == 1<<2|1<<3 && stages[3]&(1<<2|1<<3) == 1<<2|1<<3 &&
		!t.timedOut && t.packetOverflow == 0 && t.feedbackOverflow == 0 && t.truncated == 0 && t.parseErrors == 0
	data := struct {
		OriginMS                                                 int64 `json:"origin_ms"`
		ClearRequestNS                                           int64 `json:"clear_request_ns"`
		ClearAppliedNS                                           int64 `json:"clear_applied_ns"`
		ClearResponseWrittenNS                                   int64 `json:"clear_response_written_ns"`
		Complete                                                 bool  `json:"complete"`
		PacketOverflow, FeedbackOverflow, Truncated, ParseErrors uint64
		Packets                                                  []browserTraceRTP  `json:"rtp"`
		Feedback                                                 []browserTraceRTCP `json:"rtcp"`
		Note                                                     string             `json:"note"`
	}{t.origin.UnixMilli(), t.clearRequest, t.clearApplied, t.clearResponse, complete, t.packetOverflow, t.feedbackOverflow, t.truncated, t.parseErrors, packets, feedback,
		"Synthetic local H264 only. Roles1=source,2=control,3=impaired. RTP stages1=source read,2=primary admission before cache/pacer,3=successful final Pion writer return,4=failed final write. Success does not prove browser SRTP/decode acceptance. NAL bitmask is1<<NALtype; flags marker1,padding2,RTX4,FUstart8,FUend16,malformed32,truncated64. Cache misses are not observable. Fixed event bounds; no payload/CSRC/SDP/addresses. Clear response timestamp means server encoded response, not browser receipt."}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	err = json.NewEncoder(f).Encode(data)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if !complete {
		return fmt.Errorf("bounded recovery trace incomplete; inspect overflow/truncation/clear markers")
	}
	return nil
}

func TestBrowserRecoveryTraceScopeAndWriterOutcome(t *testing.T) {
	trace, err := newBrowserMediaTrace(browserFanoutProfile{Capacity: true, Viewers: 5, Codec: "h264", SlowWriteMS: 5}, "1")
	if err != nil {
		t.Fatal(err)
	}
	trace.assignPeer("pub", "a")
	trace.assignPeer("healthy", "viewer")
	trace.assignPeer("slow", "viewer5")
	trace.assignPeer("other", "viewer2")
	trace.impairment(true, 1)
	h := rtp.Header{Version: 2, SSRC: 22, SequenceNumber: 100, Timestamp: 90000, Marker: true, CSRC: []uint32{1, 2}}
	payload := []byte{0, 7, 0x7c, 0x85, 0xaa}
	trace.recordRTP("other", 3, 22, &h, payload)
	if trace.packetCount != 0 {
		t.Fatal("unselected peer retained metadata")
	}
	trace.recordRTP("slow", 2, 22, &h, payload)
	e := trace.packets[0]
	if e.Role != 3 || e.OSN != 7 || e.NALTypes != 1<<5 || e.Flags != 1|4|8 {
		t.Fatalf("wrong structural metadata: %+v", e)
	}
	payload[4] = 0xbb
	h.SequenceNumber = 101
	if trace.packets[0].Sequence != 100 {
		t.Fatal("trace retained mutable packet")
	}
	i := &browserTraceInterceptor{trace: trace, pcID: "healthy", final: true}
	writes := 0
	writer := i.BindLocalStream(&interceptor.StreamInfo{MimeType: "video/H264", SSRCRetransmission: 22}, interceptor.RTPWriterFunc(func(*rtp.Header, []byte, interceptor.Attributes) (int, error) {
		writes++
		return 0, fmt.Errorf("synthetic write failure")
	}))
	if _, err := writer.Write(&h, payload, nil); err == nil {
		t.Fatal("trace swallowed write failure")
	}
	if writes != 1 || trace.packets[1].Stage != 4 {
		t.Fatal("failed socket falsely marked successful")
	}
	writer = i.BindLocalStream(&interceptor.StreamInfo{MimeType: "video/H264"}, interceptor.RTPWriterFunc(func(*rtp.Header, []byte, interceptor.Attributes) (int, error) { return 1, nil }))
	if _, err := writer.Write(&h, payload, nil); err != nil || trace.packets[2].Stage != 3 {
		t.Fatal("successful writer result missing")
	}
}

func TestBrowserRecoveryTraceBoundsAndNAL(t *testing.T) {
	trace, err := newBrowserMediaTrace(browserFanoutProfile{Capacity: true, Viewers: 5, Codec: "h264", SlowWriteMS: 5}, "1")
	if err != nil {
		t.Fatal(err)
	}
	trace.assignPeer("slow", "viewer5")
	trace.bindVideo("slow", 11)
	trace.impairment(true, 1)
	h := rtp.Header{Version: 2, SSRC: 11}
	for range browserTracePackets + 1 {
		trace.recordRTP("slow", 2, 0, &h, []byte{0x65, 0})
	}
	if trace.packetCount != browserTracePackets || trace.packetOverflow != 1 {
		t.Fatal("packet memory/overflow bound lost")
	}
	nack := &rtcp.TransportLayerNack{MediaSSRC: 11, Nacks: make([]rtcp.NackPair, browserTracePairs+1)}
	for range browserTraceFeedback + 1 {
		trace.recordRTCP("slow", []rtcp.Packet{nack}, false)
	}
	if trace.feedbackCount != browserTraceFeedback || trace.feedbackOverflow != 1 || trace.truncated != browserTraceFeedback+1 {
		t.Fatal("feedback memory/truncation bound lost")
	}
	types, flags := browserTraceNAL([]byte{24, 0, 2, 0x67, 1, 0, 2, 0x68, 1, 0, 2, 0x65, 1})
	if types != (1<<7|1<<8|1<<5) || flags != 0 {
		t.Fatal("STAP parameter/keyframe headers lost")
	}
	if _, flags := browserTraceNAL([]byte{24, 0, 4, 0x67}); flags != 32 {
		t.Fatal("invalid structural size accepted")
	}
	trace.deadline = time.Now().Add(-time.Second)
	trace.recordRTP("slow", 2, 0, &h, nil)
	if !trace.stopped || !trace.timedOut || trace.packetOverflow != 1 {
		t.Fatal("hard time bound lost")
	}
	if _, err := newBrowserMediaTrace(browserFanoutProfile{Capacity: true, Viewers: 20, Codec: "h264", SlowWriteMS: 5}, "1"); err == nil {
		t.Fatal("unapproved trace scope accepted")
	}
	trace, err = newBrowserMediaTrace(browserFanoutProfile{Capacity: true, Viewers: 5, Codec: "h264", SlowWriteMS: 5}, "1")
	if err != nil {
		t.Fatal(err)
	}
	trace.origin = trace.origin.Add(-20 * time.Second)
	trace.assignPeer("slow", "viewer5")
	trace.impairment(true, 1)
	trace.clearApplied = int64(10 * time.Second)
	trace.recordRTP("slow", 2, 0, &h, nil)
	if !trace.stopped || trace.timedOut || trace.packetCount != 0 {
		t.Fatal("post-clear recording exceeded eight-second tail")
	}
}

func TestBrowserRecoveryTraceProductionChainAndRepairBoundary(t *testing.T) {
	trace, err := newBrowserMediaTrace(browserFanoutProfile{Capacity: true, Viewers: 5, Codec: "h264", SlowWriteMS: 5}, "1")
	if err != nil {
		t.Fatal(err)
	}
	trace.assignPeer("peer", "viewer5")
	trace.impairment(true, 1)
	_, registry, err := newEngineMedia(false, VideoBounds{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	chain, err := (browserDelayedProductionFactory{base: registry, trace: trace}).NewInterceptor("peer")
	if err != nil {
		t.Fatal(err)
	}
	var closeOnce sync.Once
	var closeErr error
	closeChain := func() { closeOnce.Do(func() { closeErr = chain.Close() }) }
	t.Cleanup(closeChain)
	written := make(chan uint32, 2)
	info := &interceptor.StreamInfo{ID: "synthetic", SSRC: 11, SSRCRetransmission: 22, PayloadType: 102, PayloadTypeRetransmission: 103,
		MimeType: "video/H264", ClockRate: 90000, RTCPFeedback: []interceptor.RTCPFeedback{{Type: "nack"}}}
	writer := chain.BindLocalStream(info, interceptor.RTPWriterFunc(func(h *rtp.Header, payload []byte, _ interceptor.Attributes) (int, error) {
		written <- h.SSRC
		return h.MarshalSize() + len(payload), nil
	}))
	header := rtp.Header{Version: 2, SSRC: 11, SequenceNumber: 100, Timestamp: 90000, PayloadType: 102}
	if _, err := writer.Write(&header, []byte{0x65, 1}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-written:
	case <-time.After(time.Second):
		t.Fatal("primary did not cross final writer")
	}
	feedback, err := rtcp.Marshal([]rtcp.Packet{&rtcp.TransportLayerNack{MediaSSRC: 11, Nacks: []rtcp.NackPair{{PacketID: 100}}}})
	if err != nil {
		t.Fatal(err)
	}
	reader := chain.BindRTCPReader(interceptor.RTCPReaderFunc(func(buf []byte, attrs interceptor.Attributes) (int, interceptor.Attributes, error) {
		return copy(buf, feedback), attrs, nil
	}))
	if _, _, err := reader.Read(make([]byte, 1500), nil); err != nil {
		t.Fatal(err)
	}
	select {
	case ssrc := <-written:
		if ssrc != 22 {
			t.Fatal("repair mapping changed")
		}
	case <-time.After(time.Second):
		t.Fatal("repair did not cross final writer")
	}
	closeChain()
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	trace.mu.Lock()
	defer trace.mu.Unlock()
	if trace.packetCount != 3 || trace.packets[0].Stage != 2 || trace.packets[1].Stage != 3 || trace.packets[2].Stage != 3 ||
		trace.packets[2].Flags&4 == 0 || trace.packets[2].OSN != 100 || trace.feedbackCount != 1 {
		t.Fatalf("admission/final/repair boundaries changed: count=%d packets=%+v", trace.packetCount, trace.packets[:3])
	}
}

func TestBrowserRecoveryTraceIncompleteArtifactIsPreserved(t *testing.T) {
	trace, err := newBrowserMediaTrace(browserFanoutProfile{Capacity: true, Viewers: 5, Codec: "h264", SlowWriteMS: 5}, "1")
	if err != nil {
		t.Fatal(err)
	}
	// Avoid depending on the Windows clock advancing between immediate markers.
	trace.origin = trace.origin.Add(-time.Second)
	trace.impairment(true, 1)
	trace.impairment(false, 1)
	trace.impairment(false, 2)
	trace.impairment(false, 3)
	path := filepath.Join(t.TempDir(), "trace.json")
	if err := trace.save(path); err == nil {
		t.Fatal("short/missing-role window falsely marked complete")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		Complete      bool  `json:"complete"`
		ClearRequest  int64 `json:"clear_request_ns"`
		ClearResponse int64 `json:"clear_response_written_ns"`
	}
	if err := json.Unmarshal(data, &saved); err != nil || saved.Complete || saved.ClearRequest == 0 || saved.ClearResponse == 0 {
		t.Fatalf("incomplete artifact or clear context lost: %+v %v", saved, err)
	}
}
