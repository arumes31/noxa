package main

import (
	"context"

	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/broadcast"
	"noxa/internal/eventbus"
	"noxa/internal/netproto"
)

func (q *queryBackend) RoleIntegrationsEnabled() bool {
	return q.tcp.RoleIntegrationsEnabled()
}

func (q *queryBackend) AuthenticateIntegration(ctx context.Context, identifier, password, remoteIP string) (auth.IntegrationPrincipal, error) {
	return q.tcp.AuthenticateIntegration(ctx, identifier, password, remoteIP)
}

func (q *queryBackend) WithIntegrationSnapshot(ctx context.Context, principal auth.IntegrationPrincipal, deliver func(context.Context, *broadcast.TreeSnapshot) error) error {
	return q.tcp.WithIntegrationSnapshot(ctx, principal, deliver)
}

func (q *queryBackend) WithIntegrationEvent(ctx context.Context, principal auth.IntegrationPrincipal, event eventbus.Event, deliver func(context.Context, broadcast.IntegrationEvent) error) error {
	return q.tcp.WithIntegrationEvent(ctx, principal, event, deliver)
}

func (q *queryBackend) WithIntegrationRoleState(ctx context.Context, principal auth.IntegrationPrincipal, channelID int64, deliver func(context.Context, netproto.RoleState) error) error {
	return q.tcp.WithIntegrationRoleState(ctx, principal, channelID, deliver)
}

func (q *queryBackend) ChangeIntegrationRoles(ctx context.Context, principal auth.IntegrationPrincipal, change authorization.RoleChange) (netproto.RoleChangeResult, error) {
	return q.tcp.ChangeIntegrationRoles(ctx, principal, change)
}

func (q *queryBackend) WithIntegrationRoleMembers(ctx context.Context, principal auth.IntegrationPrincipal, request authorization.MemberQuery, deliver func(context.Context, authorization.MemberPage) error) error {
	return q.tcp.WithIntegrationRoleMembers(ctx, principal, request, deliver)
}

func (q *queryBackend) WithIntegrationAccessCheck(ctx context.Context, principal auth.IntegrationPrincipal, request netproto.AccessCheck, deliver func(context.Context, netproto.AccessCheckResult) error) error {
	return q.tcp.WithIntegrationAccessCheck(ctx, principal, request, deliver)
}

func (q *queryBackend) WithIntegrationChannelState(ctx context.Context, principal auth.IntegrationPrincipal, request netproto.RoleChannelQuery, deliver func(context.Context, netproto.RoleChannelState) error) error {
	return q.tcp.WithIntegrationChannelState(ctx, principal, request, deliver)
}

func (q *queryBackend) ChangeIntegrationChannel(ctx context.Context, principal auth.IntegrationPrincipal, request netproto.RoleChannelChange) (netproto.RoleChannelResult, error) {
	return q.tcp.ChangeIntegrationChannel(ctx, principal, request)
}

func (q *queryBackend) SetIntegrationMemberVoice(ctx context.Context, principal auth.IntegrationPrincipal, request netproto.MemberVoiceSet) (netproto.MemberVoiceState, error) {
	return q.tcp.SetIntegrationMemberVoice(ctx, principal, request)
}

func (q *queryBackend) MoveIntegrationMember(ctx context.Context, principal auth.IntegrationPrincipal, request netproto.MoveClient) error {
	return q.tcp.MoveIntegrationMember(ctx, principal, request)
}

func (q *queryBackend) DisconnectIntegrationMember(ctx context.Context, principal auth.IntegrationPrincipal, request netproto.MemberDisconnect) (netproto.MemberDisconnectResult, error) {
	return q.tcp.DisconnectIntegrationMember(ctx, principal, request)
}

func (q *queryBackend) KickIntegrationMember(ctx context.Context, principal auth.IntegrationPrincipal, request netproto.MemberKick) (netproto.MemberKickResult, error) {
	return q.tcp.KickIntegrationMember(ctx, principal, request)
}

func (q *queryBackend) BanIntegrationMember(ctx context.Context, principal auth.IntegrationPrincipal, request netproto.MemberBan) (netproto.MemberBanResult, error) {
	return q.tcp.BanIntegrationMember(ctx, principal, request)
}

func (q *queryBackend) WithIntegrationBans(ctx context.Context, principal auth.IntegrationPrincipal, request netproto.BanQuery, deliver func(context.Context, netproto.BanPage) error) error {
	return q.tcp.WithIntegrationBans(ctx, principal, request, deliver)
}

func (q *queryBackend) WithIntegrationAudit(ctx context.Context, principal auth.IntegrationPrincipal, request netproto.AuditLog, deliver func(context.Context, netproto.AuditLogResponse) error) error {
	return q.tcp.WithIntegrationAudit(ctx, principal, request, deliver)
}

func (q *queryBackend) WithIntegrationComplaints(ctx context.Context, principal auth.IntegrationPrincipal, request netproto.ComplaintQuery, deliver func(context.Context, netproto.ComplaintPage) error) error {
	return q.tcp.WithIntegrationComplaints(ctx, principal, request, deliver)
}

func (q *queryBackend) ClearIntegrationComplaints(ctx context.Context, principal auth.IntegrationPrincipal, request netproto.ComplaintClear) (netproto.ComplaintClearResult, error) {
	return q.tcp.ClearIntegrationComplaints(ctx, principal, request)
}

func (q *queryBackend) WithIntegrationServerInfo(ctx context.Context, principal auth.IntegrationPrincipal, deliver func(context.Context, netproto.ServerInfoResponse) error) error {
	return q.tcp.WithIntegrationServerInfo(ctx, principal, deliver)
}

func (q *queryBackend) WithIntegrationClientInfo(ctx context.Context, principal auth.IntegrationPrincipal, targetID string, deliver func(context.Context, netproto.ClientInfoResponse) error) error {
	return q.tcp.WithIntegrationClientInfo(ctx, principal, targetID, deliver)
}

func (q *queryBackend) WithIntegrationServerConfig(ctx context.Context, principal auth.IntegrationPrincipal, deliver func(context.Context, netproto.ServerConfig) error) error {
	return q.tcp.WithIntegrationServerConfig(ctx, principal, deliver)
}

func (q *queryBackend) WithIntegrationRules(ctx context.Context, principal auth.IntegrationPrincipal, deliver func(context.Context, netproto.RulesInspection) error) error {
	return q.tcp.WithIntegrationRules(ctx, principal, deliver)
}

func (q *queryBackend) SetIntegrationServerConfig(ctx context.Context, principal auth.IntegrationPrincipal, request netproto.ServerConfig) (netproto.ServerConfig, error) {
	return q.tcp.SetIntegrationServerConfig(ctx, principal, request)
}

func (q *queryBackend) WithIntegrationChatFilters(ctx context.Context, principal auth.IntegrationPrincipal, deliver func(context.Context, netproto.ChatFilterResponse) error) error {
	return q.tcp.WithIntegrationChatFilters(ctx, principal, deliver)
}

func (q *queryBackend) SetIntegrationChatFilters(ctx context.Context, principal auth.IntegrationPrincipal, patch netproto.ChatFilterSet) (netproto.ChatFilterResponse, error) {
	return q.tcp.SetIntegrationChatFilters(ctx, principal, patch)
}

func (q *queryBackend) SetIntegrationServerText(ctx context.Context, principal auth.IntegrationPrincipal, request netproto.ServerTextSet) (netproto.ServerTextResult, error) {
	return q.tcp.SetIntegrationServerText(ctx, principal, request)
}

func (q *queryBackend) WithIntegrationCustomMetadata(ctx context.Context, principal auth.IntegrationPrincipal, request netproto.CustomMetadataQuery, deliver func(context.Context, netproto.CustomMetadataPage) error) error {
	return q.tcp.WithIntegrationCustomMetadata(ctx, principal, request, deliver)
}

func (q *queryBackend) ChangeIntegrationCustomMetadata(ctx context.Context, principal auth.IntegrationPrincipal, request netproto.CustomMetadataChange) (netproto.CustomMetadataResult, error) {
	return q.tcp.ChangeIntegrationCustomMetadata(ctx, principal, request)
}
