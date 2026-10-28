package server

import (
	"context"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"noxa/internal/webrtc"
)

const (
	eventStreamWatchStarted  = "stream_watch_started"
	eventStreamUploadChanged = "stream_upload_changed"
)

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
	if (msg.QualityMode != "" && msg.QualityMode != "source") || (msg.QualityMode != "" && msg.Action != "publish") {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid publication quality mode")
	}
	if (len(msg.Codecs) != 0 || msg.HEVCLevelID != 0) && msg.Action != "publish" && msg.Action != "watch" {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid video codec scope")
	}
	if msg.CodecRevision != 0 && msg.Action != "publish" {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid codec revision scope")
	}
	if reason := s.mediaE2EEStreamRejection(msg); reason != "" {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, reason)
	}
	s.mediaE2EECodecs(&msg)
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
	case "receiver_reset":
		if len(frame.Payload) > 4096 || msg.PublisherID == "" || (msg.Slot != webrtc.SlotCam && msg.Slot != webrtc.SlotScreen) ||
			msg.Generation == 0 || msg.Session == 0 || !msg.Active || msg.Revision == 0 || len(msg.JPEG) != 0 {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid receiver recovery scope")
		}
	case "diagnostics":
		if len(frame.Payload) > 4096 || msg.PublisherID == "" || (msg.Slot != webrtc.SlotCam && msg.Slot != webrtc.SlotScreen) ||
			msg.Generation == 0 || msg.Session == 0 || msg.Active || msg.Revision != 0 || len(msg.JPEG) != 0 {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid stream diagnostics scope")
		}
	default:
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid video stream action")
	}
	active := msg.Action != "publish" || msg.Active
	// Updating the bounded preview cache changes neither publisher permission nor
	// membership/watch consent. Its JPEG validation and TCP reply must not exclude
	// authorized packet writes; SetVideoPreview owns its cache/generation locks.
	sharedLease := msg.Action == "list" || msg.Action == "preview" || msg.Action == "diagnostics" || msg.Action == "preview_upload"
	lease := s.roleChannelControl
	if sharedLease {
		lease = s.roleChannelRead
	}
	return lease(ctx, client, capability, active, func(ctx context.Context, channelID int64) error {
		voice := s.deps.Voice
		// Real policy, membership, rules and visibility changes refresh this
		// snapshot under their mutation barriers. Polling must not acquire the
		// router's global watch writer merely to reinstall the same predicate.
		if !sharedLease {
			s.refreshRolePublishers(ctx.Value(roleLeaseKey{}).(roleLease).evaluator)
		}
		result := netproto.VideoStreamResult{Action: msg.Action, PublisherID: msg.PublisherID, Slot: msg.Slot, Generation: msg.Generation, Revision: msg.Revision, Active: msg.Active, Streams: []netproto.VideoStream{}}
		var err error
		var started bool
		result.Session = msg.Session
		switch msg.Action {
		case "publish":
			result.Generation, err = publishVideoWithMode(voice, client.ID, msg)
			if err == nil && msg.Active {
				for _, stream := range voice.VideoPublications(client.ID) {
					if stream.PublisherID == client.ID && stream.Slot == msg.Slot && stream.Generation == result.Generation {
						result.QualityMode, result.UploadActive, result.Codec, result.HEVCLevelID = stream.QualityMode, stream.UploadActive, stream.Codec, stream.HEVCLevelID
						result.CodecRevision = stream.CodecRevision
						break
					}
				}
			}
			if err == nil && msg.Slot == webrtc.SlotScreen {
				s.deps.State.SetSharing(client.ID, msg.Active)
			}
		case "watch":
			started, err = watchVideoWithCodecs(voice, client.ID, msg)
		case "receiver_reset":
			if recovery, ok := voice.(interface {
				ResetStreamReceiver(string, string, string, uint64, uint64, uint64) error
			}); ok {
				err = recovery.ResetStreamReceiver(client.ID, msg.PublisherID, msg.Slot, msg.Generation, msg.Session, msg.Revision)
			} else {
				return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "receiver recovery is unavailable")
			}
		case "preview_upload":
			err = voice.SetVideoPreview(client.ID, msg.Slot, msg.Generation, msg.JPEG)
		case "preview":
			result.JPEG, result.PreviewAt, err = voice.VideoPreview(client.ID, msg.PublisherID, msg.Slot, msg.Generation)
		case "diagnostics":
			result.Diagnostics, err = s.streamDiagnostics(client, channelID, msg)
		case "list":
			result.Session = voice.VideoWatchSession(client.ID)
			for _, stream := range voice.VideoPublications(client.ID) {
				result.Streams = append(result.Streams, netproto.VideoStream{PublisherID: stream.PublisherID, Slot: stream.Slot, Generation: stream.Generation, PreviewAt: stream.PreviewAt, WatchRevision: stream.WatchRevision, ViewerCount: stream.ViewerCount, QualityMode: stream.QualityMode, UploadActive: stream.UploadActive, Codec: stream.Codec, HEVCLevelID: stream.HEVCLevelID, CodecRevision: stream.CodecRevision})
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
		if msg.Action == "watch" && msg.PublisherID != client.ID {
			s.notifyStreamUploadChanged(msg.PublisherID, msg.Slot, msg.Generation)
		}
		return s.writeCommittedReply(client, netproto.MsgVideoStreamResult, result)
	})
}
