package server

import (
	"context"

	"go.uber.org/zap"
	"noxa/internal/netproto"
)

func (s *TCPServer) handleServerAdminList(ctx context.Context, client *Client, f *netproto.Frame) error {
	if !client.isAdmin() {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "only a server admin may list server admins")
	}
	var msg netproto.ServerAdminList
	if err := netproto.Decode(f, &msg); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "malformed server_admin_list")
	}
	if s.deps == nil || s.deps.ServerAdmins == nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "server admin roster unavailable")
	}
	admins, err := s.deps.ServerAdmins.ListServerAdmins(ctx)
	if err != nil {
		s.logger.Warn("server admin roster failed", zap.Error(err))
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "server admin roster unavailable")
	}
	resp := netproto.ServerAdmins{Entries: make([]netproto.ServerAdminEntry, 0, len(admins))}
	for _, admin := range admins {
		resp.Entries = append(resp.Entries, netproto.ServerAdminEntry{UniqueID: admin.UniqueID, Nickname: admin.Nickname})
	}
	return s.writeMessage(client, netproto.MsgServerAdmins, resp)
}
