package server

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"noxa/internal/version"
)

func (s *TCPServer) handleVoiceTelemetry(ctx context.Context, client *Client, frame *netproto.Frame) error {
	var report netproto.VoiceTelemetry
	if len(frame.Payload) > netproto.MaxVoiceTelemetryBytes || netproto.Decode(frame, &report) != nil || !report.Valid() {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid voice diagnostics")
	}
	return s.roleChannelControl(ctx, client, authorization.Connect, true, func(ctx context.Context, channelID int64) error {
		if channelID != report.ChannelID {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeConflict, "voice diagnostics channel changed")
		}
		member, present := s.deps.State.GetClient(client.ID)
		if !present {
			return nil
		}
		client.mu.Lock()
		defer client.mu.Unlock()
		if client.voiceTelemetryEpoch == member.ChannelEpoch && time.Since(client.voiceTelemetryAt) < 2*time.Second {
			return nil
		}
		client.voiceTelemetry, client.voiceTelemetryAt = &report, time.Now()
		client.voiceTelemetryEpoch = member.ChannelEpoch
		retainVoiceHistory(client, report, member.ChannelEpoch, client.voiceTelemetryAt)
		return nil
	})
}

type voiceDiagnosticsBackend interface {
	VoiceDiagnostics(string) *netproto.VoiceTransportDiagnostics
}

func (s *TCPServer) voiceDiagnostics(client *Client) *netproto.VoiceDiagnostics {
	if s.deps.State == nil || !client.isAuthed() {
		return nil
	}
	member, ok := s.deps.State.GetClient(client.ID)
	if !ok {
		return nil
	}
	now := time.Now()
	result := &netproto.VoiceDiagnostics{ServerVersion: version.String(), ObservedAt: now.UnixMilli(), ClientID: client.ID, Nickname: member.Nickname, ChannelID: member.ChannelID, PingMS: -1}
	client.mu.RLock()
	result.ClientVersion = client.clientVersion
	if client.rttKnown {
		result.PingMS = client.rttNs / int64(time.Millisecond)
	}
	if report := client.voiceTelemetry; report != nil && report.ChannelID == member.ChannelID && client.voiceTelemetryEpoch == member.ChannelEpoch {
		age := max(0, now.Sub(client.voiceTelemetryAt).Milliseconds())
		result.ClientReport = &netproto.VoiceClientReport{ReceivedAt: client.voiceTelemetryAt.UnixMilli(), AgeMS: age, Stale: age > 15000, Report: *report}
		for _, point := range client.voiceHistory {
			if client.voiceHistoryEpoch == member.ChannelEpoch && now.UnixMilli()-point.ObservedAt <= 300000 {
				result.History = append(result.History, point)
			}
		}
	}
	client.mu.RUnlock()
	if backend, ok := s.deps.Voice.(voiceDiagnosticsBackend); ok {
		result.Transport = backend.VoiceDiagnostics(client.ID)
	}
	s.correlateVoiceDiagnostics(result)
	return result
}

// Called with client.mu held. Only summaries are retained, at most 60 samples
// and five minutes; disconnect and channel epoch changes isolate sessions.
func retainVoiceHistory(client *Client, report netproto.VoiceTelemetry, epoch uint64, now time.Time) {
	if client.voiceHistoryEpoch != epoch || client.voiceHistorySession != report.SessionID {
		client.voiceHistory = nil
	}
	client.voiceHistoryEpoch = epoch
	client.voiceHistorySession = report.SessionID
	point := netproto.VoiceHistoryPoint{ObservedAt: now.UnixMilli(), TrackCount: len(report.Tracks), RTTMS: report.RTTMS, OutputState: report.OutputState}
	maximum := func(current, value *float64) *float64 {
		if value != nil && (current == nil || *value > *current) {
			copy := *value
			return &copy
		}
		return current
	}
	for _, track := range report.Tracks {
		point.LossPercent = maximum(point.LossPercent, track.LossPercent)
		point.DiscardPercent = maximum(point.DiscardPercent, track.DiscardPercent)
		point.ConcealmentPercent = maximum(point.ConcealmentPercent, track.NonSilentConcealmentPercent)
		point.BufferMS = maximum(point.BufferMS, track.BufferMS)
	}
	keep := client.voiceHistory[:0]
	for _, previous := range client.voiceHistory {
		if point.ObservedAt-previous.ObservedAt <= 300000 {
			keep = append(keep, previous)
		}
	}
	if len(keep) >= 60 {
		copy(keep, keep[len(keep)-59:])
		keep = keep[:59]
	}
	keep = append(keep, point)
	client.voiceHistory = keep
}

func correlateVoicePath(receiver netproto.VoiceReceiverDiagnostics, receiverAt int64, path netproto.VoiceMediaPath, publisher *netproto.VoiceClientReport) netproto.VoiceCorrelatedPath {
	result := netproto.VoiceCorrelatedPath{PublisherID: path.PublisherID, Slot: path.Slot, OutputSSRC: path.OutputSSRC, Ingress: path.Ingress}
	if receiver.SSRC != nil && *receiver.SSRC == path.OutputSSRC && path.Ingress != nil && receiverAt >= path.Ingress.StartedAt {
		result.Receiver = &receiver
	}
	if path.Ingress == nil || path.Ingress.Stale || publisher == nil || publisher.Stale || publisher.ReceivedAt < path.Ingress.StartedAt {
		return result
	}
	for _, sender := range publisher.Report.Senders {
		if sender.SSRC == path.Ingress.SSRC {
			result.Sender = &sender
			age := publisher.AgeMS
			result.SenderAgeMS = &age
			break
		}
	}
	return result
}

func (s *TCPServer) correlateVoiceDiagnostics(result *netproto.VoiceDiagnostics) {
	if result.Transport == nil {
		return
	}
	for _, path := range result.Transport.Paths {
		var receiver netproto.VoiceReceiverDiagnostics
		var receiverAt int64
		if result.ClientReport != nil && !result.ClientReport.Stale {
			receiverAt = result.ClientReport.ReceivedAt
			for _, track := range result.ClientReport.Report.Tracks {
				if track.SSRC != nil && *track.SSRC == path.OutputSSRC {
					receiver = track
					break
				}
			}
		}
		var publisher *netproto.VoiceClientReport
		s.mu.RLock()
		client := s.clients[path.PublisherID]
		s.mu.RUnlock()
		member, present := s.deps.State.GetClient(path.PublisherID)
		if client != nil && present {
			client.mu.RLock()
			if report := client.voiceTelemetry; report != nil && client.voiceTelemetryEpoch == member.ChannelEpoch && report.ChannelID == member.ChannelID {
				age := max(0, time.Since(client.voiceTelemetryAt).Milliseconds())
				publisher = &netproto.VoiceClientReport{ReceivedAt: client.voiceTelemetryAt.UnixMilli(), AgeMS: age, Stale: age > 15000, Report: *report}
			}
			client.mu.RUnlock()
		}
		correlated := correlateVoicePath(receiver, receiverAt, path, publisher)
		for _, feedback := range result.Transport.ReceiverReports {
			if feedback.SSRC == path.OutputSSRC && !feedback.Stale {
				correlated.Feedback = &feedback
				break
			}
		}
		result.Paths = append(result.Paths, correlated)
	}
}

// VoiceDiagnosticsHandler must be registered with health.HandleLocalGET. Remote
// owner reads use the control protocol's current policy lease instead. Reports
// live only on connected Client objects; disconnect removes the retained data.
func (s *TCPServer) VoiceDiagnosticsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		id, nickname := r.URL.Query().Get("client_id"), r.URL.Query().Get("nickname")
		if len(id) > 80 || len(nickname) > 128 || (id != "" && nickname != "") {
			http.Error(w, "invalid diagnostic filter", http.StatusBadRequest)
			return
		}
		s.mu.RLock()
		clients := make([]*Client, 0, len(s.clients))
		for _, client := range s.clients {
			clients = append(clients, client)
		}
		s.mu.RUnlock()
		sort.Slice(clients, func(i, j int) bool { return clients[i].ID < clients[j].ID })
		results := make([]*netproto.VoiceDiagnostics, 0)
		truncated := false
		for _, client := range clients {
			if id != "" && client.ID != id {
				continue
			}
			diagnostic := s.voiceDiagnostics(client)
			if diagnostic == nil || (nickname != "" && !strings.EqualFold(diagnostic.Nickname, nickname)) {
				continue
			}
			if len(results) == 256 {
				truncated = true
				break
			}
			results = append(results, diagnostic)
		}
		if (id != "" || nickname != "") && len(results) == 0 {
			http.Error(w, "client not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// Encoding can fail only after the caller disconnects: all numbers and
		// strings entered through bounded validated reports or server counters.
		_ = json.NewEncoder(w).Encode(struct {
			Clients   []*netproto.VoiceDiagnostics `json:"clients"`
			Truncated bool                         `json:"truncated"`
		}{results, truncated})
	})
}
