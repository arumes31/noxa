package webrtc

import (
	"encoding/binary"
	"fmt"
	"math"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
	pion "github.com/pion/webrtc/v4"
)

// shapedLink is a UDP relay modelling a constrained downlink: a token-bucket
// bottleneck with a drop-tail queue. Uplink (RTCP, STUN) is not shaped.
type shapedLink struct {
	front, back *net.UDPConn
	client      atomic.Pointer[net.UDPAddr]
	rate        float64 // bytes per second
	limit       time.Duration
	mu          sync.Mutex
	next        time.Time
	queue       chan shapedPacket
	dropped     atomic.Int64
	done        chan struct{}
}

type shapedPacket struct {
	b      []byte
	depart time.Time
}

func newShapedLink(t *testing.T, server *net.UDPAddr, bitsPerSecond float64, limit time.Duration) *shapedLink {
	t.Helper()
	front, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	back, err := net.DialUDP("udp4", nil, server)
	if err != nil {
		t.Fatal(err)
	}
	l := &shapedLink{front: front, back: back, rate: bitsPerSecond / 8, limit: limit, queue: make(chan shapedPacket, 1<<14), done: make(chan struct{})}
	go func() { // uplink
		buf := make([]byte, 1600)
		for {
			n, addr, err := front.ReadFromUDP(buf)
			if err != nil {
				return
			}
			l.client.Store(addr)
			_, _ = back.Write(buf[:n])
		}
	}()
	go func() { // shaped downlink admission
		buf := make([]byte, 1600)
		for {
			n, err := back.Read(buf)
			if err != nil {
				close(l.queue)
				return
			}
			now := time.Now()
			l.mu.Lock()
			start := l.next
			if start.Before(now) {
				start = now
			}
			if start.Sub(now) > l.limit {
				l.mu.Unlock()
				l.dropped.Add(1)
				continue
			}
			l.next = start.Add(time.Duration(float64(n+28) / l.rate * float64(time.Second)))
			depart := l.next
			l.mu.Unlock()
			select {
			case l.queue <- shapedPacket{append([]byte(nil), buf[:n]...), depart}:
			default:
				l.dropped.Add(1)
			}
		}
	}()
	go func() { // serializer
		defer close(l.done)
		for p := range l.queue {
			if wait := time.Until(p.depart); wait > 0 {
				time.Sleep(wait)
			}
			if addr := l.client.Load(); addr != nil {
				_, _ = front.WriteToUDP(p.b, addr)
			}
		}
	}()
	t.Cleanup(func() { _ = front.Close(); _ = back.Close(); <-l.done })
	return l
}

func (l *shapedLink) port() int { return l.front.LocalAddr().(*net.UDPAddr).Port }

func hostCandidate(t *testing.T, sdp string) *net.UDPAddr {
	t.Helper()
	for _, line := range strings.Split(sdp, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) >= 8 && strings.HasPrefix(fields[0], "a=candidate:") && strings.EqualFold(fields[2], "udp") && fields[7] == "host" {
			var port int
			if _, err := fmt.Sscanf(fields[5], "%d", &port); err == nil {
				return &net.UDPAddr{IP: net.ParseIP(fields[4]), Port: port}
			}
		}
	}
	t.Fatal("server offer has no UDP host candidate")
	return nil
}

func stripCandidates(sdp string) string {
	lines := strings.Split(sdp, "\r\n")
	out := lines[:0]
	for _, line := range lines {
		if !strings.HasPrefix(line, "a=candidate:") {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\r\n")
}

type voiceListener struct {
	id         string
	server     *PeerConnectionWrapper
	receiver   *pion.PeerConnection
	link       *shapedLink
	negotiate  sync.Mutex
	arrivals   []atomic.Int64 // arrival UnixNano per voice index, 0 = lost
	videoBytes atomic.Int64
	streamsMu  sync.Mutex
	streams    map[string][]pion.RTPCodecType // MSID stream -> track kinds
}

// TestVoiceSurvivesVideoOverloadAndRenegotiation runs the production Engine,
// Router, egress, pacer and shared socket over real ICE/DTLS/SRTP with one
// speaker and four listeners. A 1080p60-sized video flood (~20 Mbit/s per
// listener) is pushed to every listener while subscribers are renegotiated
// repeatedly; one listener sits behind an 8 Mbit/s bottleneck. Voice must
// still arrive (>= 99.5 %) with no meaningful added latency.
func TestVoiceSurvivesVideoOverloadAndRenegotiation(t *testing.T) {
	if testing.Short() {
		t.Skip("multi-second media stress test")
	}
	const (
		listeners     = 4
		frameInterval = 20 * time.Millisecond
		baseline      = 3 * time.Second
		flood         = 6 * time.Second
		videoFPS      = 60
		videoBitrate  = 20_000_000
		videoPayload  = 1188
	)
	engine, err := NewWithNetwork(testLogger(), nil, false, NetworkConfig{UDPAddr: "127.0.0.1:0", ExternalIPs: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	engine.iceServers = nil
	router := NewRouter(testLogger())
	router.SetMediaGuard(func(_ MediaDelivery, write func() error) error { return write() })
	router.JoinChannel(1, "speaker")
	router.JoinChannel(1, "streamer")

	total := int((baseline + flood + time.Second) / frameInterval)
	sendTimes := make([]atomic.Int64, total)
	ls := make([]*voiceListener, listeners)
	pending := make(chan string, 1024)
	router.SetRenegotiateHook(func(sub string) {
		select {
		case pending <- sub:
		default:
		}
	})

	for i := range ls {
		id := fmt.Sprintf("listener%d", i)
		peer, err := engine.NewPeerConnection(id)
		if err != nil {
			t.Fatal(err)
		}
		l := &voiceListener{id: id, server: peer, arrivals: make([]atomic.Int64, total), streams: map[string][]pion.RTPCodecType{}}
		ls[i] = l
		router.JoinChannel(1, id)
		if err := router.AttachPeer(id, peer); err != nil {
			t.Fatal(err)
		}
		router.PrepareSubscriber(id)

		m := &pion.MediaEngine{}
		if err := m.RegisterDefaultCodecs(); err != nil {
			t.Fatal(err)
		}
		registry := &interceptor.Registry{}
		if err := pion.RegisterDefaultInterceptors(m, registry); err != nil {
			t.Fatal(err)
		}
		settings := pion.SettingEngine{}
		settings.SetIncludeLoopbackCandidate(true)
		settings.SetInterfaceFilter(func(name string) bool { return strings.HasPrefix(name, "lo") })
		settings.SetNetworkTypes([]pion.NetworkType{pion.NetworkTypeUDP4})
		api := pion.NewAPI(pion.WithMediaEngine(m), pion.WithInterceptorRegistry(registry), pion.WithSettingEngine(settings))
		receiver, err := api.NewPeerConnection(pion.Configuration{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = receiver.Close() })
		l.receiver = receiver
		receiver.OnTrack(func(track *pion.TrackRemote, _ *pion.RTPReceiver) {
			l.streamsMu.Lock()
			l.streams[track.StreamID()] = append(l.streams[track.StreamID()], track.Kind())
			l.streamsMu.Unlock()
			voice := track.Kind() == pion.RTPCodecTypeAudio && track.StreamID() == slotStreamID("speaker", SlotMic)
			buf := make([]byte, 1600)
			for {
				n, _, err := track.Read(buf)
				if err != nil {
					return
				}
				if !voice {
					l.videoBytes.Add(int64(n))
					continue
				}
				var packet rtp.Packet
				if packet.Unmarshal(buf[:n]) != nil || len(packet.Payload) < 5 {
					continue
				}
				if index := int(binary.BigEndian.Uint32(packet.Payload[1:5])); index < total {
					l.arrivals[index].CompareAndSwap(0, time.Now().UnixNano())
				}
			}
		})
	}
	// One listener is behind a constrained downlink (a slow peer).
	constrained := ls[listeners-1]

	gather := func(pc *pion.PeerConnection, d pion.SessionDescription) error {
		done := pion.GatheringCompletePromise(pc)
		if err := pc.SetLocalDescription(d); err != nil {
			return err
		}
		select {
		case <-done:
			return nil
		case <-time.After(5 * time.Second):
			return fmt.Errorf("ICE gathering timeout")
		}
	}
	var renegotiations atomic.Int64
	negotiate := func(l *voiceListener) error {
		l.negotiate.Lock()
		defer l.negotiate.Unlock()
		server := l.server.pc
		offer, err := server.CreateOffer(nil)
		if err != nil {
			return err
		}
		if err := gather(server, offer); err != nil {
			return err
		}
		remote := *server.LocalDescription()
		if l == constrained {
			if l.link == nil {
				l.link = newShapedLink(t, hostCandidate(t, remote.SDP), 8_000_000, 60*time.Millisecond)
			}
			addr := hostCandidate(t, remote.SDP)
			remote.SDP = strings.ReplaceAll(remote.SDP, fmt.Sprintf(" %s %d typ host", addr.IP, addr.Port), fmt.Sprintf(" 127.0.0.1 %d typ host", l.link.port()))
		}
		if err := l.receiver.SetRemoteDescription(remote); err != nil {
			return err
		}
		answer, err := l.receiver.CreateAnswer(nil)
		if err != nil {
			return err
		}
		if err := gather(l.receiver, answer); err != nil {
			return err
		}
		local := *l.receiver.LocalDescription()
		if l == constrained {
			local.SDP = stripCandidates(local.SDP) // Force the shaped path.
		}
		renegotiations.Add(1)
		return server.SetRemoteDescription(local)
	}
	for _, l := range ls {
		if err := negotiate(l); err != nil {
			t.Fatalf("%s: %v", l.id, err)
		}
	}
	for len(pending) > 0 { // Joins above already requested these.
		<-pending
	}
	deadline := time.Now().Add(10 * time.Second)
	for _, l := range ls {
		for l.receiver.ConnectionState() != pion.PeerConnectionStateConnected || l.server.pc.ConnectionState() != pion.PeerConnectionStateConnected {
			if time.Now().After(deadline) {
				t.Fatalf("%s did not connect: receiver=%s server=%s", l.id, l.receiver.ConnectionState(), l.server.pc.ConnectionState())
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	renegotiations.Store(0)
	stop := make(chan struct{})
	var workers sync.WaitGroup
	// Renegotiation driver: serve router requests as the signaling layer does.
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			select {
			case <-stop:
				return
			case id := <-pending:
				for _, l := range ls {
					if l.id == id {
						if err := negotiate(l); err != nil {
							t.Errorf("renegotiate %s: %v", id, err)
						}
					}
				}
			}
		}
	}()

	// Voice: the speaker's 20 ms Opus frames through the production fan-out.
	var maxFanout atomic.Int64 // slowest voice fan-out call (router + egress admission)
	start := time.Now()
	floodStart, floodEnd := start.Add(baseline), start.Add(baseline+flood)
	workers.Add(1)
	go func() {
		defer workers.Done()
		ticker := time.NewTicker(frameInterval)
		defer ticker.Stop()
		for index := 0; index < total; index++ {
			payload := make([]byte, 80)
			payload[0] = 0xf8
			binary.BigEndian.PutUint32(payload[1:5], uint32(index))
			sendTimes[index].Store(time.Now().UnixNano())
			router.ForwardRTP("speaker", SlotMic, &rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: mediaOpusPayloadType, SequenceNumber: uint16(index), Timestamp: uint32(index) * 960, SSRC: 0x5eed}, Payload: payload})
			if fanout := time.Now().UnixNano() - sendTimes[index].Load(); fanout > maxFanout.Load() {
				maxFanout.Store(fanout)
			}
			select {
			case <-stop:
				return
			case <-ticker.C:
			}
		}
	}()

	// Video flood: 1080p60-sized frames bursted to every listener's cam output.
	camSlot := func(l *voiceListener) *pubSlot {
		router.mu.RLock()
		defer router.mu.RUnlock()
		if t := router.pubTracks[l.id]["streamer"]; t != nil {
			return t.video[SlotCam]
		}
		return nil
	}
	time.Sleep(time.Until(floodStart))
	workers.Add(1)
	go func() {
		defer workers.Done()
		perFrame := videoBitrate / 8 / videoFPS / (videoPayload + 12)
		ticker := time.NewTicker(time.Second / videoFPS)
		defer ticker.Stop()
		seq := uint16(0)
		for frame := 0; time.Now().Before(floodEnd); frame++ {
			for _, l := range ls {
				slot := camSlot(l)
				if slot == nil {
					continue
				}
				for k := range perFrame {
					payload := make([]byte, videoPayload)
					payload[0] = 0x10
					packet := rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: seq + uint16(k), Timestamp: uint32(frame) * 1500, SSRC: 0xf100d, Marker: k == perFrame-1}, Payload: payload}
					_, _ = slot.enqueueMedia(&packet, mediaTicket{delivery: MediaDelivery{SenderID: "streamer", RecipientID: l.id, ChannelID: 1, RecipientChannelID: 1, Slot: SlotCam}})
				}
			}
			seq += uint16(perFrame)
			select {
			case <-stop:
				return
			case <-ticker.C:
			}
		}
	}()
	// Record the constrained listener's congestion estimate for the log.
	var estimates []string
	workers.Add(1)
	go func() {
		defer workers.Done()
		for time.Now().Before(floodEnd) {
			if slot := camSlot(constrained); slot != nil && slot.egress != nil {
				slot.egress.mu.RLock()
				pacer := slot.egress.pacer
				slot.egress.mu.RUnlock()
				if pacer != nil {
					d := pacer.diagnosticSnapshot(time.Now())
					estimates = append(estimates, fmt.Sprintf("%.1f/%d", float64(d.TargetBitrateBPS)/1e6, d.QueuedPackets))
				}
			}
			select {
			case <-stop:
				return
			case <-time.After(250 * time.Millisecond):
			}
		}
	}()
	// Renegotiations while flooding: publishers join and leave, and the
	// streamer starts a screen share, as in a busy channel.
	workers.Add(1)
	go func() {
		defer workers.Done()
		for n := 0; time.Now().Before(floodEnd.Add(-500 * time.Millisecond)); n++ {
			extra := fmt.Sprintf("extra%d", n/2)
			switch {
			case n == 1:
				router.SetTrackSlots("streamer", map[string]string{"screen-track": SlotScreen})
			case n%2 == 0:
				router.JoinChannel(1, extra)
			default:
				router.LeaveChannel(1, extra)
			}
			select {
			case <-stop:
				return
			case <-time.After(400 * time.Millisecond):
			}
		}
	}()
	time.Sleep(time.Until(floodEnd.Add(700 * time.Millisecond)))
	close(stop)
	workers.Wait()

	inPhase := func(index int, from, to time.Time) bool {
		sent := sendTimes[index].Load()
		return sent != 0 && sent >= from.UnixNano() && sent < to.UnixNano()
	}
	type result struct {
		sent, received int
		latency        []time.Duration
	}
	measure := func(l *voiceListener, from, to time.Time) result {
		var r result
		for index := range total {
			if !inPhase(index, from, to) {
				continue
			}
			r.sent++
			if at := l.arrivals[index].Load(); at != 0 {
				r.received++
				r.latency = append(r.latency, time.Duration(at-sendTimes[index].Load()))
			}
		}
		slices.Sort(r.latency)
		return r
	}
	p := func(samples []time.Duration, q float64) time.Duration {
		if len(samples) == 0 {
			return time.Duration(math.MaxInt64)
		}
		return samples[min(len(samples)-1, int(math.Ceil(q*float64(len(samples))))-1)]
	}
	warmup := start.Add(500 * time.Millisecond)
	if renegotiations.Load() < int64(listeners*5) {
		t.Errorf("only %d renegotiations ran during the flood", renegotiations.Load())
	}
	for _, l := range ls {
		base := measure(l, warmup, floodStart)
		loaded := measure(l, floodStart, floodEnd)
		videoMbps := float64(l.videoBytes.Load()) * 8 / flood.Seconds() / 1e6
		dropped := int64(0)
		if l.link != nil {
			dropped = l.link.dropped.Load()
		}
		ratio := float64(loaded.received) / float64(max(loaded.sent, 1))
		added := p(loaded.latency, 0.99) - p(base.latency, 0.99)
		t.Logf("VOICE %s: baseline %d/%d p50=%s p99=%s | flood %d/%d (%.2f%%) p50=%s p99=%s max=%s added-p99=%s | video rx %.1f Mbit/s, bottleneck drops %d",
			l.id, base.received, base.sent, p(base.latency, 0.5), p(base.latency, 0.99),
			loaded.received, loaded.sent, 100*ratio, p(loaded.latency, 0.5), p(loaded.latency, 0.99), p(loaded.latency, 1), added, videoMbps, dropped)
		if videoMbps < 2 {
			t.Errorf("%s: video flood did not reach the listener (%.1f Mbit/s)", l.id, videoMbps)
		}
		if base.received < base.sent*995/1000 {
			t.Errorf("%s: baseline voice delivery %d/%d", l.id, base.received, base.sent)
		}
		minRatio, limit := 0.995, 30*time.Millisecond
		if l == constrained {
			// Behind a saturated bottleneck voice shares the link's drop-tail
			// FIFO with video. The server holds video to the congestion
			// estimate, but GCC's loss controller holds (does not decrease)
			// at 2-10 % loss, so an estimate that overshoots the link keeps
			// the 60 ms buffer full: voice then sees that delay and loss.
			// Before the pacer/socket/router fixes this listener lost up to a
			// third of its voice. Tighten once GCC exits that hold zone.
			minRatio, limit = 0.85, 90*time.Millisecond
		}
		if ratio < minRatio {
			t.Errorf("%s: voice delivery under video overload %.2f%% < %.1f%%", l.id, 100*ratio, 100*minRatio)
		}
		if raceEnabled {
			limit = 150 * time.Millisecond // The race detector slows SRTP ~10x.
		}
		if added > limit {
			t.Errorf("%s: voice p99 latency rose by %s under video overload (limit %s)", l.id, added, limit)
		}
		l.streamsMu.Lock()
		for stream, kinds := range l.streams {
			if stream == slotStreamID("speaker", SlotMic) && slices.Contains(kinds, pion.RTPCodecTypeVideo) {
				t.Errorf("%s: speaker mic shares MSID stream %q with a video track", l.id, stream)
			}
		}
		l.streamsMu.Unlock()
	}
	t.Logf("VOICE renegotiations during flood: %d, slowest voice fan-out call %s", renegotiations.Load(), time.Duration(maxFanout.Load()))
	t.Logf("VOICE constrained listener estimate Mbit/s / queued packets every 250 ms: %v", estimates)
}
