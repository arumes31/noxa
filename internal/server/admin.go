package server

import (
	"context"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

// --- ban administration (wave 6b) --------------------------------------------

// handleBanList returns the ban list, newest first.
func (s *TCPServer) handleBanList(ctx context.Context, client *Client, f *netproto.Frame) error {
	if err := netproto.Decode(f, &netproto.BanList{}); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "malformed ban_list: "+err.Error())
	}
	return s.roleAction(ctx, client, 0, authorization.BanMembers, func(ctx context.Context) error {
		return s.sendBanList(ctx, client)
	})
}

func (s *TCPServer) sendBanList(ctx context.Context, client *Client) error {
	if s.deps == nil || s.deps.BanAdmin == nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "ban store unavailable")
	}
	bans, err := s.deps.BanAdmin.ListBans(ctx)
	if err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "ban list failed")
	}
	resp := netproto.BanListResponse{Bans: []netproto.BanEntry{}}
	for _, b := range bans {
		e := netproto.BanEntry{
			ID: b.ID, Type: b.Type, Value: b.Value, Reason: b.Reason,
			BannedBy: b.BannedBy, CreatedAt: b.CreatedAt.Unix(),
		}
		if b.ExpiresAt != nil {
			e.ExpiresAt = b.ExpiresAt.Unix()
		}
		resp.Bans = append(resp.Bans, e)
	}
	return s.writeMessage(client, netproto.MsgBanListResponse, resp)
}
