package authorization

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
)

var (
	ErrRoleForbidden      = errors.New("role change forbidden")
	ErrRoleConflict       = errors.New("authorization revision changed")
	ErrRoleInvalid        = errors.New("invalid role change")
	ErrRolesNotConfigured = errors.New("role authorization is not configured")
	ErrRolesInactive      = errors.New("role authorization is not active")
)

type RoleChangeKind string

const (
	RoleCreate           RoleChangeKind = "role_create"
	RoleUpdate           RoleChangeKind = "role_update"
	RoleDelete           RoleChangeKind = "role_delete"
	RolesReorder         RoleChangeKind = "roles_reorder"
	MemberRolesSet       RoleChangeKind = "member_roles_set"
	ChannelAccessSet     RoleChangeKind = "channel_access_set"
	DefaultMemberRoleSet RoleChangeKind = "default_member_role_set"
	OwnerTransfer        RoleChangeKind = "owner_transfer"
)

// RoleChange describes one atomic operation. Actor identity is supplied by the
// authenticated transport, never taken from this request. RoleCreate's ID is
// allocated by the store; role order is lowest first, including @everyone.
type RoleChange struct {
	Kind             RoleChangeKind `json:"kind"`
	ExpectedRevision int64          `json:"expected_revision"`
	Role             Role           `json:"role"`
	RoleID           int64          `json:"role_id"`
	RoleIDs          []int64        `json:"role_ids"`
	UserID           int64          `json:"user_id"`
	Channel          ChannelPolicy  `json:"channel"`
}

// ApplyRoleChange checks all authority against the previous snapshot and returns
// a separate validated snapshot. Persistence must serialize read/check/write
// and commit the audit record together with the new revision.
func ApplyRoleChange(p RolePolicy, actorID int64, change RoleChange) (RolePolicy, error) {
	e, err := NewRoleEvaluator(p)
	if err != nil {
		return RolePolicy{}, fmt.Errorf("invalid current policy: %w", err)
	}
	if change.ExpectedRevision != p.Revision {
		return RolePolicy{}, ErrRoleConflict
	}
	if actorID < 1 {
		return RolePolicy{}, ErrRoleForbidden
	}
	owner := actorID == p.OwnerID
	next := cloneRolePolicy(p)
	if change.Kind != ChannelAccessSet && !e.Evaluate(actorID, 0, ManageRoles).Allowed {
		return RolePolicy{}, ErrRoleForbidden
	}
	switch change.Kind {
	case RoleCreate:
		if change.Role.ID < 1 || len(p.Roles) >= 250 {
			return RolePolicy{}, ErrRoleInvalid
		}
		if _, exists := e.roles[change.Role.ID]; exists {
			return RolePolicy{}, ErrRoleInvalid
		}
		if !owner && e.HighestRole(actorID) == 0 {
			return RolePolicy{}, ErrRoleForbidden
		}
		if !canGrantCapabilities(e, actorID, 0, change.Role.Permissions) {
			return RolePolicy{}, ErrRoleForbidden
		}
		// Insert below all existing assigned roles, preserving their relative order.
		for i := range next.Roles {
			if next.Roles[i].Position > 0 {
				next.Roles[i].Position++
			}
		}
		r := change.Role
		r.Position = 1
		r.Permissions = slices.Clone(r.Permissions)
		next.Roles = append(next.Roles, r)
	case RoleUpdate:
		old, exists := e.roles[change.Role.ID]
		if !exists || change.Role.Position != old.Position {
			return RolePolicy{}, ErrRoleInvalid
		}
		if !e.CanManageRole(actorID, old.ID) {
			return RolePolicy{}, ErrRoleForbidden
		}
		if !canGrantCapabilities(e, actorID, 0, addedCapabilities(old.Permissions, change.Role.Permissions)) {
			return RolePolicy{}, ErrRoleForbidden
		}
		if old.ID == p.EveryoneID && change.Role.Name != "@everyone" {
			return RolePolicy{}, ErrRoleInvalid
		}
		for i := range next.Roles {
			if next.Roles[i].ID == old.ID {
				next.Roles[i] = change.Role
				next.Roles[i].Permissions = slices.Clone(change.Role.Permissions)
			}
		}
	case RoleDelete:
		if change.RoleID == p.EveryoneID {
			return RolePolicy{}, ErrRoleInvalid
		}
		if !e.CanManageRole(actorID, change.RoleID) {
			return RolePolicy{}, ErrRoleForbidden
		}
		next.Roles = slices.DeleteFunc(next.Roles, func(r Role) bool { return r.ID == change.RoleID })
		slices.SortFunc(next.Roles, func(a, b Role) int { return cmp.Compare(a.Position, b.Position) })
		for i := range next.Roles {
			next.Roles[i].Position = i
		}
		for i := range next.Members {
			next.Members[i].RoleIDs = slices.DeleteFunc(next.Members[i].RoleIDs, func(id int64) bool { return id == change.RoleID })
		}
		for i := range next.Channels {
			next.Channels[i].Overrides = slices.DeleteFunc(next.Channels[i].Overrides, func(o RoleOverride) bool { return o.RoleID == change.RoleID })
		}
		if next.DefaultMemberRoleID == change.RoleID {
			next.DefaultMemberRoleID = 0
		}
	case RolesReorder:
		if len(change.RoleIDs) != len(p.Roles) || len(change.RoleIDs) == 0 || change.RoleIDs[0] != p.EveryoneID {
			return RolePolicy{}, ErrRoleInvalid
		}
		seen := map[int64]bool{}
		for position, id := range change.RoleIDs {
			r, exists := e.roles[id]
			if !exists || seen[id] {
				return RolePolicy{}, ErrRoleInvalid
			}
			seen[id] = true
			if !owner && r.Position != position && (!e.CanManageRole(actorID, id) || position >= e.HighestRole(actorID)) {
				return RolePolicy{}, ErrRoleForbidden
			}
			for i := range next.Roles {
				if next.Roles[i].ID == id {
					next.Roles[i].Position = position
				}
			}
		}
	case MemberRolesSet:
		if change.UserID < 1 {
			return RolePolicy{}, ErrRoleInvalid
		}
		if !e.CanManageMember(actorID, change.UserID) {
			return RolePolicy{}, ErrRoleForbidden
		}
		old := e.members[change.UserID]
		for _, id := range append(slices.Clone(old), change.RoleIDs...) {
			if slices.Contains(old, id) == slices.Contains(change.RoleIDs, id) {
				continue
			}
			if !e.CanManageRole(actorID, id) {
				return RolePolicy{}, ErrRoleForbidden
			}
			if slices.Contains(change.RoleIDs, id) && !canGrantCapabilities(e, actorID, 0, e.roles[id].Permissions) {
				return RolePolicy{}, ErrRoleForbidden
			}
		}
		next.Members = slices.DeleteFunc(next.Members, func(m RoleMember) bool { return m.UserID == change.UserID })
		if len(change.RoleIDs) > 0 {
			next.Members = append(next.Members, RoleMember{UserID: change.UserID, RoleIDs: slices.Clone(change.RoleIDs)})
		}
	case OwnerTransfer:
		if !owner {
			return RolePolicy{}, ErrRoleForbidden
		}
		if change.UserID < 1 || change.UserID == p.OwnerID {
			return RolePolicy{}, ErrRoleInvalid
		}
		// Persistence verifies the target is a registered account in the same
		// transaction. Existing role assignments remain unchanged.
		next.OwnerID = change.UserID
	case DefaultMemberRoleSet:
		if !owner {
			return RolePolicy{}, ErrRoleForbidden
		}
		next.DefaultMemberRoleID = change.RoleID
	case ChannelAccessSet:
		old, exists := e.channels[change.Channel.ChannelID]
		if !exists {
			return RolePolicy{}, ErrRoleInvalid
		}
		if old.ParentID > 0 && !e.Evaluate(actorID, old.ParentID, ViewChannel).Allowed {
			// The management projection flattens a hidden parent. Access edits
			// preserve its actual parent; this operation never reparents channels.
			if change.Channel.ParentID != 0 {
				return RolePolicy{}, ErrRoleInvalid
			}
			change.Channel.ParentID = old.ParentID
		} else if old.ParentID != change.Channel.ParentID {
			return RolePolicy{}, ErrRoleInvalid
		}
		if !e.Evaluate(actorID, old.ChannelID, ManageChannelAccess).Allowed {
			return RolePolicy{}, ErrRoleForbidden
		}
		for i := range next.Channels {
			if next.Channels[i].ChannelID == old.ChannelID {
				next.Channels[i] = change.Channel
				next.Channels[i].Overrides = slices.Clone(change.Channel.Overrides)
			}
		}
		// Resolve sync before authorizing changes: switching sync can add or
		// remove inherited grants just as an explicit override edit can.
		candidate, err := NewRoleEvaluator(next)
		if err != nil {
			return RolePolicy{}, fmt.Errorf("%w: %v", ErrRoleInvalid, err)
		}
		if err := authorizeChangedChannelAccess(e, candidate, actorID); err != nil {
			return RolePolicy{}, err
		}
	default:
		return RolePolicy{}, ErrRoleInvalid
	}
	next.Revision++
	slices.SortFunc(next.Roles, func(a, b Role) int { return cmp.Compare(a.Position, b.Position) })
	if _, err := NewRoleEvaluator(next); err != nil {
		return RolePolicy{}, fmt.Errorf("%w: %v", ErrRoleInvalid, err)
	}
	return next, nil
}

func canGrantCapabilities(e *RoleEvaluator, actorID, channelID int64, capabilities []Capability) bool {
	for _, c := range capabilities {
		if c == Administrator && actorID != e.policy.OwnerID {
			return false
		}
		if !e.Evaluate(actorID, channelID, c).Allowed {
			return false
		}
	}
	return true
}

// A parent edit also changes its synced descendants. Check every changed
// effective policy against the actor's pre-change authority, including cases
// where the edited parent itself is allowed but a descendant is not.
func authorizeChangedChannelAccess(before, after *RoleEvaluator, actorID int64) error {
	if actorID == before.policy.OwnerID {
		return nil
	}
	for id := range after.channels {
		oldOverrides, newOverrides := before.effectiveOverrides(id), after.effectiveOverrides(id)
		if err := authorizeOverrideChanges(before, actorID, id, oldOverrides, newOverrides); err != nil {
			return err
		}
	}
	return nil
}

func authorizeOverrideChanges(before *RoleEvaluator, actorID, channelID int64, oldOverrides, newOverrides []RoleOverride) error {
	if actorID == before.policy.OwnerID {
		return nil
	}
	for _, override := range append(slices.Clone(oldOverrides), newOverrides...) {
		if slices.Contains(oldOverrides, override) && slices.Contains(newOverrides, override) {
			continue
		}
		if !before.Evaluate(actorID, channelID, ManageChannelAccess).Allowed || !before.Evaluate(actorID, channelID, override.Capability).Allowed {
			return ErrRoleForbidden
		}
		if override.RoleID != 0 && !before.CanManageChannelRole(actorID, channelID, override.RoleID) {
			return ErrRoleForbidden
		}
		if override.UserID != 0 && !before.CanManageMember(actorID, override.UserID) {
			return ErrRoleForbidden
		}
	}
	return nil
}

func addedCapabilities(before, after []Capability) []Capability {
	var added []Capability
	for _, c := range after {
		if !slices.Contains(before, c) {
			added = append(added, c)
		}
	}
	return added
}

func (e *RoleEvaluator) effectiveOverrides(channelID int64) []RoleOverride {
	ch := e.channels[channelID]
	for ch.Synced && ch.ParentID != 0 {
		ch = e.channels[ch.ParentID]
	}
	return ch.Overrides
}

func cloneRolePolicy(p RolePolicy) RolePolicy {
	p.Roles = slices.Clone(p.Roles)
	for i := range p.Roles {
		p.Roles[i].Permissions = slices.Clone(p.Roles[i].Permissions)
	}
	p.Members = slices.Clone(p.Members)
	for i := range p.Members {
		p.Members[i].RoleIDs = slices.Clone(p.Members[i].RoleIDs)
	}
	p.Channels = slices.Clone(p.Channels)
	for i := range p.Channels {
		p.Channels[i].Overrides = slices.Clone(p.Channels[i].Overrides)
	}
	return p
}
