package main

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

// roleScenario owns only resources created by this run.
// Use dedicated unassigned test accounts on an isolated server.
type roleScenario struct {
	query      *querySession
	revision   int64
	ownerID    int64
	alice      authorization.MemberIdentity
	bob        authorization.MemberIdentity
	halted     bool
	roleIDs    map[int64]bool
	channelIDs map[int64]bool
}

func prepareRoleScenario(q *querySession, aliceUID, bobUID string) (*roleScenario, error) {
	if aliceUID == "" || bobUID == "" || aliceUID == bobUID {
		return nil, errors.New("role scenarios require two distinct test account unique IDs")
	}
	state, err := e2eQueryJSON[netproto.RoleState](q, "rolelist", nil)
	if err != nil {
		return nil, err
	}
	if state.ActorID <= 0 || state.ActorID != state.Policy.OwnerID {
		return nil, errors.New("role scenarios require the actual server owner as the Query actor")
	}
	evaluator, err := authorization.NewRoleEvaluator(state.Policy)
	if err != nil {
		return nil, fmt.Errorf("invalid owner policy: %w", err)
	}
	s := &roleScenario{query: q, revision: state.Policy.Revision, ownerID: state.ActorID, roleIDs: map[int64]bool{}, channelIDs: map[int64]bool{}}
	for _, role := range state.Policy.Roles {
		s.roleIDs[role.ID] = true
	}
	for _, channel := range state.Policy.Channels {
		s.channelIDs[channel.ChannelID] = true
	}
	for _, target := range []struct {
		uid    string
		member *authorization.MemberIdentity
	}{{aliceUID, &s.alice}, {bobUID, &s.bob}} {
		member, err := s.member(target.uid)
		if err != nil {
			return nil, err
		}
		if member.UserID == s.ownerID || !member.Manageable || len(member.RoleIDs) != 0 || evaluator.Evaluate(member.UserID, 0, authorization.ManageRoles).Allowed {
			return nil, errors.New("role scenarios require unassigned non-owner test accounts without role-management authority")
		}
		*target.member = member
	}
	if s.alice.UserID == s.bob.UserID {
		return nil, errors.New("test unique IDs resolve to the same account")
	}
	return s, nil
}

func (s *roleScenario) member(uid string) (authorization.MemberIdentity, error) {
	page, err := e2eQueryJSON[authorization.MemberPage](s.query, "rolemembers", authorization.MemberQuery{ExpectedRevision: s.revision, Search: uid})
	if err != nil {
		return authorization.MemberIdentity{}, err
	}
	if page.Revision != s.revision || page.More {
		return authorization.MemberIdentity{}, errors.New("member lookup is stale or incomplete")
	}
	var result authorization.MemberIdentity
	for _, entry := range page.Entries {
		if entry.UniqueID == uid {
			if result.UserID != 0 || entry.UserID <= 0 {
				return result, errors.New("member lookup is ambiguous")
			}
			result = entry
		}
	}
	if result.UserID == 0 {
		return result, errors.New("exact test account unique ID was not found")
	}
	return result, nil
}

func (s *roleScenario) policy() (authorization.RolePolicy, error) {
	state, err := e2eQueryJSON[netproto.RoleState](s.query, "rolelist", nil)
	if err != nil {
		return authorization.RolePolicy{}, err
	}
	if state.ActorID != s.ownerID || state.Policy.OwnerID != s.ownerID || state.Policy.Revision != s.revision {
		return authorization.RolePolicy{}, errors.New("owner or policy revision changed during the scenario")
	}
	return state.Policy, nil
}

func (s *roleScenario) changeRole(change authorization.RoleChange) (netproto.RoleChangeResult, error) {
	return s.changeRoleAs(s.query, change)
}

func (s *roleScenario) changeRoleAs(q *querySession, change authorization.RoleChange) (netproto.RoleChangeResult, error) {
	if s.halted {
		return netproto.RoleChangeResult{}, errors.New("scenario stopped after an uncertain or pending write; inspect its test resources before another run")
	}
	change.ExpectedRevision = s.revision
	s.halted = true // a lost acknowledgement must never become a blind retry
	result, err := e2eQueryJSON[netproto.RoleChangeResult](q, "rolechange", change)
	if err != nil {
		return result, err
	}
	if result.Revision != s.revision+1 || (change.Kind == authorization.RoleCreate && (result.CreatedRoleID <= 0 || s.roleIDs[result.CreatedRoleID])) {
		return result, errors.New("invalid committed role acknowledgement")
	}
	if change.Kind == authorization.RoleCreate {
		if s.roleIDs == nil {
			s.roleIDs = map[int64]bool{}
		}
		s.roleIDs[result.CreatedRoleID] = true
	}
	s.revision = result.Revision
	if result.EnforcementPending {
		return result, fmt.Errorf("role change saved at revision %d with enforcement pending (created role %d)", result.Revision, result.CreatedRoleID)
	}
	s.halted = false
	return result, nil
}

func (s *roleScenario) createRole(role authorization.Role) (authorization.Role, error) {
	created, err := s.changeRole(authorization.RoleChange{Kind: authorization.RoleCreate, Role: role})
	if err != nil {
		return authorization.Role{}, err
	}
	s.halted = true
	policy, err := s.policy()
	if err != nil {
		return authorization.Role{}, fmt.Errorf("verify new E2E role %d before cleanup: %w", created.CreatedRoleID, err)
	}
	index := slices.IndexFunc(policy.Roles, func(r authorization.Role) bool { return r.ID == created.CreatedRoleID })
	if index < 0 {
		return authorization.Role{}, fmt.Errorf("new E2E role %d absent from committed policy; cleanup not attempted", created.CreatedRoleID)
	}
	got := policy.Roles[index]
	if got.Name != role.Name || got.Color != role.Color || got.Icon != role.Icon || got.Hoist != role.Hoist || !slices.Equal(got.Permissions, role.Permissions) {
		return authorization.Role{}, fmt.Errorf("new E2E role %d does not match creation; cleanup not attempted", created.CreatedRoleID)
	}
	s.halted = false
	return got, nil
}

func (s *roleScenario) changeChannel(change netproto.RoleChannelChange) (netproto.RoleChannelResult, error) {
	if s.halted {
		return netproto.RoleChannelResult{}, errors.New("scenario stopped after an uncertain or pending write; inspect its test resources before another run")
	}
	change.ExpectedRevision = s.revision
	s.halted = true
	result, err := e2eQueryJSON[netproto.RoleChannelResult](s.query, "channelchange", change)
	if err != nil {
		return result, err
	}
	if result.Revision != s.revision+1 || result.ChannelID <= 0 || (change.Kind == authorization.ChannelCreate && s.channelIDs[result.ChannelID]) || (change.Kind != authorization.ChannelCreate && result.ChannelID != change.ChannelID) {
		return result, errors.New("invalid committed channel acknowledgement")
	}
	if change.Kind == authorization.ChannelCreate {
		if s.channelIDs == nil {
			s.channelIDs = map[int64]bool{}
		}
		s.channelIDs[result.ChannelID] = true
	}
	s.revision = result.Revision
	if result.EnforcementPending {
		return result, fmt.Errorf("channel %d saved at revision %d with enforcement pending", result.ChannelID, result.Revision)
	}
	s.halted = false
	return result, nil
}

func (s *roleScenario) expectAccess(userID, channelID int64, capability authorization.Capability, allowed bool) error {
	result, err := e2eQueryJSON[netproto.AccessCheckResult](s.query, "accesscheck", netproto.AccessCheck{UserID: userID, ChannelID: channelID, Capability: capability, ExpectedRevision: s.revision})
	if err != nil {
		return err
	}
	if result.Decision.Revision != s.revision || result.Decision.Allowed != allowed {
		return fmt.Errorf("unexpected %s decision for member %d in channel %d at revision %d", capability, userID, channelID, s.revision)
	}
	return nil
}

func checkRoleLifecycle(s *roleScenario) (retErr error) {
	name := "e2e-role-" + randHex(8)
	before := s.revision
	role, err := s.createRole(authorization.Role{Name: name})
	if err != nil {
		return err
	}
	roleID := role.ID
	defer func() {
		if roleID == 0 {
			return
		}
		if s.halted {
			retErr = errors.Join(retErr, fmt.Errorf("inspect E2E role %d; cleanup was not attempted after an uncertain or pending write", roleID))
			return
		}
		_, err := s.changeRole(authorization.RoleChange{Kind: authorization.RoleDelete, RoleID: roleID})
		if err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("cleaning E2E role %d: %w", roleID, err))
		}
	}()
	role.Name += " updated"
	role.Color = "#5588cc"
	stale := authorization.RoleChange{Kind: authorization.RoleUpdate, ExpectedRevision: before, Role: role}
	if err := expectRoleQueryError(s.query, "rolechange", stale, 521); err != nil {
		s.halted = true
		return err
	}
	if _, err := s.changeRole(authorization.RoleChange{Kind: authorization.RoleUpdate, Role: role}); err != nil {
		return err
	}
	policy, err := s.policy()
	if err != nil {
		return err
	}
	index := slices.IndexFunc(policy.Roles, func(r authorization.Role) bool { return r.ID == roleID })
	if index < 0 || policy.Roles[index].Name != role.Name || policy.Roles[index].Color != role.Color {
		return errors.New("role edit was not saved")
	}
	if _, err := s.changeRole(authorization.RoleChange{Kind: authorization.MemberRolesSet, UserID: s.bob.UserID, RoleIDs: []int64{roleID}}); err != nil {
		return err
	}
	member, err := s.member(s.bob.UniqueID)
	if err != nil {
		return err
	}
	if !slices.Equal(member.RoleIDs, []int64{roleID}) {
		return errors.New("role assignment was not saved")
	}
	if _, err := s.changeRole(authorization.RoleChange{Kind: authorization.MemberRolesSet, UserID: s.bob.UserID}); err != nil {
		return err
	}
	member, err = s.member(s.bob.UniqueID)
	if err != nil {
		return err
	}
	if len(member.RoleIDs) != 0 {
		return errors.New("role removal was not saved")
	}
	if _, err := s.changeRole(authorization.RoleChange{Kind: authorization.RoleDelete, RoleID: roleID}); err != nil {
		return err
	}
	deletedID := roleID
	roleID = 0
	policy, err = s.policy()
	if err != nil {
		return err
	}
	if slices.ContainsFunc(policy.Roles, func(r authorization.Role) bool { return r.ID == deletedID }) {
		return errors.New("deleted role remains in committed policy")
	}
	return nil
}

func expectRoleQueryError(q *querySession, command string, request any, code int) error {
	// Use the same encoder as successful requests; only the response shape differs.
	payload, err := e2eQueryCommand(command, request)
	if err != nil {
		return err
	}
	lines, err := q.cmd(payload)
	if err != nil {
		return err
	}
	if len(lines) != 1 || !strings.HasPrefix(lines[0], fmt.Sprintf("error id=%d msg=", code)) {
		return fmt.Errorf("%s did not reject with expected error %d", command, code)
	}
	return nil
}

func checkRoleChannelLifecycle(s *roleScenario) (retErr error) {
	policy, err := s.policy()
	if err != nil {
		return err
	}
	state, err := e2eQueryJSON[netproto.RoleChannelState](s.query, "channelquery", netproto.RoleChannelQuery{Kind: authorization.ChannelCreate})
	if err != nil {
		return err
	}
	if state.Revision != s.revision || !state.CanCreatePermanent || !state.CanManageAccess || state.EveryoneID != policy.EveryoneID {
		return errors.New("channel preflight does not match owner policy")
	}
	settings := state.Settings
	settings.Name = "e2e-channel-" + randHex(8)
	overrides := []authorization.RoleOverride{}
	for _, capability := range []authorization.Capability{authorization.ViewChannel, authorization.Connect, authorization.ReadHistory, authorization.SendMessages} {
		overrides = append(overrides, authorization.RoleOverride{RoleID: policy.EveryoneID, Capability: capability, Effect: authorization.Allow})
	}
	created, err := s.changeChannel(netproto.RoleChannelChange{Kind: authorization.ChannelCreate, ChannelType: 2, Settings: &settings, Access: &netproto.RoleChannelAccess{Overrides: overrides}})
	if err != nil {
		return err
	}
	channelID := created.ChannelID
	s.halted = true // an acknowledgement alone does not establish ownership
	defer func() {
		if s.halted {
			retErr = errors.Join(retErr, fmt.Errorf("inspect E2E channel %d; cleanup was not attempted after an uncertain or pending write", channelID))
			return
		}
		if _, err := s.changeChannel(netproto.RoleChannelChange{Kind: authorization.ChannelDelete, ChannelID: channelID}); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("cleaning E2E channel %d: %w", channelID, err))
			return
		}
		policy, err := s.policy()
		if err != nil {
			retErr = errors.Join(retErr, err)
			return
		}
		if slices.ContainsFunc(policy.Channels, func(c authorization.ChannelPolicy) bool { return c.ChannelID == channelID }) {
			retErr = errors.Join(retErr, errors.New("deleted channel remains in committed policy"))
		}
	}()
	state, err = e2eQueryJSON[netproto.RoleChannelState](s.query, "channelquery", netproto.RoleChannelQuery{Kind: authorization.ChannelEdit, ChannelID: channelID})
	if err != nil {
		return err
	}
	if state.Revision != s.revision || state.ChannelID != channelID || state.Settings.Name != settings.Name {
		return errors.New("created channel settings were not saved")
	}
	s.halted = false
	settings = state.Settings
	settings.SlowModeSeconds = 3
	if _, err := s.changeChannel(netproto.RoleChannelChange{Kind: authorization.ChannelEdit, ChannelID: channelID, Settings: &settings}); err != nil {
		return err
	}
	state, err = e2eQueryJSON[netproto.RoleChannelState](s.query, "channelquery", netproto.RoleChannelQuery{Kind: authorization.ChannelEdit, ChannelID: channelID})
	if err != nil {
		return err
	}
	if state.Revision != s.revision || state.ChannelID != channelID || state.Settings.SlowModeSeconds != 3 {
		return errors.New("channel edit was not saved")
	}
	if err := s.expectAccess(0, channelID, authorization.SendMessages, true); err != nil {
		return err
	}
	for i := range overrides {
		if overrides[i].Capability == authorization.SendMessages {
			overrides[i].Effect = authorization.Deny
		}
	}
	access := authorization.ChannelPolicy{ChannelID: channelID, Overrides: overrides}
	if _, err := s.changeRole(authorization.RoleChange{Kind: authorization.ChannelAccessSet, Channel: access}); err != nil {
		return err
	}
	if err := s.expectAccess(0, channelID, authorization.SendMessages, false); err != nil {
		return err
	}
	if err := s.expectAccess(s.bob.UserID, channelID, authorization.SendMessages, false); err != nil {
		return err
	}
	access.Overrides = append(access.Overrides, authorization.RoleOverride{UserID: s.bob.UserID, Capability: authorization.SendMessages, Effect: authorization.Allow})
	if _, err := s.changeRole(authorization.RoleChange{Kind: authorization.ChannelAccessSet, Channel: access}); err != nil {
		return err
	}
	if err := s.expectAccess(s.bob.UserID, channelID, authorization.SendMessages, true); err != nil {
		return err
	}
	return s.expectAccess(0, channelID, authorization.SendMessages, false)
}
