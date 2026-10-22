package server

import (
	"noxa/internal/netproto"
	"strings"
	"time"
)

// RTT is measured by the server; loss/jitter are fresh client receiver reports.
// Loss is packet-weighted over current streams' cumulative received/lost
// counters. Jitter is the mean of measured receivers, not a maximum or an RTT.
func (s *TCPServer) serverOverview(now time.Time) *netproto.ServerOverview {
	if s.deps == nil || s.deps.State == nil {
		return nil
	}
	result := &netproto.ServerOverview{ObservedAt: now.UnixMilli()}
	s.mu.RLock()
	clients := make([]*Client, 0, len(s.clients))
	for _, client := range s.clients {
		clients = append(clients, client)
	}
	s.mu.RUnlock()
	var rtt, jitter, received, lost float64
	for _, client := range clients {
		if !client.isAuthed() {
			continue
		}
		member, exists := s.deps.State.GetClient(client.ID)
		if !exists {
			continue
		}
		result.Clients++
		client.mu.RLock()
		if client.rttKnown && !client.rttObservedAt.IsZero() && now.Sub(client.rttObservedAt) <= 30*time.Second {
			rtt += float64(client.rttNs) / float64(time.Millisecond)
			result.LatencySamples++
		}
		report := client.voiceTelemetry
		if report != nil && now.Sub(client.voiceTelemetryAt) <= 15*time.Second && client.voiceTelemetryEpoch == member.ChannelEpoch && report.ChannelID == member.ChannelID && member.ChannelID > 0 {
			for _, track := range report.Tracks {
				if track.Codec != "" && !strings.HasPrefix(strings.ToLower(track.Codec), "audio/") {
					continue
				}
				if track.PacketsReceived != nil && track.PacketsLost != nil && *track.PacketsReceived+max(0, *track.PacketsLost) > 0 {
					received += *track.PacketsReceived
					lost += max(0, *track.PacketsLost)
					result.LossSamples++
				}
				if track.JitterMS != nil && track.PacketsReceived != nil && *track.PacketsReceived > 0 {
					jitter += *track.JitterMS
					result.JitterSamples++
				}
			}
		}
		client.mu.RUnlock()
	}
	if result.LatencySamples > 0 {
		value := rtt / float64(result.LatencySamples)
		result.LatencyMS = &value
	}
	if result.JitterSamples > 0 {
		value := jitter / float64(result.JitterSamples)
		result.JitterMS = &value
	}
	if received+lost > 0 {
		value := 100 * lost / (received + lost)
		result.LossPercent = &value
	}
	if s.deps.TrafficHistory != nil {
		summary := s.deps.TrafficHistory.Snapshot()
		result.Traffic = &summary
	}
	return result
}
