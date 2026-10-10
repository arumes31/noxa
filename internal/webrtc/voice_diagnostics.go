package webrtc

import (
	"math"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"noxa/internal/netproto"
)

// One small counter set per live audio publication. Arrival gaps describe
// delivery timing, not packet loss: intentional DTX/silence may also cause gaps.
// The RTP timestamp-based jitter calculation excludes ordinary DTX intervals.
type audioIngressDiagnostic struct {
	mu                                    sync.Mutex
	token                                 uint64
	started, first, last, windowAt        time.Time
	ssrc, timestamp                       uint32
	packets, bytes, windowPackets, bursts uint64
	jitter, maxGap                        float64
}

func (r *Router) startAudioIngress(clientID, slot string, token uint64) *audioIngressDiagnostic {
	input := &audioIngressDiagnostic{token: token, started: time.Now()}
	r.mu.Lock()
	if r.slotClaims[clientID][slot].token != token {
		r.mu.Unlock()
		return input
	}
	if r.audioIngress == nil {
		r.audioIngress = make(map[publicationKey]*audioIngressDiagnostic)
	}
	r.audioIngress[publicationKey{clientID, slot}] = input
	r.mu.Unlock()
	return input
}

func (r *Router) stopAudioIngress(clientID, slot string, input *audioIngressDiagnostic) {
	r.mu.Lock()
	key := publicationKey{clientID, slot}
	if r.audioIngress[key] == input {
		delete(r.audioIngress, key)
	}
	r.mu.Unlock()
}

func (d *audioIngressDiagnostic) observe(packet *rtp.Packet, now time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.last.IsZero() && packet.SSRC != d.ssrc {
		d.first, d.last, d.windowAt = time.Time{}, time.Time{}, time.Time{}
		d.packets, d.bytes, d.windowPackets, d.bursts = 0, 0, 0, 0
		d.jitter, d.maxGap = 0, 0
		d.started = now
	}
	if d.windowAt.IsZero() || now.Sub(d.windowAt) >= 5*time.Second {
		d.windowAt = now
		d.windowPackets, d.bursts = 0, 0
		d.maxGap = 0
	}
	if !d.last.IsZero() {
		arrivalMS := float64(now.Sub(d.last)) / float64(time.Millisecond)
		// RTP clocks wrap at 32 bits; retain negative deltas for reordered packets.
		timestampDelta := int64(packet.Timestamp) - int64(d.timestamp)
		if timestampDelta > math.MaxInt32 {
			timestampDelta -= 1 << 32
		} else if timestampDelta < math.MinInt32 {
			timestampDelta += 1 << 32
		}
		rtpMS := float64(timestampDelta) / 48
		d.jitter += (math.Abs(arrivalMS-rtpMS) - d.jitter) / 16
		d.maxGap = max(d.maxGap, arrivalMS)
		if arrivalMS >= 0 && arrivalMS < 2 {
			d.bursts++
		}
	} else {
		d.first = now
	}
	d.ssrc, d.timestamp, d.last = packet.SSRC, packet.Timestamp, now
	d.packets++
	d.windowPackets++
	if size := packet.MarshalSize(); size > 0 {
		d.bytes += uint64(size)
	}
}

func (d *audioIngressDiagnostic) snapshot(now time.Time) *netproto.VoiceIngressDiagnostics {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.last.IsZero() {
		return nil
	}
	age := max(0, now.Sub(d.last).Milliseconds())
	sampleMS := max(0, float64(d.last.Sub(d.windowAt))/float64(time.Millisecond))
	result := &netproto.VoiceIngressDiagnostics{Publication: strconv.FormatUint(d.token, 10), SSRC: d.ssrc,
		StartedAt: d.started.UnixMilli(), AgeMS: age, Stale: age > 15000, Packets: d.packets, Bytes: d.bytes,
		SampleMS: sampleMS, JitterMS: d.jitter, MaxGapMS: d.maxGap, BurstPackets: d.bursts}
	if sampleMS > 0 && d.windowPackets > 1 {
		rate := float64(d.windowPackets-1) * 1000 / sampleMS
		result.PacketsPerSecond = &rate
	}
	return result
}

func (r *Router) voiceMediaPaths(clientID string, now time.Time) []netproto.VoiceMediaPath {
	paths := make([]netproto.VoiceMediaPath, 0)
	r.mu.RLock()
	for publisher, tracks := range r.pubTracks[clientID] {
		for slot, output := range tracks.audio {
			input := r.audioIngress[publicationKey{publisher, slot}]
			if input == nil || output.sender == nil {
				continue
			}
			parameters := output.sender.GetParameters()
			if len(parameters.Encodings) == 0 {
				continue
			}
			paths = append(paths, netproto.VoiceMediaPath{PublisherID: publisher, Slot: slot,
				OutputSSRC: uint32(parameters.Encodings[0].SSRC), Ingress: input.snapshot(now)})
		}
	}
	r.mu.RUnlock()
	sort.Slice(paths, func(i, j int) bool { return paths[i].OutputSSRC < paths[j].OutputSSRC })
	if len(paths) > netproto.MaxVoiceDiagnosticTracks {
		paths = paths[:netproto.MaxVoiceDiagnosticTracks]
	}
	return paths
}

type audioReceiverReport struct {
	report     rtcp.ReceptionReport
	receivedAt time.Time
}

// Reuse the existing RTCP drain: this Pion version does not collect remote
// inbound stats for senders. Retain one report per active audio binding.
func (r *Router) recordAudioReceiverReports(clientID string, sender *webrtc.RTPSender, reports []rtcp.ReceptionReport) {
	if sender == nil || len(reports) == 0 {
		return
	}
	parameters := sender.GetParameters()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, tracks := range r.pubTracks[clientID] {
		for _, output := range tracks.audio {
			if output.sender != sender {
				continue
			}
			for _, report := range reports {
				for _, encoding := range parameters.Encodings {
					if report.SSRC == uint32(encoding.SSRC) {
						output.receiverReport = &audioReceiverReport{report: report, receivedAt: time.Now()}
						return
					}
				}
			}
		}
	}
}

func (r *Router) voiceReceiverReports(clientID string) []netproto.VoiceServerTrack {
	result := make([]netproto.VoiceServerTrack, 0)
	now := time.Now()
	r.mu.RLock()
	for publisher, tracks := range r.pubTracks[clientID] {
		for slot, output := range tracks.audio {
			if output.receiverReport == nil {
				continue
			}
			sample := output.receiverReport
			age := max(0, now.Sub(sample.receivedAt).Milliseconds())
			// RTCP encodes cumulative loss as signed 24-bit, despite Pion's
			// unsigned field. Widen before subtracting to avoid overflow.
			lost := int64(sample.report.TotalLost & 0xffffff)
			if lost&0x800000 != 0 {
				lost -= 0x1000000
			}
			result = append(result, netproto.VoiceServerTrack{
				SSRC: sample.report.SSRC, PublisherID: publisher, Slot: slot,
				ReceivedAt: sample.receivedAt.UnixMilli(), AgeMS: age, Stale: age > 15000,
				PacketsLost:  lost,
				FractionLost: float64(sample.report.FractionLost) / 256,
				JitterMS:     float64(sample.report.Jitter) / 48, // Opus RTP clock: 48 kHz.
			})
		}
	}
	r.mu.RUnlock()
	sort.Slice(result, func(i, j int) bool { return result[i].SSRC < result[j].SSRC })
	if len(result) > netproto.MaxVoiceDiagnosticTracks {
		result = result[:netproto.MaxVoiceDiagnosticTracks]
	}
	return result
}

func (v *Voice) VoiceDiagnostics(clientID string) *netproto.VoiceTransportDiagnostics {
	peer := v.engine.PeerConnection(clientID)
	if peer == nil {
		return nil
	}
	return &netproto.VoiceTransportDiagnostics{ConnectionState: peer.pc.ConnectionState().String(), ReceiverReports: v.router.voiceReceiverReports(clientID), Paths: v.router.voiceMediaPaths(clientID, time.Now())}
}
