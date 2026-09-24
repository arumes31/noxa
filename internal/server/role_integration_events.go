package server

import (
	"context"
	"encoding/json"

	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/broadcast"
	"noxa/internal/eventbus"
)

// WithIntegrationEvent pins fresh admission, policy and live metadata through
// the transport callback. No raw transition payload, private kick reason or
// global bus sequence is forwarded to an integration.
func (s *TCPServer) WithIntegrationEvent(ctx context.Context, principal auth.IntegrationPrincipal, event eventbus.Event, deliver func(context.Context, broadcast.IntegrationEvent) error) error {
	if deliver == nil || len(event.Data) > 1<<20 {
		return authorization.ErrRoleInvalid
	}
	return s.withIntegrationPolicy(ctx, principal, func(ctx context.Context, e *authorization.RoleEvaluator) error {
		result, err := s.projectIntegrationEvent(ctx, e, principal.UserID(), principal.UniqueID(), event)
		if err != nil {
			return err
		}
		if result.Snapshot == nil && result.Speaking == nil {
			return ctx.Err()
		}
		return deliver(ctx, result)
	})
}

func (s *TCPServer) projectIntegrationEvent(ctx context.Context, e *authorization.RoleEvaluator, viewerID int64, viewerUID string, event eventbus.Event) (broadcast.IntegrationEvent, error) {
	var result broadcast.IntegrationEvent
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if s.deps.State == nil {
		return result, authorization.ErrAuthorizationUnavailable
	}
	switch event.Type {
	case eventUserJoined, eventUserLeft, eventUserMoved, eventChannelCreated, eventChannelDeleted, eventChannelUpdated, eventStatusChanged, eventMemberVoiceChanged, eventKicked:
		snapshot, err := s.integrationSnapshot(ctx, e, viewerID, viewerUID)
		return broadcast.IntegrationEvent{Snapshot: snapshot}, err
	case eventSpeakingChanged:
		var activity struct {
			ClientID  string `json:"client_id"`
			ChannelID *int64 `json:"channel_id"`
			Speaking  *bool  `json:"speaking"`
		}
		if err := json.Unmarshal(event.Data, &activity); err != nil || activity.ClientID == "" || activity.ChannelID == nil || *activity.ChannelID <= 0 || activity.Speaking == nil {
			return result, authorization.ErrRoleInvalid
		}
		member, ok := s.deps.State.GetClient(activity.ClientID)
		if !ok || member.ChannelID != *activity.ChannelID || member.IsSpeaking != *activity.Speaking ||
			!e.Evaluate(viewerID, member.ChannelID, authorization.ViewChannel).Allowed {
			return result, nil
		}
		if member.Status == "invisible" && member.UniqueID != viewerUID && !e.Evaluate(viewerID, 0, authorization.ViewConnectionInfo).Allowed {
			return result, nil
		}
		if *activity.Speaking && (member.ServerMuted || !e.Evaluate(member.UserID, member.ChannelID, authorization.Speak).Allowed) {
			return result, nil
		}
		result.Speaking = &broadcast.IntegrationSpeaking{ClientID: member.ClientID, ChannelID: member.ChannelID, Speaking: member.IsSpeaking}
	default:
		// New bus types require an explicit disclosure policy. In particular,
		// session-addressed chat/typing/whisper/poke are never a public feed.
	}
	return result, nil
}

func (s *TCPServer) integrationSnapshot(ctx context.Context, e *authorization.RoleEvaluator, viewerID int64, viewerUID string) (*broadcast.TreeSnapshot, error) {
	if s.deps.State == nil || s.deps.State.ChannelCount()+s.deps.State.ClientCount() > maxIntegrationSnapshotItems {
		return nil, authorization.ErrAuthorizationUnavailable
	}
	snapshot, err := buildRoleSnapshotContext(ctx, s.deps.State, e, viewerID, viewerUID)
	if err != nil {
		return nil, err
	}
	filterActivity := func(members []*broadcast.ClientInfo) error {
		for _, member := range members {
			if err := ctx.Err(); err != nil {
				return err
			}
			member.IsSpeaking = member.IsSpeaking && member.ChannelID > 0 && !member.ServerMuted &&
				e.Evaluate(member.UserID, member.ChannelID, authorization.Speak).Allowed
		}
		return nil
	}
	if err := filterActivity(snapshot.UnassignedClients); err != nil {
		return nil, err
	}
	stack := append([]*broadcast.ChannelNode(nil), snapshot.RootChannels...)
	for len(stack) != 0 {
		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if err := filterActivity(node.Clients); err != nil {
			return nil, err
		}
		stack = append(stack, node.Children...)
	}
	return snapshot, ctx.Err()
}
