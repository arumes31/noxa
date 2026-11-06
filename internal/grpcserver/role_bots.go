package grpcserver

import (
	"context"
	"slices"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"noxa/internal/auth"
	"noxa/internal/netproto"
	"noxa/internal/query"
	noxav1 "noxa/v1"
)

// methodScopes maps every gRPC method an integration may call to the scope it
// needs (807). Authenticate needs no scope. A method missing here is refused
// for scoped accounts, so a new RPC cannot slip past a ceiling unnoticed.
var methodScopes = map[string]string{
	noxav1.Control_ListChannels_FullMethodName:       auth.ScopeServerRead,
	noxav1.Control_ListClients_FullMethodName:        auth.ScopeServerRead,
	noxav1.Control_GetChannelInfo_FullMethodName:     auth.ScopeServerRead,
	noxav1.Control_GetChannelOptions_FullMethodName:  auth.ScopeServerRead,
	noxav1.Control_GetServerInfo_FullMethodName:      auth.ScopeServerRead,
	noxav1.Control_GetClientInfo_FullMethodName:      auth.ScopeServerRead,
	noxav1.Control_GetServerConfig_FullMethodName:    auth.ScopeServerRead,
	noxav1.Control_GetMediaLimits_FullMethodName:     auth.ScopeServerRead,
	noxav1.Control_GetServerRules_FullMethodName:     auth.ScopeServerRead,
	noxav1.Control_GetChatFilters_FullMethodName:     auth.ScopeServerRead,
	noxav1.Control_ListCustomMetadata_FullMethodName: auth.ScopeServerRead,

	noxav1.Control_SetServerConfig_FullMethodName:      auth.ScopeServerManage,
	noxav1.Control_SetMediaLimits_FullMethodName:       auth.ScopeServerManage,
	noxav1.Control_SetChatFilters_FullMethodName:       auth.ScopeServerManage,
	noxav1.Control_SetServerText_FullMethodName:        auth.ScopeServerManage,
	noxav1.Control_ChangeCustomMetadata_FullMethodName: auth.ScopeServerManage,
	noxav1.Control_GetIntegrationScopes_FullMethodName: auth.ScopeServerManage,
	noxav1.Control_SetIntegrationScopes_FullMethodName: auth.ScopeServerManage,
	noxav1.Control_GetRoleState_FullMethodName:         auth.ScopeRolesRead,
	noxav1.Control_ListRoleMembers_FullMethodName:      auth.ScopeRolesRead,
	noxav1.Control_CheckAccess_FullMethodName:          auth.ScopeRolesRead,
	noxav1.Control_ChangeRoles_FullMethodName:          auth.ScopeRolesManage,
	noxav1.Control_ChangeChannel_FullMethodName:        auth.ScopeChannelsManage,
	noxav1.Control_SetMemberVoice_FullMethodName:       auth.ScopeMembersVoice,
	noxav1.Control_MoveMember_FullMethodName:           auth.ScopeMembersMove,
	noxav1.Control_DisconnectMember_FullMethodName:     auth.ScopeMembersMove,
	noxav1.Control_KickMember_FullMethodName:           auth.ScopeMembersKick,
	noxav1.Control_BanMember_FullMethodName:            auth.ScopeMembersBan,
	noxav1.Control_ListBans_FullMethodName:             auth.ScopeMembersBan,
	noxav1.Control_ListDevices_FullMethodName:          auth.ScopeDevicesManage,
	noxav1.Control_RevokeDevice_FullMethodName:         auth.ScopeDevicesManage,
	noxav1.Control_ResetDevices_FullMethodName:         auth.ScopeDevicesManage,
	noxav1.Control_ListAuditLog_FullMethodName:         auth.ScopeAuditRead,
	noxav1.Control_ListComplaints_FullMethodName:       auth.ScopeComplaints,
	noxav1.Control_ClearComplaints_FullMethodName:      auth.ScopeComplaints,
	noxav1.Control_BulkDeleteMessages_FullMethodName:   auth.ScopeMessagesManage,
	noxav1.Events_Subscribe_FullMethodName:             auth.ScopeEventsRead,
}

// MethodScope returns the scope a gRPC method needs.
func MethodScope(fullMethod string) (string, bool) {
	scope, ok := methodScopes[fullMethod]
	return scope, ok
}

// methodAllowedByScope applies the principal's ceiling before the handler.
func methodAllowedByScope(principal auth.IntegrationPrincipal, fullMethod string) bool {
	if _, scoped := principal.Scopes(); !scoped {
		return true
	}
	scope, ok := methodScopes[fullMethod]
	return ok && principal.AllowsScope(scope)
}

var errOutsideScopes = status.Error(codes.PermissionDenied, "RPC outside this integration's scopes")

func (c *controlService) BulkDeleteMessages(ctx context.Context, req *noxav1.BulkDeleteMessagesRequest) (*noxav1.BulkDeleteMessagesResponse, error) {
	p, authenticated := ctx.Value(integrationPrincipalKey{}).(auth.IntegrationPrincipal)
	b, supported := c.backend.(query.RoleBotBackend)
	if !authenticated || !supported {
		return nil, status.Error(codes.FailedPrecondition, "role integration required")
	}
	request := netproto.MessageBulkDelete{ChannelID: req.GetChannelId(), MessageIDs: slices.Clone(req.GetMessageIds())}
	if !request.Valid() {
		return nil, status.Error(codes.InvalidArgument, "channel_id and 1-100 unique positive message_ids required")
	}
	effectCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	result, err := b.BulkDeleteIntegrationMessages(effectCtx, p, request)
	if err != nil {
		return nil, roleStatus(err)
	}
	return &noxav1.BulkDeleteMessagesResponse{DeletedIds: result.Deleted, NotFoundIds: result.NotFound}, nil
}

func (c *controlService) GetIntegrationScopes(ctx context.Context, req *noxav1.GetIntegrationScopesRequest) (*noxav1.GetIntegrationScopesResponse, error) {
	p, authenticated := ctx.Value(integrationPrincipalKey{}).(auth.IntegrationPrincipal)
	b, supported := c.backend.(query.RoleBotBackend)
	if !authenticated || !supported {
		return nil, status.Error(codes.FailedPrecondition, "role integration required")
	}
	request := netproto.IntegrationScopesQuery{UniqueID: req.GetUniqueId()}
	if !request.Valid() {
		return nil, status.Error(codes.InvalidArgument, "unique_id required")
	}
	effectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result, err := b.GetIntegrationScopes(effectCtx, p, request)
	if err != nil {
		return nil, roleStatus(err)
	}
	return &noxav1.GetIntegrationScopesResponse{UniqueId: result.UniqueID, Full: result.Full, Scopes: result.Scopes}, nil
}

func (c *controlService) SetIntegrationScopes(ctx context.Context, req *noxav1.SetIntegrationScopesRequest) (*noxav1.SetIntegrationScopesResponse, error) {
	p, authenticated := ctx.Value(integrationPrincipalKey{}).(auth.IntegrationPrincipal)
	b, supported := c.backend.(query.RoleBotBackend)
	if !authenticated || !supported {
		return nil, status.Error(codes.FailedPrecondition, "role integration required")
	}
	scopes := slices.Clone(req.GetScopes())
	if scopes == nil && !req.GetFull() {
		scopes = []string{} // proto cannot tell an empty list from none
	}
	request := netproto.IntegrationScopesSet{UniqueID: req.GetUniqueId(), Full: req.GetFull(), Scopes: scopes}
	if !request.Valid() {
		return nil, status.Error(codes.InvalidArgument, "unique_id and either scopes or full required")
	}
	effectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result, err := b.SetIntegrationScopes(effectCtx, p, request)
	if err != nil {
		return nil, roleStatus(err)
	}
	return &noxav1.SetIntegrationScopesResponse{UniqueId: result.UniqueID, Full: result.Full, Scopes: result.Scopes}, nil
}
