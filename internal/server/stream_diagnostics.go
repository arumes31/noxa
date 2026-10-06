package server

import (
	"errors"
	"time"

	"noxa/internal/netproto"
)

type streamDiagnosticsBackend interface {
	StreamDiagnostics(subscriber, publisher, slot string, generation, session uint64) (*netproto.VideoStreamDiagnostics, error)
}

// Called under the same policy and membership lease as watch requests. The
// backend must additionally bind its snapshot to the exact publication/session.
func (s *TCPServer) streamDiagnostics(client *Client, channelID int64, request netproto.VideoStreamControl) (*netproto.VideoStreamDiagnostics, error) {
	backend, ok := s.deps.Voice.(streamDiagnosticsBackend)
	if !ok {
		return nil, errors.New("stream diagnostics unavailable")
	}
	s.mu.RLock()
	publisher := s.clients[request.PublisherID]
	s.mu.RUnlock()
	member, present := s.deps.State.GetClient(request.PublisherID)
	if publisher == nil || !publisher.isAuthed() || !present || member.ChannelID != channelID {
		return nil, errors.New("stream unavailable")
	}
	result, err := backend.StreamDiagnostics(client.ID, request.PublisherID, request.Slot, request.Generation, request.Session)
	if err != nil {
		return nil, err
	}
	if result == nil || result.PublisherID != request.PublisherID || result.Slot != request.Slot || result.Generation != request.Generation || result.Session != request.Session {
		return nil, errors.New("stream diagnostics scope changed")
	}
	publisher.mu.RLock()
	defer publisher.mu.RUnlock()
	if report := publisher.voiceTelemetry; report != nil && report.ChannelID == channelID && publisher.voiceTelemetryEpoch == member.ChannelEpoch {
		attachVideoSenderReport(result, report.VideoSenders, publisher.voiceTelemetryAt, time.Now())
	}
	return result, nil
}

func attachVideoSenderReport(result *netproto.VideoStreamDiagnostics, rows []netproto.VideoSenderDiagnostics, received, now time.Time) {
	age := max(0, now.Sub(received).Milliseconds())
	if received.IsZero() || age > 15000 {
		return
	}
	report := &netproto.VideoSenderReport{ReceivedAt: received.UnixMilli(), AgeMS: age}
	for _, row := range rows {
		if row.Generation != result.Generation || row.Slot != result.Slot {
			continue
		}
		for _, layer := range result.Layers {
			if row.SSRC == layer.SSRC && row.RID == layer.RID && report.ReceivedAt >= layer.StartedAt {
				report.Rows = append(report.Rows, row)
				break
			}
		}
	}
	if len(report.Rows) > 0 {
		result.SenderReport = report
	}
}
