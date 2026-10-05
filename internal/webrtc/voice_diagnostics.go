package webrtc

import (
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
	"noxa/internal/netproto"
	"sort"
	"time"
)

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
			result = append(result, netproto.VoiceServerTrack{
				SSRC: sample.report.SSRC, PublisherID: publisher, Slot: slot,
				ReceivedAt: sample.receivedAt.UnixMilli(), AgeMS: age, Stale: age > 15000,
				PacketsLost:  int32(sample.report.TotalLost<<8) >> 8,
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
	return &netproto.VoiceTransportDiagnostics{ConnectionState: peer.pc.ConnectionState().String(), ReceiverReports: v.router.voiceReceiverReports(clientID)}
}
