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
	}
	client.mu.RUnlock()
	if backend, ok := s.deps.Voice.(voiceDiagnosticsBackend); ok {
		result.Transport = backend.VoiceDiagnostics(client.ID)
	}
	return result
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
