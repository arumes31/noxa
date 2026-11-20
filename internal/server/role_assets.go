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
		// The disk read, image decode and the large reply stay outside the
		// server-wide metadata lock: one slow reader must not stall channel
		// changes, moderation and disconnect cleanup for everyone.
		visible, unavailable := func() (bool, bool) {
			s.roleMetadataMu.Lock()
			defer s.roleMetadataMu.Unlock()
			if msg.UniqueID == client.uniqueID() {
				return true, false
			}
			if s.deps.State == nil {
				return false, true
			}
			member, ok := s.deps.State.GetClientByUniqueID(msg.UniqueID)
			return ok && e.Evaluate(client.userID(), member.ChannelID, authorization.ViewChannel).Allowed &&
				(!s.deps.State.PresenceHidden(member) || e.Evaluate(client.userID(), 0, authorization.ViewConnectionInfo).Allowed), false
		}()
		if unavailable {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "state backend unavailable")
		}
		if !visible {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeNotFound, "no avatar for this user")
		}
		return s.readAvatar(ctx, client, msg)
	})
}
