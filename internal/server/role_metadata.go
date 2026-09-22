package server

import (
	"context"
	"errors"
	"net"
	"runtime"
	"strconv"
	"time"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"noxa/internal/version"
)

func (s *TCPServer) roleClientInfo(ctx context.Context, client *Client, targetID string) error {
	return s.rolePolicyRead(ctx, client, func(ctx context.Context) error {
		e := ctx.Value(roleLeaseKey{}).(roleLease).evaluator
		s.roleMetadataMu.Lock()
		defer s.roleMetadataMu.Unlock()
		resp, err := s.buildRoleClientInfo(e, client.userID(), client.uniqueID(), client.ID, targetID)
		if errors.Is(err, authorization.ErrRoleForbidden) {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeNotFound, "client not found")
		}
		if err != nil {
			return err
		}
		return s.writeMessage(client, netproto.MsgClientInfoResponse, resp)
	})
}

// Callers hold both policy and metadata leases. Only a native connection can
// supply selfSessionID; an integration has no native self-session exemption.
func (s *TCPServer) buildRoleClientInfo(e *authorization.RoleEvaluator, actorID int64, actorUID, selfSessionID, targetID string) (netproto.ClientInfoResponse, error) {
	target, ok := s.clientByID(targetID)
	if !ok || !target.isAuthed() || s.deps.State == nil {
		return netproto.ClientInfoResponse{}, authorization.ErrRoleForbidden
	}
	member, ok := s.deps.State.GetClient(targetID)
	self := selfSessionID != "" && targetID == selfSessionID
	showStats := self || e.Evaluate(actorID, 0, authorization.ViewConnectionInfo).Allowed
	if !ok || (!self && (!e.Evaluate(actorID, member.ChannelID, authorization.ViewChannel).Allowed || (member.Status == "invisible" && member.UniqueID != actorUID && !showStats))) {
		return netproto.ClientInfoResponse{}, authorization.ErrRoleForbidden
	}
	resp := netproto.ClientInfoResponse{ClientID: member.ClientID, UniqueID: member.UniqueID, Nickname: member.Nickname, ChannelID: member.ChannelID, PingMs: -1}
	if showStats {
		st := target.stats()
		resp.ConnectedAt = member.ConnectedAt.Unix()
		resp.IdleSeconds = int64(time.Since(st.lastActive).Seconds())
		if st.rttKnown {
			resp.PingMs = st.rttNs / int64(time.Millisecond)
		}
		resp.BytesIn, resp.BytesOut = st.bytesIn, st.bytesOut
	}
	if self || e.Evaluate(actorID, 0, authorization.ViewRemoteAddresses).Allowed {
		if host, port, err := net.SplitHostPort(target.Conn.RemoteAddr().String()); err == nil {
			resp.IP = host
			resp.Port, _ = strconv.Atoi(port)
		}
	}
	return resp, nil
}

func (s *TCPServer) roleServerInfo(ctx context.Context, client *Client) error {
	return s.rolePolicyRead(ctx, client, func(ctx context.Context) error {
		e := ctx.Value(roleLeaseKey{}).(roleLease).evaluator
		s.roleMetadataMu.Lock()
		defer s.roleMetadataMu.Unlock()
		resp, err := s.buildRoleServerInfo(ctx, e, client.userID(), client.uniqueID())
		if err != nil {
			return err
		}
		return s.writeMessage(client, netproto.MsgServerInfoResponse, resp)
	})
}

// Callers hold policy and metadata leases through the returned data's delivery.
func (s *TCPServer) buildRoleServerInfo(ctx context.Context, e *authorization.RoleEvaluator, actorID int64, actorUID string) (netproto.ServerInfoResponse, error) {
	s.configMu.RLock()
	resp := netproto.ServerInfoResponse{Name: s.cfg.ServerName, Version: version.String(), Platform: runtime.GOOS + "/" + runtime.GOARCH, MaxClients: s.cfg.MaxClients}
	publicMOTD := s.cfg.ServerInfoMOTD
	s.configMu.RUnlock()
	if name := s.serverSetting(ctx, "server_name"); name != "" {
		resp.Name = name
	}
	if publicMOTD && e.Evaluate(actorID, 0, authorization.ViewChannel).Allowed {
		resp.MOTD = s.serverSettingPlain(ctx, "motd")
	}
	if s.deps.State != nil {
		snapshot, err := buildRoleSnapshotContext(ctx, s.deps.State, e, actorID, actorUID)
		if err != nil {
			return netproto.ServerInfoResponse{}, err
		}
		resp.ClientsOnline, resp.ChannelsOnline = snapshot.TotalClients, snapshot.TotalChannels
	}
	if !s.startedAt.IsZero() {
		resp.UptimeSeconds = int64(time.Since(s.startedAt).Seconds())
	}
	return resp, ctx.Err()
}
