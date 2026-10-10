package server

import (
	"errors"

	"noxa/internal/netproto"
)

type sourceQualityPublisher interface {
	PublishVideoWithMode(publisher, slot string, generation uint64, active bool, mode string) (uint64, error)
}

func publishVideoWithMode(voice VoiceBackend, publisher string, request netproto.VideoStreamControl) (uint64, error) {
	if backend, ok := voice.(sourceQualityPublisher); ok {
		return backend.PublishVideoWithMode(publisher, request.Slot, request.Generation, request.Active, request.QualityMode)
	}
	if request.QualityMode != "" {
		return 0, errors.New("source quality publications unavailable")
	}
	return voice.PublishVideo(publisher, request.Slot, request.Generation, request.Active)
}

// This is an invalidation hint, never a delayed demand value. The publisher
// retrieves the current aggregate using its serialized catalog request.
func (s *TCPServer) notifyStreamUploadChanged(publisher, slot string, generation uint64) {
	if s.deps.Broadcast == nil {
		return
	}
	for _, stream := range s.deps.Voice.VideoPublications(publisher) {
		if stream.PublisherID != publisher || stream.Slot != slot || stream.Generation != generation || stream.QualityMode != "source" {
			continue
		}
		payload, err := eventEnvelope(eventStreamUploadChanged, streamWatchStartedEvent{publisher, slot, generation})
		if err == nil {
			// A stopped/slow publisher must not fail the viewer's watch operation.
			_ = s.deps.Broadcast.BroadcastToClient(publisher, payload)
		}
		return
	}
}
