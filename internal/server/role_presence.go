package server

import (
	"context"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

func (s *TCPServer) rolePoke(ctx context.Context, client *Client, msg netproto.Poke) error {
	return s.roleAction(ctx, client, 0, authorization.PokeMembers, func(ctx context.Context) error {
		s.roleMetadataMu.Lock()
		defer s.roleMetadataMu.Unlock()
		e := ctx.Value(roleLeaseKey{}).(roleLease).evaluator
		target, ok := s.clientByID(msg.ClientID)
		if !ok || !target.isAuthed() || !s.rolePokeTargetVisible(e, client, target.ID) {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeNotFound, "target client not found")
		}
		return s.sendPoke(ctx, client, target, msg)
	})
}

// Caller holds the policy and metadata barriers. A guessed connection ID must
// not make a hidden or invisible member reachable through a social action.
func (s *TCPServer) rolePokeTargetVisible(e *authorization.RoleEvaluator, sender *Client, targetID string) bool {
	if s.deps.State == nil {
		return false
	}
	target, ok := s.deps.State.GetClient(targetID)
	return ok && (targetID == sender.ID ||
		(e.Evaluate(sender.userID(), target.ChannelID, authorization.ViewChannel).Allowed &&
			(target.Status != "invisible" || e.Evaluate(sender.userID(), 0, authorization.ViewConnectionInfo).Allowed)))
}
