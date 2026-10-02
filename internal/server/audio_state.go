package server

import (
	"context"

	"noxa/internal/netproto"
)

const eventAudioStateChanged = "audio_state_changed"

func (s *TCPServer) handleAudioStateSet(ctx context.Context, client *Client, frame *netproto.Frame) error {
	var msg netproto.AudioStateSet
	if err := netproto.Decode(frame, &msg); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "malformed audio state")
	}
	if s.deps == nil || s.deps.State == nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "state backend unavailable")
	}
	return s.rolePolicyRead(ctx, client, func(ctx context.Context) error {
		s.roleMetadataMu.Lock()
		defer s.roleMetadataMu.Unlock()
		client.roleActionMu.Lock()
		defer client.roleActionMu.Unlock()
		muted := msg.Muted || msg.Deafened
		if !s.deps.State.SetAudioState(client.ID, muted, msg.Deafened) {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "session unavailable")
		}
		saved := netproto.AudioStateSaved{ClientID: client.ID, Muted: muted, Deafened: msg.Deafened}
		s.broadcastEvent(eventAudioStateChanged, saved)
		return s.writeCommittedReply(client, netproto.MsgAudioStateSaved, saved)
	})
}
