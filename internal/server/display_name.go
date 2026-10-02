package server

import (
	"context"

	"noxa/internal/netproto"
)

const eventNicknameChanged = "nickname_changed"

func (s *TCPServer) handleDisplayNameSet(ctx context.Context, client *Client, f *netproto.Frame) error {
	var msg netproto.DisplayNameSet
	if err := netproto.Decode(f, &msg); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "malformed display name")
	}
	name, err := netproto.NormalizeDisplayName(msg.Nickname)
	if err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, err.Error())
	}
	if s.deps == nil || s.deps.State == nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "state backend unavailable")
	}
	return s.rolePolicyRead(ctx, client, func(ctx context.Context) error {
		s.roleMetadataMu.Lock()
		defer s.roleMetadataMu.Unlock()
		client.roleActionMu.Lock()
		defer client.roleActionMu.Unlock()
		for _, member := range s.deps.State.ListClients() {
			if member.ClientID != client.ID && member.Nickname == name {
				return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "display name is already in use")
			}
		}
		if !s.deps.State.SetNickname(client.ID, name) {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "session unavailable")
		}
		client.mu.Lock()
		client.Username = name
		client.mu.Unlock()
		e := ctx.Value(roleLeaseKey{}).(roleLease).evaluator
		s.refreshRolePublishers(e)
		s.broadcastEvent(eventNicknameChanged, netproto.DisplayNameSaved{ClientID: client.ID, Nickname: name})
		return s.writeCommittedReply(client, netproto.MsgDisplayNameSaved, netproto.DisplayNameSaved{ClientID: client.ID, Nickname: name})
	})
}
