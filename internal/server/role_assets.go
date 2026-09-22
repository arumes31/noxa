package server

import (
	"context"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

// Avatar lookup follows live member visibility; knowledge of an offline or
// hidden member's unique ID alone does not authorize their profile asset.
func (s *TCPServer) roleAvatarGet(ctx context.Context, client *Client, msg netproto.AvatarGet) error {
	return s.rolePolicyRead(ctx, client, func(ctx context.Context) error {
		e := ctx.Value(roleLeaseKey{}).(roleLease).evaluator
		s.roleMetadataMu.Lock()
		defer s.roleMetadataMu.Unlock()
		if msg.UniqueID != client.uniqueID() {
			if s.deps.State == nil {
				return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "state backend unavailable")
			}
			member, ok := s.deps.State.GetClientByUniqueID(msg.UniqueID)
			if !ok || !e.Evaluate(client.userID(), member.ChannelID, authorization.ViewChannel).Allowed ||
				(member.Status == "invisible" && !e.Evaluate(client.userID(), 0, authorization.ViewConnectionInfo).Allowed) {
				return s.sendErrorFor(client, requestOrigin(ctx), errCodeNotFound, "no avatar for this user")
			}
		}
		return s.readAvatar(ctx, client, msg)
	})
}
