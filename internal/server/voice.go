// voice.go implements the voice-pipeline handlers of the TCP control server:
// WebRTC signaling (SDP offer/answer, trickle ICE), whisper configuration,
// and 3D position relays. All handlers require authentication (enforced by
// dispatch). The heavy lifting lives behind the VoiceBackend interface, so
// this file contains no Pion types.
package server

import (
	"context"
	"errors"
	"time"

	"go.uber.org/zap"

	"noxa/internal/authorization"
	"noxa/internal/channels"
	"noxa/internal/netproto"
	"noxa/internal/recorder"
	"noxa/internal/webrtc"
)

// Broadcast event types for the voice pipeline.
const (
	eventSpeakingChanged        = "speaking_changed"
	eventPosition               = "position"
	eventPrioritySpeakerChanged = "priority_speaker_changed"
	// eventWhisper is delivered ONLY to the targets of an active whisper. It
	// cannot ride the broadcast speaking event: that one goes to everybody, so
	// it can say THAT someone whispers but never that they whispered to YOU —
	// which is exactly what the receiving client needs to sound the whisper
	// cue, flash the taskbar (32) and remember who to whisper back to (33).
	eventWhisper = "whisper"
)

// mediaPermissionTimeout bounds backend permission work triggered by voice
// callbacks, which have no request context to inherit.
const mediaPermissionTimeout = time.Second

// whisperTargeter is the optional VoiceBackend capability that reports which
// clients a speaker's active whisper currently reaches. webrtc.Voice
// implements it; a backend that does not simply produces no whisper events.
type whisperTargeter interface {
	WhisperTargets(clientID string) []string
}

// trackSlotDeclarer is the optional VoiceBackend capability that accepts the
// media slot of each track in a client's offer (70). webrtc.Voice implements
// it; a backend that does not routes every track to its kind's default slot.
type trackSlotDeclarer interface {
	DeclareTrackSlots(clientID string, slots map[string]string)
}

// The production backend must satisfy the optional capability: a signature
// drift would make the type assertion in handleWebRTCOffer miss silently and
// every screen share would publish its audio into the microphone slot.
var _ trackSlotDeclarer = (*webrtc.Voice)(nil)

// whisperEvent is the payload of whisper events, emitted to every target of an
// active whisper when the whisperer's VAD state toggles. FromUniqueID is the
// handle the whisper-reply hotkey whispers back to (33); Speaking mirrors the
// speaking event so the receiver can clear the indicator again.
type whisperEvent struct {
	ChannelID    int64  `json:"channel_id,omitempty"`
	FromClientID string `json:"from_client_id"`
	FromUniqueID string `json:"from_unique_id"`
	FromNickname string `json:"from_nickname"`
	Speaking     bool   `json:"speaking"`
}

// prioritySpeakerEvent is the payload of priority_speaker_changed events,
// emitted when a client toggles its priority-speaker flag.
type prioritySpeakerEvent struct {
	ClientID  string `json:"client_id"`
	ChannelID int64  `json:"channel_id"`
	Active    bool   `json:"active"`
}

// speakingEvent is the payload of speaking_changed events, emitted when a
// client's VAD speaking state toggles.
type speakingEvent struct {
	ClientID  string `json:"client_id"`
	ChannelID int64  `json:"channel_id,omitempty"`
	Speaking  bool   `json:"speaking"`
	// Whisper marks the transmission as a whisper (32). It names no targets —
	// this event is broadcast, and who is being whispered to is only disclosed
	// to those targets, via eventWhisper.
	Whisper bool `json:"whisper,omitempty"`
}

// positionEvent is the payload of position events, relaying a client's 3D
// position to the other members of its channel.
type positionEvent struct {
	Context   string  `json:"context"`
	ChannelID int64   `json:"channel_id"`
	ClientID  string  `json:"client_id"`
	X         float64 `json:"x"`
	Y         float64 `json:"y"`
	Z         float64 `json:"z"`
}

// handleWebRTCOffer establishes a WebRTC session for the client: it forwards
// the SDP offer to the voice backend and replies with the SDP answer. Local
// ICE candidates are pushed to the client asynchronously as ICECandidate
// messages.
func (s *TCPServer) handleWebRTCOffer(ctx context.Context, client *Client, f *netproto.Frame) error {
	var msg netproto.WebRTCOffer
	if err := netproto.Decode(f, &msg); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "malformed webrtc_offer: "+err.Error())
	}
	if s.deps == nil || s.deps.Voice == nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "voice backend unavailable")
	}
	return s.roleChannelControl(ctx, client, authorization.Connect, true, func(ctx context.Context, _ int64) error {
		s.refreshRolePublishers(ctx.Value(roleLeaseKey{}).(roleLease).evaluator)
		return s.applyWebRTCOffer(ctx, client, msg)
	})
}

func (s *TCPServer) applyWebRTCOffer(ctx context.Context, client *Client, msg netproto.WebRTCOffer) error {
	// The slot declaration has to land BEFORE the offer is applied: the
	// backend builds this client's output tracks while answering, and the
	// other members' extra output tracks are added from here too (70).
	if d, ok := s.deps.Voice.(trackSlotDeclarer); ok {
		slots := make(map[string]string, len(msg.Tracks))
		for _, t := range msg.Tracks {
			if t.TrackID != "" {
				slots[t.TrackID] = t.Slot
			}
		}
		d.DeclareTrackSlots(client.ID, slots)
	}

	answer, err := s.deps.Voice.HandleOffer(client.ID, msg.SDP,
		func(candidate, sdpMid string, mlineIndex uint16) {
			// Push server-side candidates to the client as they are gathered.
			// Write errors (e.g. disconnect) are logged by writeMessage.
			_ = s.writeVoiceSignal(client, netproto.MsgICECandidate, netproto.ICECandidate{
				Candidate:     candidate,
				SDPMid:        sdpMid,
				SDPMLineIndex: mlineIndex,
			})
		})
	// A peer rebuild retires publications; ordinary renegotiation preserves them.
	// Reconcile even after a failed rebuild, while channel-control locks are held.
	sharing := false
	for _, stream := range s.deps.Voice.VideoPublications(client.ID) {
		if stream.PublisherID == client.ID && stream.Slot == webrtc.SlotScreen {
			sharing = true
			break
		}
	}
	s.deps.State.SetSharing(client.ID, sharing)
	if err != nil {
		if errors.Is(err, webrtc.ErrOfferCollision) {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, webrtc.ErrOfferCollision.Error())
		}
		s.logger.Warn("webrtc offer failed",
			zap.String("client_id", client.ID),
			zap.Error(err),
		)
		if errors.Is(err, webrtc.ErrPeerReset) {
			// End this control session so the desktop reconnects with a fresh
			// peer instead of retaining a permanently stuck DTLS transport.
			return err
		}
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "webrtc offer failed")
	}
	return s.writeMessage(client, netproto.MsgWebRTCAnswer, netproto.WebRTCAnswer{SDP: answer})
}

// handleWebRTCAnswer applies an SDP answer from the client (used when the
// server initiated renegotiation).
func (s *TCPServer) handleWebRTCAnswer(ctx context.Context, client *Client, f *netproto.Frame) error {
	var msg netproto.WebRTCAnswer
	if err := netproto.Decode(f, &msg); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "malformed webrtc_answer: "+err.Error())
	}
	if s.deps == nil || s.deps.Voice == nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "voice backend unavailable")
	}
	return s.roleChannelControl(ctx, client, authorization.Connect, true, func(ctx context.Context, _ int64) error {
		return s.applyWebRTCAnswer(ctx, client, msg)
	})
}

func (s *TCPServer) applyWebRTCAnswer(ctx context.Context, client *Client, msg netproto.WebRTCAnswer) error {
	if err := s.deps.Voice.HandleAnswer(client.ID, msg.SDP); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeNotFound, "webrtc answer failed: no active session")
	}
	return nil
}

// handleICECandidate applies a trickle ICE candidate from the client.
func (s *TCPServer) handleICECandidate(ctx context.Context, client *Client, f *netproto.Frame) error {
	var msg netproto.ICECandidate
	if err := netproto.Decode(f, &msg); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "malformed ice_candidate: "+err.Error())
	}
	if s.deps == nil || s.deps.Voice == nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "voice backend unavailable")
	}
	return s.roleChannelControl(ctx, client, authorization.Connect, true, func(context.Context, int64) error {
		return s.applyICECandidate(client, msg)
	})
}

func (s *TCPServer) applyICECandidate(client *Client, msg netproto.ICECandidate) error {
	if err := s.deps.Voice.AddICECandidate(client.ID, msg.Candidate, msg.SDPMid, msg.SDPMLineIndex); err != nil {
		s.logger.Debug("ice candidate rejected",
			zap.String("client_id", client.ID),
			zap.Error(err),
		)
	}
	return nil
}

// handleWhisperSet configures the client's whisper list and mode. Target
// unique IDs are resolved to online connections at set time; offline targets
// are dropped. Gated by i_client_whisper_power (unset = allowed, negated =
// denied).
func (s *TCPServer) handleWhisperSet(ctx context.Context, client *Client, f *netproto.Frame) error {
	var msg netproto.WhisperSet
	if err := netproto.Decode(f, &msg); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "malformed whisper_set: "+err.Error())
	}
	if s.deps == nil || s.deps.Voice == nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "voice backend unavailable")
	}
	return s.roleWhisperSet(ctx, client, msg)
}

// handlePositionUpdate relays the client's 3D position to the other members
// of its channel. Positions are low-rate metadata, so they travel over the
// control channel (broadcaster events) rather than a WebRTC DataChannel.
func (s *TCPServer) handlePositionUpdate(ctx context.Context, client *Client, f *netproto.Frame) error {
	var msg netproto.PositionUpdate
	if err := netproto.Decode(f, &msg); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "malformed position_update: "+err.Error())
	}
	if s.deps == nil || s.deps.State == nil || s.deps.Broadcast == nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "state backend unavailable")
	}
	return s.rolePositionUpdate(ctx, client, msg)
}

// handlePrioritySpeaker toggles the calling client's priority-speaker flag
// (TS3-style channel commander). Gated by b_client_priority_speaker (deny on
// unset; server admins bypass). The flag is broadcast so subscribers can duck
// other publishers while a priority speaker talks.
func (s *TCPServer) handlePrioritySpeaker(ctx context.Context, client *Client, f *netproto.Frame) error {
	var msg netproto.PrioritySpeaker
	if err := netproto.Decode(f, &msg); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "malformed priority_speaker: "+err.Error())
	}
	if s.deps == nil || s.deps.State == nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "state backend unavailable")
	}
	return s.rolePrioritySpeaker(ctx, client, msg)
}

// handleVideoQuality sets the client's preferred simulcast layer for the
// video it receives.
func (s *TCPServer) handleVideoQuality(ctx context.Context, client *Client, f *netproto.Frame) error {
	var msg netproto.VideoQuality
	if err := netproto.Decode(f, &msg); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "malformed video_quality: "+err.Error())
	}
	if s.deps == nil || s.deps.Voice == nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "voice backend unavailable")
	}
	return s.roleChannelControl(ctx, client, authorization.Connect, true, func(ctx context.Context, _ int64) error {
		return s.applyVideoQuality(ctx, client, msg)
	})
}

func (s *TCPServer) applyVideoQuality(ctx context.Context, client *Client, msg netproto.VideoQuality) error {
	if err := s.deps.Voice.SetVideoQuality(client.ID, msg.Quality); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, err.Error())
	}
	return s.acknowledgeMediaControl(client, msg.AckRequested, netproto.MediaControlSaved{Operation: netproto.MsgVideoQuality, Quality: msg.Quality})
}

// handleRecordingControl starts or stops a server-side recording of a
// channel, gated by the current RecordChannel capability.
func (s *TCPServer) handleRecordingControl(ctx context.Context, client *Client, f *netproto.Frame) error {
	var msg netproto.RecordingControl
	if err := netproto.Decode(f, &msg); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "malformed recording_control: "+err.Error())
	}
	if s.deps == nil || s.deps.Recorder == nil || s.deps.Voice == nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "recording backend unavailable")
	}
	return s.roleAction(ctx, client, msg.ChannelID, authorization.RecordChannel, func(ctx context.Context) error {
		if msg.ChannelID <= 0 || client.userID() <= 0 || client.rulesBlocked() {
			return authorization.ErrRoleForbidden
		}
		s.roleRecordingMu.Lock()
		defer s.roleRecordingMu.Unlock()
		return s.recordingControlAllowed(ctx, client, msg)
	})
}

func (s *TCPServer) recordingControlAllowed(ctx context.Context, client *Client, msg netproto.RecordingControl) error {
	switch msg.Action {
	case "start":
		if s.deps.Channels == nil {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "channel backend unavailable")
		}
		var session *recorder.Session
		err := s.deps.Channels.WithChannelLifecycle(msg.ChannelID, func() error {
			var startErr error
			session, startErr = s.deps.Recorder.Start(ctx, msg.ChannelID, s.deps.Voice)
			return startErr
		})
		if err != nil {
			if errors.Is(err, channels.ErrChannelNotFound) {
				return s.sendErrorFor(client, requestOrigin(ctx), errCodeNotFound, "channel not found")
			}
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "recording start failed: "+err.Error())
		}
		if session == nil {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "recording start failed: empty session")
		}
		s.roleRecordingOwners.Store(msg.ChannelID, client.userID())
		s.roleRevokedRecordings.Delete(msg.ChannelID)
		s.logger.Info("recording started",
			zap.String("client_id", client.ID),
			zap.Int64("channel_id", msg.ChannelID),
			zap.String("output", session.FilePath),
		)
	case "stop":
		if err := s.deps.Recorder.Stop(msg.ChannelID); err != nil {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeNotFound, "recording stop failed: "+err.Error())
		}
		s.roleRecordingOwners.Delete(msg.ChannelID)
		s.roleRevokedRecordings.Delete(msg.ChannelID)
		s.logger.Info("recording stopped",
			zap.String("client_id", client.ID),
			zap.Int64("channel_id", msg.ChannelID),
		)
	default:
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "unknown recording action: "+msg.Action)
	}
	return nil
}

// --- voice callbacks (installed on the voice backend at server construction) -

// canTalk evaluates Speak in the client's current channel and current mute state.
func (s *TCPServer) canTalk(clientID string) bool {
	client, ok := s.clientByID(clientID)
	if !ok {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), mediaPermissionTimeout)
	defer cancel()
	member, present := s.deps.State.GetClient(clientID)
	return present && member.ChannelID > 0 && !member.ServerMuted && !client.rulesBlocked() && s.roleAllowed(ctx, client, member.ChannelID, authorization.Speak)
}

// canPublishVideo checks the client's current video capabilities and channel.
func (s *TCPServer) canPublishVideo(clientID string) bool {
	client, ok := s.clientByID(clientID)
	if !ok {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), mediaPermissionTimeout)
	defer cancel()
	channelID, _, present := s.deps.State.ClientChannelState(clientID)
	return present && channelID > 0 && !client.rulesBlocked() &&
		(s.roleAllowed(ctx, client, channelID, authorization.ShareCamera) || s.roleAllowed(ctx, client, channelID, authorization.ShareScreen))
}

// onSpeakingChanged is the router's speaking-state callback: it updates the
// state manager and announces the transition to all clients. The channel is
// read back from the synchronized state manager after the speaking update, so
// it is the channel current at publication time.
//
// When the speaker is whispering, each target additionally gets a directed
// whisper event naming the whisperer (32/33).
func (s *TCPServer) onSpeakingChanged(clientID string, speaking bool) {
	if s.deps == nil || s.deps.State == nil {
		return
	}
	s.deps.State.SetSpeaking(clientID, speaking)
	stateClient, ok := s.deps.State.GetClient(clientID)
	if !ok {
		return
	}
	speaking = stateClient.IsSpeaking

	var targets []string
	if wt, ok := s.deps.Voice.(whisperTargeter); ok {
		targets = wt.WhisperTargets(clientID)
	}
	s.broadcastEvent(eventSpeakingChanged, speakingEvent{
		ClientID:  clientID,
		ChannelID: stateClient.ChannelID,
		Speaking:  speaking,
		Whisper:   len(targets) > 0,
	})
	if len(targets) == 0 || s.deps.Broadcast == nil {
		return
	}

	ev := whisperEvent{FromClientID: clientID, ChannelID: stateClient.ChannelID, Speaking: speaking}
	if c, ok := s.clientByID(clientID); ok {
		ev.FromUniqueID = c.uniqueID()
		ev.FromNickname = c.Username
	}
	payload, err := eventEnvelope(eventWhisper, ev)
	if err != nil {
		s.logger.Error("encoding whisper event failed", zap.Error(err))
		return
	}
	for _, target := range targets {
		if target == clientID {
			continue
		}
		if err := s.deps.Broadcast.BroadcastToClient(target, payload); err != nil {
			s.logger.Debug("whisper event undeliverable",
				zap.String("client_id", target),
				zap.Error(err),
			)
		}
	}
}
