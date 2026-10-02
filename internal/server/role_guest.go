package server

import (
	"context"

	"noxa/internal/authorization"
)

type roleGuestStore interface {
	AssignGuestRoles(context.Context, int64, string, string, string, authorization.RoleChange) (authorization.RolePolicy, int64, error)
}

func (s *TCPServer) roleGuest(uniqueID string) (*Client, bool) {
	client, ok := s.clientByUniqueID(uniqueID)
	if !ok {
		return nil, false
	}
	client.mu.RLock()
	defer client.mu.RUnlock()
	return client, client.authed && !client.revoked && client.UserID == 0 && client.verifiedGuestKey != ""
}

func (s *TCPServer) assignGuestRoles(ctx context.Context, actor *Client, change authorization.RoleChange, validate func(context.Context) error) (authorization.RolePolicy, error) {
	backend, ok := s.deps.Roles.(roleGuestStore)
	if !ok || change.Kind != authorization.MemberRolesSet || change.UserID != 0 || len(change.RoleIDs) == 0 {
		return authorization.RolePolicy{}, authorization.ErrRoleInvalid
	}
	return s.deps.Authority.ChangeLifecyclePolicyValidated(ctx, change.ExpectedRevision, validate, func(ctx context.Context) (authorization.RolePolicy, error) {
		s.roleMetadataMu.Lock()
		defer s.roleMetadataMu.Unlock()
		target, ok := s.roleGuest(change.MemberUniqueID)
		if !ok {
			return authorization.RolePolicy{}, authorization.ErrRoleInvalid
		}
		target.mu.RLock()
		publicKey, nickname := target.verifiedGuestKey, target.Username
		target.mu.RUnlock()
		policy, userID, err := backend.AssignGuestRoles(ctx, actor.userID(), change.MemberUniqueID, publicKey, nickname, change)
		if err != nil {
			return authorization.RolePolicy{}, err
		}
		// Publish the new account ID before ordinary role reconciliation runs.
		// The exclusive policy gate also blocks new logins and protected effects.
		for _, member := range s.deps.State.ListClients() {
			if member.UniqueID != change.MemberUniqueID || member.UserID != 0 {
				continue
			}
			if session, ok := s.clientByID(member.ClientID); ok && session.isAuthed() {
				session.setIdentity(member.UniqueID, session.nickname(), userID, false)
				s.deps.State.EnrollClient(member.ClientID, member.UniqueID, userID)
			}
		}
		return policy, nil
	})
}
