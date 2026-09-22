package server

import (
	"context"
	"errors"
	"time"

	"noxa/internal/authorization"
	"noxa/internal/store"
)

type roleLeaseKey struct{}
type roleLease struct {
	authority *authorization.Authority
	evaluator *authorization.RoleEvaluator
}

// withRoleAccess holds the policy revision through one bounded protected effect.
// Nested checks share that lease: reacquiring an RWMutex reader while a writer
// is pending would deadlock. The callback context must not escape the callback
// or be used by background work.
func (s *TCPServer) withRoleAccess(ctx context.Context, client *Client, channelID int64, capability authorization.Capability, effect func(context.Context) error) error {
	return s.withRolePolicy(ctx, func(ctx context.Context) error {
		if lease, ok := ctx.Value(roleLeaseKey{}).(roleLease); ok && s.deps != nil && lease.authority == s.deps.Authority {
			if client.sessionRevoked() || !lease.evaluator.Evaluate(client.userID(), channelID, capability).Allowed {
				return authorization.ErrRoleForbidden
			}
		}
		return effect(ctx)
	})
}

func (s *TCPServer) withRolePolicy(ctx context.Context, effect func(context.Context) error) error {
	if s.deps == nil || s.deps.Authority == nil {
		return authorization.ErrAuthorizationUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if lease, ok := ctx.Value(roleLeaseKey{}).(roleLease); ok && lease.authority == s.deps.Authority {
		return effect(ctx)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return s.deps.Authority.WithPolicy(ctx, func(e *authorization.RoleEvaluator) error {
		return effect(context.WithValue(ctx, roleLeaseKey{}, roleLease{s.deps.Authority, e}))
	})
}

func (s *TCPServer) roleAction(ctx context.Context, client *Client, channelID int64, capability authorization.Capability, effect func(context.Context) error) error {
	err := s.withRoleAccess(ctx, client, channelID, capability, effect)
	if errors.Is(err, authorization.ErrRoleForbidden) || errors.Is(err, authorization.ErrAuthorizationUnavailable) {
		return s.roleError(ctx, client, err)
	}
	return err
}

func (s *TCPServer) roleAllowed(ctx context.Context, client *Client, channelID int64, capability authorization.Capability) bool {
	return s.withRoleAccess(ctx, client, channelID, capability, func(context.Context) error { return nil }) == nil
}

func (s *TCPServer) withRoleSession(ctx context.Context, client *Client, effect func(context.Context) error) error {
	return s.withRolePolicy(ctx, func(ctx context.Context) error {
		if s.deps != nil && s.deps.Authority != nil && client.sessionRevoked() {
			return authorization.ErrRoleForbidden
		}
		return effect(ctx)
	})
}

func (s *TCPServer) rolePolicyRead(ctx context.Context, client *Client, effect func(context.Context) error) error {
	err := s.withRoleSession(ctx, client, effect)
	if errors.Is(err, authorization.ErrAuthorizationUnavailable) || errors.Is(err, authorization.ErrRoleForbidden) {
		return s.roleError(ctx, client, err)
	}
	return err
}

// Message IDs do not disclose a channel until access to that message's actual
// scope is established. Scope and author are immutable message attributes.
func (s *TCPServer) messageRoleAction(ctx context.Context, client *Client, message *store.ChatMessage, capability authorization.Capability, effect func(context.Context) error) error {
	if message == nil || message.DeletedAt != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeNotFound, "message not found")
	}
	err := s.withRoleAccess(ctx, client, message.ChannelID, capability, effect)
	if errors.Is(err, authorization.ErrRoleForbidden) {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeNotFound, "message not found")
	}
	if errors.Is(err, authorization.ErrAuthorizationUnavailable) {
		return s.roleError(ctx, client, err)
	}
	return err
}
