package main

import (
	"errors"
	"fmt"
	"slices"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

// The member connection belongs to the dedicated Alice account. It must be
// integration-enabled, but begins without assigned roles or ManageRoles.
func checkRoleHierarchy(s *roleScenario, memberQuery *querySession) (retErr error) {
	if err := expectRoleQueryError(memberQuery, "rolelist", nil, 2568); err != nil {
		return err
	}
	manager, err := s.createRole(authorization.Role{Name: "e2e-manager-" + randHex(8), Permissions: []authorization.Capability{authorization.ManageRoles}})
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, s.cleanupRole(manager.ID)) }()
	lower, err := s.createRole(authorization.Role{Name: "e2e-lower-" + randHex(8)})
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, s.cleanupRole(lower.ID)) }()
	if _, err := s.changeRole(authorization.RoleChange{Kind: authorization.MemberRolesSet, UserID: s.alice.UserID, RoleIDs: []int64{manager.ID}}); err != nil {
		return err
	}
	if _, err := s.changeRole(authorization.RoleChange{Kind: authorization.MemberRolesSet, UserID: s.bob.UserID, RoleIDs: []int64{lower.ID}}); err != nil {
		return err
	}
	state, err := e2eQueryJSON[netproto.RoleState](memberQuery, "rolelist", nil)
	if err != nil {
		return err
	}
	if state.ActorID != s.alice.UserID || state.Policy.Revision != s.revision || !slices.Contains(state.ManageableRoleIDs, lower.ID) || slices.Contains(state.ManageableRoleIDs, manager.ID) {
		return errors.New("delegated role hierarchy does not match the test accounts")
	}
	// A delegated manager must fail even though role management itself is granted.
	managerIndex := slices.IndexFunc(state.Policy.Roles, func(r authorization.Role) bool { return r.ID == manager.ID })
	if managerIndex < 0 {
		return errors.New("manager role absent from delegated projection")
	}
	manager = state.Policy.Roles[managerIndex] // creation of the lower role shifted its position
	selfEdit := manager
	selfEdit.Color = "#dd3355"
	adminGrant := lower
	adminGrant.Permissions = []authorization.Capability{authorization.Administrator}
	for _, denied := range []authorization.RoleChange{
		{Kind: authorization.RoleUpdate, Role: selfEdit},
		{Kind: authorization.RoleUpdate, Role: adminGrant},
		{Kind: authorization.MemberRolesSet, UserID: s.ownerID, RoleIDs: []int64{lower.ID}},
		{Kind: authorization.MemberRolesSet, UserID: s.alice.UserID, RoleIDs: []int64{manager.ID, lower.ID}},
	} {
		denied.ExpectedRevision = s.revision
		if err := expectRoleQueryError(memberQuery, "rolechange", denied, 2568); err != nil {
			s.halted = true
			return err
		}
	}
	lower.Color = "#33aa66"
	if _, err := s.changeRoleAs(memberQuery, authorization.RoleChange{Kind: authorization.RoleUpdate, Role: lower}); err != nil {
		return err
	}
	policy, err := s.policy()
	if err != nil {
		return err
	}
	index := slices.IndexFunc(policy.Roles, func(r authorization.Role) bool { return r.ID == lower.ID })
	if index < 0 || policy.Roles[index].Color != lower.Color {
		return errors.New("delegated lower-role edit was not committed")
	}
	// Restore assignments explicitly before deleting resources, and verify that
	// the same still-authenticated connection loses its management authority.
	for _, userID := range []int64{s.bob.UserID, s.alice.UserID} {
		if _, err := s.changeRole(authorization.RoleChange{Kind: authorization.MemberRolesSet, UserID: userID}); err != nil {
			return err
		}
	}
	return expectRoleQueryError(memberQuery, "rolelist", nil, 2568)
}

func (s *roleScenario) cleanupRole(roleID int64) error {
	if s.halted {
		return fmt.Errorf("inspect E2E role %d; cleanup not attempted after an uncertain or pending write", roleID)
	}
	if _, err := s.changeRole(authorization.RoleChange{Kind: authorization.RoleDelete, RoleID: roleID}); err != nil {
		return fmt.Errorf("cleaning E2E role %d: %w", roleID, err)
	}
	return nil
}
