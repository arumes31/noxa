package server

import (
	"context"

	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

// WithIntegrationAudit shares native audit provenance and per-row filtering,
// retaining current account admission, policy and metadata through delivery.
func (s *TCPServer) WithIntegrationAudit(ctx context.Context, principal auth.IntegrationPrincipal, request netproto.AuditLog, deliver func(context.Context, netproto.AuditLogResponse) error) error {
	if deliver == nil || request.BeforeID < 0 || request.Limit < 0 || request.Limit > 200 {
		return authorization.ErrRoleInvalid
	}
	if request.Limit == 0 {
		request.Limit = 50
	}
	return s.withIntegrationPolicy(ctx, principal, func(ctx context.Context, e *authorization.RoleEvaluator) error {
		if !e.Evaluate(principal.UserID(), 0, authorization.ViewAuditLog).Allowed {
			return authorization.ErrRoleForbidden
		}
		if s.deps.Groups == nil {
			return authorization.ErrAuthorizationUnavailable
		}
		entries, err := s.deps.Groups.AuditList(ctx, request.BeforeID, request.Limit)
		if err != nil {
			return err
		}
		response := netproto.AuditLogResponse{Entries: []netproto.AuditEntry{}, Capabilities: authorization.Capabilities()}
		administrator := e.Evaluate(principal.UserID(), 0, authorization.Administrator).Allowed
		visible := func(channel int64) bool {
			return e.Evaluate(principal.UserID(), channel, authorization.ViewChannel).Allowed
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			response.Entries = append(response.Entries, projectAuditEntry(entry, administrator, visible))
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return deliver(ctx, response)
	})
}
