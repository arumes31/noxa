package server

import (
	"context"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"noxa/internal/webrtc"
)

const eventStreamWatchStarted = "stream_watch_started"

// No viewer identity is disclosed, including for invisible members.
type streamWatchStartedEvent struct {
	PublisherID string `json:"publisher_id"`
	Slot        string `json:"slot"`
	Generation  uint64 `json:"generation,string"`
}

func (s *TCPServer) handleVideoStreamControl(ctx context.Context, client *Client, frame *netproto.Frame) error {
	var msg netproto.VideoStreamControl
	if err := netproto.Decode(frame, &msg); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "malformed video stream control")
	}
	if s.deps == nil || s.deps.Voice == nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "voice backend unavailable")
	}
	capability := authorization.Connect
	switch msg.Action {
	case "publish", "preview_upload":
		if msg.PublisherID != "" && msg.PublisherID != client.ID {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid publisher")
		}
		msg.PublisherID = client.ID
		switch msg.Slot {
		case webrtc.SlotCam:
			capability = authorization.ShareCamera
		case webrtc.SlotScreen:
			capability = authorization.ShareScreen
		default:
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid publication slot")
		}
	case "watch", "preview", "list":
	default:
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid video stream action")
	}
	active := msg.Action != "publish" || msg.Active
	return s.roleChannelControl(ctx, client, capability, active, func(ctx context.Context, _ int64) error {
		voice := s.deps.Voice
		s.refreshRolePublishers(ctx.Value(roleLeaseKey{}).(roleLease).evaluator)
		result := netproto.VideoStreamResult{Action: msg.Action, PublisherID: msg.PublisherID, Slot: msg.Slot, Generation: msg.Generation, Revision: msg.Revision, Active: msg.Active, Streams: []netproto.VideoStream{}}
		var err error
		var started bool
		result.Session = msg.Session
		switch msg.Action {
		case "publish":
			result.Generation, err = voice.PublishVideo(client.ID, msg.Slot, msg.Generation, msg.Active)
			if err == nil && msg.Slot == webrtc.SlotScreen {
				s.deps.State.SetSharing(client.ID, msg.Active)
			}
		case "watch":
			started, err = voice.WatchVideo(client.ID, msg.PublisherID, msg.Slot, msg.Generation, msg.Revision, msg.Session, msg.Active)
		case "preview_upload":
			err = voice.SetVideoPreview(client.ID, msg.Slot, msg.Generation, msg.JPEG)
		case "preview":
			result.JPEG, result.PreviewAt, err = voice.VideoPreview(client.ID, msg.PublisherID, msg.Slot, msg.Generation)
		case "list":
			result.Session = voice.VideoWatchSession(client.ID)
			for _, stream := range voice.VideoPublications(client.ID) {
				result.Streams = append(result.Streams, netproto.VideoStream{PublisherID: stream.PublisherID, Slot: stream.Slot, Generation: stream.Generation, PreviewAt: stream.PreviewAt, WatchRevision: stream.WatchRevision})
			}
		}
		if err != nil {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, err.Error())
		}
		if started && msg.PublisherID != client.ID && s.deps.Broadcast != nil {
			payload, encodeErr := eventEnvelope(eventStreamWatchStarted, streamWatchStartedEvent{msg.PublisherID, msg.Slot, msg.Generation})
			if encodeErr != nil {
				return encodeErr
			}
			// A disconnected or slow publisher must not fail the viewer's watch.
			_ = s.deps.Broadcast.BroadcastToClient(msg.PublisherID, payload)
		}
		return s.writeCommittedReply(client, netproto.MsgVideoStreamResult, result)
	})
}
