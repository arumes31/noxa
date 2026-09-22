package server

import (
	"context"
	"errors"

	"noxa/internal/authorization"
	"noxa/internal/recorder"
	"noxa/internal/state"
)

// reconcileRoleMedia runs under Authority's exclusive gate, before chat key
// rotation and visible-state refresh. Packet delivery cannot cross this hook.
func (s *TCPServer) reconcileRoleMedia(ctx context.Context, _ *authorization.RoleEvaluator, after *authorization.RoleEvaluator) error {
	if s.deps == nil || s.deps.State == nil {
		return authorization.ErrAuthorizationUnavailable
	}
	s.roleMetadataMu.Lock()
	defer s.roleMetadataMu.Unlock()
	s.refreshRolePublishers(after)
	for _, member := range s.deps.State.ListClients() {
		if err := ctx.Err(); err != nil {
			return err
		}
		client, ok := s.clientByID(member.ClientID)
		if !ok || member.ChannelID == 0 {
			continue
		}
		if !after.Evaluate(client.userID(), member.ChannelID, authorization.Connect).Allowed {
			if s.deps.Channels != nil {
				if _, err := s.deps.Channels.LeaveClient(client.ID); err != nil && !errors.Is(err, state.ErrNotInChannel) && !errors.Is(err, state.ErrClientNotFound) {
					return err
				}
			} else if err := s.deps.State.LeaveChannel(client.ID); err != nil && !errors.Is(err, state.ErrNotInChannel) && !errors.Is(err, state.ErrClientNotFound) {
				return err
			}
			if s.deps.Voice != nil {
				s.deps.Voice.LeaveChannel(client.ID, member.ChannelID)
			}
		}
		if !after.Evaluate(client.userID(), member.ChannelID, authorization.Speak).Allowed {
			s.deps.State.SetSpeaking(client.ID, false)
		}
		if !after.Evaluate(client.userID(), member.ChannelID, authorization.PrioritySpeaker).Allowed {
			s.deps.State.SetPrioritySpeaker(client.ID, false)
		}
		if !after.Evaluate(client.userID(), member.ChannelID, authorization.ShareScreen).Allowed {
			s.deps.State.SetSharing(client.ID, false)
		}
		if s.deps.Voice != nil && !after.Evaluate(client.userID(), member.ChannelID, authorization.Whisper).Allowed {
			s.deps.Voice.SetWhisper(client.ID, nil, nil, false)
		}
	}
	var stopErr error
	s.roleRecordingOwners.Range(func(key, value any) bool {
		if err := ctx.Err(); err != nil {
			stopErr = err
			return false
		}
		channelID, ownerID := key.(int64), value.(int64)
		_, revoked := s.roleRevokedRecordings.Load(channelID)
		if !revoked && after.Evaluate(ownerID, channelID, authorization.RecordChannel).Allowed {
			return true
		}
		if s.deps.Recorder == nil {
			stopErr = authorization.ErrAuthorizationUnavailable
			return false
		}
		if err := s.deps.Recorder.Stop(channelID); err != nil && !errors.Is(err, recorder.ErrNotRecording) {
			stopErr = err
			return false
		}
		s.roleRecordingOwners.Delete(channelID)
		s.roleRevokedRecordings.Delete(channelID)
		return true
	})
	return stopErr
}
