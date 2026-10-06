package server

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"noxa/internal/netproto"
	"noxa/internal/state"
)

type videoOperatorDiagnosticsBackend interface {
	VideoOperatorDiagnostics() *netproto.VideoOperatorDiagnostics
}

// StreamOperatorDiagnosticsHandler must be registered with health.HandleLocalGET.
// It snapshots bounded in-memory media state only; the viewer control protocol
// retains its separate publication/session authorization checks.
// Filters select from the bounded global snapshot. A truncated result, including
// an empty filtered result, cannot establish that another publication is absent.
func (s *TCPServer) StreamOperatorDiagnosticsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		id, nickname, valid := streamOperatorFilter(r.URL.RawQuery)
		if !valid {
			http.Error(w, "invalid diagnostic filter", http.StatusBadRequest)
			return
		}
		if s.deps == nil || s.deps.State == nil {
			http.Error(w, "stream diagnostics unavailable", http.StatusServiceUnavailable)
			return
		}
		backend, ok := s.deps.Voice.(videoOperatorDiagnosticsBackend)
		if !ok {
			http.Error(w, "stream diagnostics unavailable", http.StatusServiceUnavailable)
			return
		}
		snapshot := backend.VideoOperatorDiagnostics()
		if snapshot == nil {
			http.Error(w, "stream diagnostics unavailable", http.StatusServiceUnavailable)
			return
		}
		result := s.enrichStreamOperatorSnapshot(snapshot, id, nickname, time.Now())
		encoded, err := json.Marshal(result)
		if err != nil || len(encoded) > 2*1024*1024 {
			http.Error(w, "stream diagnostics unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(encoded)
	})
}

func streamOperatorFilter(raw string) (id, nickname string, valid bool) {
	if len(raw) > 512 {
		return "", "", false
	}
	query, err := url.ParseQuery(raw)
	if err != nil {
		return "", "", false
	}
	for key, values := range query {
		if (key != "publisher_id" && key != "nickname") || len(values) != 1 {
			return "", "", false
		}
	}
	id, nickname = query.Get("publisher_id"), query.Get("nickname")
	valid = len(id) <= 80 && len(nickname) <= 128 && (id == "" || nickname == "") && !strings.ContainsAny(id+nickname, "\x00\r\n\t")
	return id, nickname, valid
}

func (s *TCPServer) streamOperatorMember(id string, channelID int64) (*Client, *state.Client) {
	s.mu.RLock()
	client := s.clients[id]
	s.mu.RUnlock()
	if client == nil || !client.isAuthed() {
		return nil, nil
	}
	member, present := s.deps.State.GetClient(id)
	if !present || member.ChannelID != channelID {
		return nil, nil
	}
	return client, member
}

func (s *TCPServer) enrichStreamOperatorSnapshot(snapshot *netproto.VideoOperatorDiagnostics, id, nickname string, now time.Time) *netproto.VideoOperatorDiagnostics {
	result := &netproto.VideoOperatorDiagnostics{SampledAt: snapshot.SampledAt, Truncated: snapshot.Truncated, Publications: []netproto.VideoOperatorPublication{}}
	publications := snapshot.Publications
	if len(publications) > netproto.MaxVideoOperatorPublications {
		publications = publications[:netproto.MaxVideoOperatorPublications]
		result.Truncated = true
	}
	for _, publication := range publications {
		publisher, member := s.streamOperatorMember(publication.PublisherID, publication.ChannelID)
		if publisher == nil || (id != "" && publication.PublisherID != id) || (nickname != "" && !strings.EqualFold(member.Nickname, nickname)) {
			continue
		}
		publication.PublisherNickname = member.Nickname
		publication.SenderReport = nil
		publication.Layers = publication.Layers[:min(len(publication.Layers), 4)]
		publisher.mu.RLock()
		publication.PublisherVersion = publisher.clientVersion
		if report := publisher.voiceTelemetry; report != nil && report.ChannelID == publication.ChannelID && publisher.voiceTelemetryEpoch == member.ChannelEpoch {
			scoped := netproto.VideoStreamDiagnostics{PublisherID: publication.PublisherID, Slot: publication.Slot, Generation: publication.Generation, Layers: publication.Layers}
			attachVideoSenderReport(&scoped, report.VideoSenders, publisher.voiceTelemetryAt, now)
			publication.SenderReport = scoped.SenderReport
		}
		publisher.mu.RUnlock()
		viewers := publication.Viewers
		if len(viewers) > netproto.MaxVideoOperatorViewers {
			viewers = viewers[:netproto.MaxVideoOperatorViewers]
			publication.ViewersTruncated = true
		}
		publication.Viewers = make([]netproto.VideoOperatorViewer, 0, len(viewers))
		for _, viewer := range viewers {
			client, current := s.streamOperatorMember(viewer.ClientID, publication.ChannelID)
			if client == nil {
				continue
			}
			viewer.Nickname = current.Nickname
			client.mu.RLock()
			viewer.ClientVersion = client.clientVersion
			client.mu.RUnlock()
			publication.Viewers = append(publication.Viewers, viewer)
		}
		result.Publications = append(result.Publications, publication)
	}
	return result
}
