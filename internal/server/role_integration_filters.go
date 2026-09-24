package server

import (
	"context"

	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

func (s *TCPServer) WithIntegrationChatFilters(ctx context.Context, principal auth.IntegrationPrincipal, deliver func(context.Context, netproto.ChatFilterResponse) error) error {
	if deliver == nil {
		return authorization.ErrRoleInvalid
	}
	return s.withIntegrationPolicy(ctx, principal, func(ctx context.Context, e *authorization.RoleEvaluator) error {
		if !e.Evaluate(principal.UserID(), 0, authorization.ManageChatFilters).Allowed {
			return authorization.ErrRoleForbidden
		}
		result, err := s.readManagedChatFilters(ctx)
		if err != nil {
			return err
		}
		return deliver(ctx, result)
	})
}

func (s *TCPServer) SetIntegrationChatFilters(ctx context.Context, principal auth.IntegrationPrincipal, patch netproto.ChatFilterSet) (netproto.ChatFilterResponse, error) {
	if !patch.ValidLists() || (patch.WordFilter == nil && patch.LinkBlacklist == nil && patch.LinkWhitelist == nil) {
		return netproto.ChatFilterResponse{}, authorization.ErrRoleInvalid
	}
	var result netproto.ChatFilterResponse
	err := s.withIntegrationPolicy(ctx, principal, func(ctx context.Context, e *authorization.RoleEvaluator) error {
		if !e.Evaluate(principal.UserID(), 0, authorization.ManageChatFilters).Allowed {
			return authorization.ErrRoleForbidden
		}
		var err error
		result, err = s.saveChatFilters(ctx, principal.UniqueID(), patch)
		return err
	})
	return result, err
}
