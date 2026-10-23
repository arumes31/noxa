package server

import (
	"context"
	"strconv"
	"time"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

func (s *TCPServer) handleRoleBanRemove(ctx context.Context, client *Client, f *netproto.Frame) error {
	var request netproto.BanRemove
	if err := netproto.Decode(f, &request); err != nil || request.BanID < 1 {
		return s.roleError(ctx, client, authorization.ErrRoleInvalid)
	}
	if s.deps == nil || s.deps.Authority == nil {
		return s.roleError(ctx, client, authorization.ErrRolesNotConfigured)
	}
	err := s.withRoleAccess(ctx, client, 0, authorization.BanMembers, func(ctx context.Context) error {
		if s.deps.BanAdmin == nil {
			return authorization.ErrAuthorizationUnavailable
		}
		issuer, err := s.deps.BanAdmin.BanIssuer(ctx, request.BanID)
		if err != nil {
			return err
		}
		if issuer == nil {
			return nil
		} // Already absent remains idempotent.
		e := ctx.Value(roleLeaseKey{}).(roleLease).evaluator
		if !canRemoveIssuedBan(e, client.userID(), *issuer) {
			return authorization.ErrRoleForbidden
		}
		return s.removeRoleBan(ctx, client.uniqueID(), request.BanID)
	})
	if err != nil {
		return s.roleError(ctx, client, err)
	}
	// A committed deletion stays acknowledged even if its request context ended.
	replyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return s.writeMessageInContext(replyCtx, client, netproto.MsgRoleBanRemoved, netproto.RoleBanRemoved(request))
}

// Caller retains current admission and BanMembers authority through deletion.
func (s *TCPServer) removeRoleBan(ctx context.Context, actor string, banID int64) error {
	if banID < 1 {
		return authorization.ErrRoleInvalid
	}
	if s.deps == nil || s.deps.BanAdmin == nil {
		return authorization.ErrAuthorizationUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.deps.BanAdmin.DeleteBan(ctx, banID); err != nil {
		return err
	}
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	s.audit(auditCtx, actor, "ban_remove", strconv.FormatInt(banID, 10), "")
	return nil
}

func canRemoveIssuedBan(e *authorization.RoleEvaluator, actor, issuer int64) bool {
	return actor > 0 && (actor == e.Policy().OwnerID || actor == issuer || issuer > 0 && e.CanManageMember(actor, issuer))
}
