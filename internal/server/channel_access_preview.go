package server

import (
	"context"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

func (s *TCPServer) handleChannelAccessPreview(ctx context.Context, client *Client, f *netproto.Frame) error {
	if s.deps == nil || s.deps.Authority == nil {
		return s.roleError(ctx, client, authorization.ErrRolesNotConfigured)
	}
	var query netproto.ChannelAccessPreview
	if err := netproto.Decode(f, &query); err != nil {
		return s.roleError(ctx, client, authorization.ErrRoleInvalid)
	}
	p, _, err := s.readRolePolicy(ctx)
	if err != nil {
		return s.roleError(ctx, client, err)
	}
	var result authorization.ChannelAccessImpact
	if query.Tree != nil {
		if query.Change.Kind != "" {
			return s.roleError(ctx, client, authorization.ErrRoleInvalid)
		}
		result, err = authorization.PreviewChannelTreeInScope(ctx, p, client.userID(), *query.Tree, query.ScopeChannelID, query.UserIDs)
	} else {
		result, err = authorization.PreviewChannelAccessInScope(ctx, p, client.userID(), query.Change, query.ScopeChannelID, query.UserIDs)
	}
	if err != nil {
		return s.roleError(ctx, client, err)
	}
	return s.writeMessage(client, netproto.MsgChannelAccessImpact, result)
}
