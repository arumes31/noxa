package server

import (
	"context"
	"errors"
	"time"

	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/broadcast"
	"noxa/internal/netproto"
)

type integrationAuthenticator interface {
	AuthenticateIntegration(context.Context, string, string, string) (auth.IntegrationPrincipal, error)
	ValidateIntegration(context.Context, auth.IntegrationPrincipal) error
}

// Bound copying/sorting before the cancellation-aware role traversal. Larger
// deployments need a paginated integration API, not unbounded query responses.
const maxIntegrationSnapshotItems = 10000

// RoleIntegrationsEnabled reports the fixed startup authorization model. It
// does not authenticate a caller or indicate that the policy is available.
func (s *TCPServer) RoleIntegrationsEnabled() bool {
	return s != nil && s.deps != nil && s.deps.Authority != nil
}

// AuthenticateIntegration accepts only explicit integration accounts. The
// transport owns source-derived login rate limits and session lifetime.
func (s *TCPServer) AuthenticateIntegration(ctx context.Context, identifier, password, remoteIP string) (auth.IntegrationPrincipal, error) {
	if !s.RoleIntegrationsEnabled() {
		return auth.IntegrationPrincipal{}, authorization.ErrRolesNotConfigured
	}
	a, ok := s.deps.Auth.(integrationAuthenticator)
	if !ok {
		return auth.IntegrationPrincipal{}, authorization.ErrAuthorizationUnavailable
	}
	principal, err := a.AuthenticateIntegration(ctx, identifier, password, remoteIP)
	if err != nil {
		return auth.IntegrationPrincipal{}, err
	}
	if err := s.deps.Authority.RefreshIfChanged(ctx, principal.UserID(), s.noLiveMemberSession); err != nil {
		return auth.IntegrationPrincipal{}, err
	}
	if err := s.withIntegrationPolicy(ctx, principal, func(context.Context, *authorization.RoleEvaluator) error { return nil }); err != nil {
		return auth.IntegrationPrincipal{}, err
	}
	return principal, nil
}

// Called by Authority under its exclusive gate. A connected native account
// needs the normal reconciliation to update its session and subscriptions.
func (s *TCPServer) noLiveMemberSession(userID int64) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, client := range s.clients {
		if client.isAuthed() && client.userID() == userID {
			return false
		}
	}
	return true
}

// WithIntegrationSnapshot delivers the same filtered tree as a native client.
// The callback must finish serialization and its bounded transport write here;
// it must not retain the snapshot or call back into the server. Role changes,
// live bans and presence/membership changes serialize with this delivery.
func (s *TCPServer) WithIntegrationSnapshot(ctx context.Context, principal auth.IntegrationPrincipal, deliver func(context.Context, *broadcast.TreeSnapshot) error) error {
	if deliver == nil {
		return authorization.ErrRoleInvalid
	}
	return s.withIntegrationPolicy(ctx, principal, func(ctx context.Context, e *authorization.RoleEvaluator) error {
		snapshot, err := s.integrationSnapshot(ctx, e, principal.UserID(), principal.UniqueID())
		if err != nil {
			return err
		}
		return deliver(ctx, snapshot)
	})
}

// withIntegrationPolicy pins account admission, policy and live metadata for a
// bounded effect. Each effect still authorizes its own capability and scope.
func (s *TCPServer) withIntegrationPolicy(ctx context.Context, principal auth.IntegrationPrincipal, effect func(context.Context, *authorization.RoleEvaluator) error) error {
	return s.withIntegrationPolicyMode(ctx, principal, false, effect)
}

func (s *TCPServer) withExclusiveIntegrationPolicy(ctx context.Context, principal auth.IntegrationPrincipal, effect func(context.Context, *authorization.RoleEvaluator) error) error {
	return s.withIntegrationPolicyMode(ctx, principal, true, effect)
}

func (s *TCPServer) withIntegrationPolicyMode(ctx context.Context, principal auth.IntegrationPrincipal, exclusive bool, effect func(context.Context, *authorization.RoleEvaluator) error) error {
	if !s.RoleIntegrationsEnabled() {
		return authorization.ErrRolesNotConfigured
	}
	a, ok := s.deps.Auth.(integrationAuthenticator)
	if !ok {
		return authorization.ErrAuthorizationUnavailable
	}
	lease := s.withRolePolicy
	if exclusive {
		lease = s.withExclusiveRolePolicy
	}
	return lease(ctx, func(ctx context.Context) error {
		s.roleMetadataMu.Lock()
		defer s.roleMetadataMu.Unlock()
		if err := a.ValidateIntegration(ctx, principal); err != nil {
			return err
		}
		e := ctx.Value(roleLeaseKey{}).(roleLease).evaluator
		return effect(ctx, e)
	})
}

// WithIntegrationRoleState uses the native role editor's management projection.
// Its callback has the same bounded-delivery contract as WithIntegrationSnapshot.
func (s *TCPServer) WithIntegrationRoleState(ctx context.Context, principal auth.IntegrationPrincipal, channelID int64, deliver func(context.Context, netproto.RoleState) error) error {
	if deliver == nil {
		return authorization.ErrRoleInvalid
	}
	return s.withIntegrationPolicy(ctx, principal, func(ctx context.Context, e *authorization.RoleEvaluator) error {
		p, err := e.BoundedPolicy(ctx, maxIntegrationSnapshotItems)
		if err != nil {
			return err
		}
		state, err := buildRoleState(ctx, p, e, principal.UserID(), channelID)
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return deliver(ctx, state)
	})
}

// ChangeIntegrationRoles uses the same transactional mutation, hierarchy and
// audit path as native clients. Admission is checked after obtaining the writer
// barrier, before any commit. No read lease is held while acquiring that barrier.
func (s *TCPServer) ChangeIntegrationRoles(ctx context.Context, principal auth.IntegrationPrincipal, change authorization.RoleChange) (netproto.RoleChangeResult, error) {
	if !s.RoleIntegrationsEnabled() {
		return netproto.RoleChangeResult{}, authorization.ErrRolesNotConfigured
	}
	a, ok := s.deps.Auth.(integrationAuthenticator)
	if !ok {
		return netproto.RoleChangeResult{}, authorization.ErrAuthorizationUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	p, err := s.deps.Authority.ChangeRolePolicyValidated(ctx, principal.UserID(), change, func(ctx context.Context) error {
		s.roleMetadataMu.Lock()
		defer s.roleMetadataMu.Unlock()
		return a.ValidateIntegration(ctx, principal)
	})
	if err != nil && !errors.Is(err, authorization.ErrEnforcementPending) {
		return netproto.RoleChangeResult{}, err
	}
	return roleChangeResult(p, change, err), nil
}

// WithIntegrationAccessCheck explains a subject's saved access without acting
// as that subject or reading protected content. Delivery retains the read lease.
func (s *TCPServer) WithIntegrationAccessCheck(ctx context.Context, principal auth.IntegrationPrincipal, query netproto.AccessCheck, deliver func(context.Context, netproto.AccessCheckResult) error) error {
	if deliver == nil {
		return authorization.ErrRoleInvalid
	}
	return s.withIntegrationPolicy(ctx, principal, func(ctx context.Context, e *authorization.RoleEvaluator) error {
		result, err := buildAccessCheck(e, principal.UserID(), query)
		if err != nil {
			return err
		}
		return deliver(ctx, result)
	})
}

// WithIntegrationRoleMembers uses the bounded native roster, including offline
// accounts and protected members whose access an authorized editor may inspect.
func (s *TCPServer) WithIntegrationRoleMembers(ctx context.Context, principal auth.IntegrationPrincipal, query authorization.MemberQuery, deliver func(context.Context, authorization.MemberPage) error) error {
	if deliver == nil {
		return authorization.ErrRoleInvalid
	}
	return s.withIntegrationPolicy(ctx, principal, func(ctx context.Context, e *authorization.RoleEvaluator) error {
		// Check the live pinned policy before entering storage. Storage checks
		// the same revision in its consistent snapshot before returning a page.
		_, err := buildAccessCheck(e, principal.UserID(), netproto.AccessCheck{
			ChannelID: query.ChannelID, ExpectedRevision: query.ExpectedRevision,
			Capability: authorization.ViewChannel,
		})
		if err != nil {
			return err
		}
		roster, ok := s.deps.Roles.(RoleMemberStore)
		if !ok {
			return authorization.ErrRolesNotConfigured
		}
		page, err := roster.RoleMembers(ctx, principal.UserID(), query)
		if err != nil {
			return err
		}
		if page.Revision != query.ExpectedRevision {
			return authorization.ErrRoleConflict
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return deliver(ctx, page)
	})
}
