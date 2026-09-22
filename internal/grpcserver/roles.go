package grpcserver

import (
	"context"
	"errors"
	"slices"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"noxa/internal/query"
	noxav1 "noxa/v1"
)

type integrationPrincipalKey struct{}

func (s *Server) roleBackend() query.RoleIntegrationBackend {
	b, ok := s.backend.(query.RoleIntegrationBackend)
	if !ok || !b.RoleIntegrationsEnabled() {
		return nil
	}
	return b
}

// Only implemented role-aware methods may receive integration credentials.
// In particular, the raw event bus and legacy permission/channel APIs stay closed.
func (s *Server) roleUnaryAuth(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler, backend query.RoleIntegrationBackend) (any, error) {
	// gRPC does not issue sessions: compatibility must accompany every RPC,
	// including Authenticate. A prior successful call grants no later bypass.
	const modelHeader = "noxa-authorization-model"
	if err := grpc.SetHeader(ctx, metadata.Pairs(modelHeader, netproto.AuthorizationModelRolesV1)); err != nil {
		return nil, status.Error(codes.Unavailable, "authorization model response unavailable")
	}
	md, _ := metadata.FromIncomingContext(ctx)
	models := md.Get(modelHeader)
	if len(models) != 1 || models[0] != netproto.AuthorizationModelRolesV1 {
		return nil, status.Error(codes.FailedPrecondition, "roles-v1 authorization required; upgrade your integration and send noxa-authorization-model: roles-v1")
	}
	var identifier, password string
	switch info.FullMethod {
	case noxav1.Control_Authenticate_FullMethodName:
		request, ok := req.(*noxav1.AuthenticateRequest)
		if !ok {
			return nil, status.Error(codes.InvalidArgument, "invalid authentication request")
		}
		identifier, password = request.GetUsername(), request.GetPassword()
	case noxav1.Control_ListChannels_FullMethodName, noxav1.Control_ChangeRoles_FullMethodName, noxav1.Control_ChangeChannel_FullMethodName, noxav1.Control_SetMemberVoice_FullMethodName, noxav1.Control_MoveMember_FullMethodName, noxav1.Control_DisconnectMember_FullMethodName, noxav1.Control_KickMember_FullMethodName, noxav1.Control_BanMember_FullMethodName,
		noxav1.Control_GetServerInfo_FullMethodName, noxav1.Control_GetClientInfo_FullMethodName, noxav1.Control_GetServerConfig_FullMethodName, noxav1.Control_GetMediaLimits_FullMethodName, noxav1.Control_SetMediaLimits_FullMethodName, noxav1.Control_ListBans_FullMethodName,
		noxav1.Control_GetRoleState_FullMethodName, noxav1.Control_ListRoleMembers_FullMethodName, noxav1.Control_CheckAccess_FullMethodName, noxav1.Control_GetChannelOptions_FullMethodName,
		noxav1.Control_ListClients_FullMethodName, noxav1.Control_GetChannelInfo_FullMethodName, noxav1.Control_ListAuditLog_FullMethodName,
		noxav1.Control_ListComplaints_FullMethodName, noxav1.Control_ClearComplaints_FullMethodName, noxav1.Control_GetServerRules_FullMethodName, noxav1.Control_SetServerConfig_FullMethodName,
		noxav1.Control_GetChatFilters_FullMethodName, noxav1.Control_SetChatFilters_FullMethodName,
		noxav1.Control_SetServerText_FullMethodName, noxav1.Control_ListCustomMetadata_FullMethodName, noxav1.Control_ChangeCustomMetadata_FullMethodName:
		var err error
		identifier, password, err = s.basicCredentials(ctx)
		if err != nil {
			return nil, err
		}
	default:
		return nil, status.Error(codes.FailedPrecondition, "RPC unavailable with roles-v1 authorization")
	}
	p, err := s.authenticateRoleIntegration(ctx, backend, identifier, password)
	if err != nil {
		return nil, roleStatus(err)
	}
	if info.FullMethod == noxav1.Control_Authenticate_FullMethodName {
		return &noxav1.AuthenticateResponse{UserId: p.UniqueID()}, nil
	}
	return handler(context.WithValue(ctx, integrationPrincipalKey{}, p), req)
}

func (s *Server) authenticateRoleIntegration(ctx context.Context, backend query.RoleIntegrationBackend, identifier, password string) (auth.IntegrationPrincipal, error) {
	if s.limiter == nil {
		return auth.IntegrationPrincipal{}, authorization.ErrRolesNotConfigured
	}
	// Match the existing loopback listener's per-principal limiter. One local
	// process must not lock every integration out via their shared source IP.
	scope := auth.LoginFailureScope("", identifier)
	attempt, allowed := s.limiter.ReserveLoginAttempt(scope)
	if !allowed {
		s.recordAuthFailure("locked_out")
		return auth.IntegrationPrincipal{}, auth.ErrIntegrationDenied
	}
	defer attempt.Cancel()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	p, err := backend.AuthenticateIntegration(ctx, identifier, password, remoteIPFromContext(ctx))
	if err != nil {
		if errors.Is(err, auth.ErrIntegrationDenied) {
			attempt.Fail()
			s.recordAuthFailure("invalid_credentials")
		}
		return auth.IntegrationPrincipal{}, err
	}
	attempt.Succeed(scope)
	return p, nil
}

func (c *controlService) ChangeRoles(ctx context.Context, req *noxav1.ChangeRolesRequest) (*noxav1.ChangeRolesResponse, error) {
	p, authenticated := ctx.Value(integrationPrincipalKey{}).(auth.IntegrationPrincipal)
	b, supported := c.backend.(query.RoleManagementBackend)
	if !authenticated || !supported {
		return nil, status.Error(codes.FailedPrecondition, "role integration required")
	}
	if req.GetExpectedRevision() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "expected_revision must be positive")
	}
	change := roleChangeFromProto(req)
	mutationCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	result, err := b.ChangeIntegrationRoles(mutationCtx, p, change)
	if err != nil {
		return nil, roleStatus(err)
	}
	// Returning the known commit uses the original RPC context, even if the
	// mutation's own reconciliation deadline expired. No new policy read.
	return &noxav1.ChangeRolesResponse{
		Revision: result.Revision, CreatedRoleId: result.CreatedRoleID,
		EnforcementPending: result.EnforcementPending,
	}, nil
}

func roleChangeFromProto(req *noxav1.ChangeRolesRequest) authorization.RoleChange {
	r, ch := req.GetRole(), req.GetChannel()
	change := authorization.RoleChange{
		Kind: authorization.RoleChangeKind(req.GetKind()), ExpectedRevision: req.GetExpectedRevision(),
		RoleID: req.GetRoleId(), RoleIDs: slices.Clone(req.GetRoleIds()), UserID: req.GetUserId(),
		Role: authorization.Role{
			ID: r.GetId(), Name: r.GetName(), Position: int(r.GetPosition()),
			Color: r.GetColor(), Icon: r.GetIcon(), Hoist: r.GetHoist(),
		},
		Channel: authorization.ChannelPolicy{
			ChannelID: ch.GetChannelId(), ParentID: ch.GetParentId(), Synced: ch.GetSynced(),
		},
	}
	for _, permission := range r.GetPermissions() {
		change.Role.Permissions = append(change.Role.Permissions, authorization.Capability(permission))
	}
	for _, override := range ch.GetOverrides() {
		change.Channel.Overrides = append(change.Channel.Overrides, authorization.RoleOverride{
			RoleID: override.GetRoleId(), UserID: override.GetUserId(),
			Capability: authorization.Capability(override.GetCapability()), Effect: authorization.OverrideEffect(override.GetEffect()),
		})
	}
	return change
}

func roleStatus(err error) error {
	switch {
	case errors.Is(err, auth.ErrIntegrationDenied):
		return status.Error(codes.Unauthenticated, "invalid integration credentials")
	case errors.Is(err, authorization.ErrRoleForbidden):
		return status.Error(codes.PermissionDenied, "role operation forbidden")
	case errors.Is(err, authorization.ErrRoleConflict):
		return status.Error(codes.Aborted, "permissions changed; refresh before saving")
	case errors.Is(err, authorization.ErrRoleInvalid):
		return status.Error(codes.InvalidArgument, "invalid role operation")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return status.FromContextError(err).Err()
	default:
		return status.Error(codes.Unavailable, "integration authorization unavailable")
	}
}
