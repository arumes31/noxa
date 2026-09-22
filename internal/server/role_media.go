package server

import (
	"noxa/internal/authorization"
	"noxa/internal/webrtc"
)

// guardRoleMedia pins authorization through the actual recipient write. The
// routing snapshot must still match both clients' membership; moving either
// client serializes against this bounded packet operation.
func (s *TCPServer) guardRoleMedia(delivery webrtc.MediaDelivery, write func() error) error {
	if s.deps == nil || s.deps.State == nil || s.deps.Authority == nil {
		return authorization.ErrAuthorizationUnavailable
	}
	return s.deps.Authority.TryWithPolicy(func(e *authorization.RoleEvaluator) error {
		sender, ok := s.clientByID(delivery.SenderID)
		if !ok || !sender.isAuthed() || sender.rulesBlocked() || delivery.ChannelID <= 0 {
			return nil
		}
		recipient, exists := s.clientByID(delivery.RecipientID)
		if !delivery.Tap && (!exists || !recipient.isAuthed() || recipient.rulesBlocked()) {
			return nil
		}
		first, second := sender, recipient
		if delivery.Tap {
			second = nil
		}
		if second != nil && first.ID > second.ID {
			first, second = second, first
		}
		if !first.roleActionMu.TryRLock() {
			return nil
		}
		defer first.roleActionMu.RUnlock()
		if second != nil && second != first {
			if !second.roleActionMu.TryRLock() {
				return nil
			}
			defer second.roleActionMu.RUnlock()
		}
		channelID, _, present := s.deps.State.ClientChannelState(sender.ID)
		if !present || channelID != delivery.ChannelID {
			return nil
		}
		audio := delivery.Slot == webrtc.SlotMic || delivery.Slot == webrtc.SlotScreenAudio
		if audio {
			source, ok := s.deps.State.GetClient(sender.ID)
			if !ok || source.ServerMuted {
				return nil
			}
			if !delivery.Tap {
				target, ok := s.deps.State.GetClient(recipient.ID)
				if !ok || target.ServerDeafened {
					return nil
				}
			}
		}
		capability := authorization.Speak
		switch delivery.Slot {
		case webrtc.SlotMic:
		case webrtc.SlotCam:
			capability = authorization.ShareCamera
		case webrtc.SlotScreen, webrtc.SlotScreenAudio:
			capability = authorization.ShareScreen
		default:
			return nil
		}
		if !e.Evaluate(sender.userID(), channelID, capability).Allowed {
			return nil
		}
		if delivery.Slot == webrtc.SlotScreenAudio && !e.Evaluate(sender.userID(), channelID, authorization.Speak).Allowed {
			return nil
		}
		if delivery.Tap {
			if _, revoked := s.roleRevokedRecordings.Load(channelID); revoked {
				return nil
			}
			// Recording entitlement is tied to the initiating account, never to
			// a synthetic tap name or the legacy admin flag.
			owner, ok := s.roleRecordingOwners.Load(channelID)
			if !ok || !e.Evaluate(owner.(int64), channelID, authorization.RecordChannel).Allowed {
				return nil
			}
		} else {
			source, ok := s.deps.State.GetClient(sender.ID)
			if !ok || (source.Status == "invisible" && sender.uniqueID() != recipient.uniqueID() && !e.Evaluate(recipient.userID(), 0, authorization.ViewConnectionInfo).Allowed) {
				return nil
			}
			receiverChannelID, _, present := s.deps.State.ClientChannelState(recipient.ID)
			if !present || receiverChannelID <= 0 || receiverChannelID != delivery.RecipientChannelID || !e.Evaluate(recipient.userID(), receiverChannelID, authorization.Connect).Allowed {
				return nil
			}
			if delivery.Whisper {
				if !e.Evaluate(sender.userID(), channelID, authorization.Whisper).Allowed || !e.Evaluate(sender.userID(), receiverChannelID, authorization.Whisper).Allowed || !e.Evaluate(recipient.userID(), channelID, authorization.ViewChannel).Allowed {
					return nil
				}
			} else if receiverChannelID != channelID {
				return nil
			}
		}
		return write()
	})
}
