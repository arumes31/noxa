package authorization

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Role grants server-wide capabilities; unset never negates another role.
type Role struct {
	ID          int64        `json:"id"`
	Name        string       `json:"name"`
	Position    int          `json:"position"`
	Color       string       `json:"color"`
	Icon        string       `json:"icon"`
	Hoist       bool         `json:"hoist"`
	Mentionable bool         `json:"mentionable"`
	Permissions []Capability `json:"permissions"`
}

type RoleMember struct {
	UserID  int64   `json:"user_id"`
	RoleIDs []int64 `json:"role_ids"`
}

type OverrideEffect string

const (
	Allow OverrideEffect = "allow"
	Deny  OverrideEffect = "deny"
)

// RoleOverride has exactly one subject. Absence represents Inherit.
type RoleOverride struct {
	RoleID     int64          `json:"role_id,omitempty"`
	UserID     int64          `json:"user_id,omitempty"`
	Capability Capability     `json:"capability"`
	Effect     OverrideEffect `json:"effect"`
}

type ChannelPolicy struct {
	ChannelID int64          `json:"channel_id"`
	ParentID  int64          `json:"parent_id"`
	Synced    bool           `json:"synced"`
	Overrides []RoleOverride `json:"overrides"`
}

// RolePolicy is a consistent committed authorization snapshot. User IDs are
// authenticated database IDs; zero is the anonymous guest and never an owner.
type RolePolicy struct {
	Revision            int64           `json:"revision"`
	OwnerID             int64           `json:"owner_id"`
	EveryoneID          int64           `json:"everyone_id"`
	DefaultMemberRoleID int64           `json:"default_member_role_id"`
	Roles               []Role          `json:"roles"`
	Members             []RoleMember    `json:"members"`
	Channels            []ChannelPolicy `json:"channels"`
}

type RoleDecision struct {
	Allowed     bool       `json:"allowed"`
	Reason      string     `json:"reason"`
	RoleIDs     []int64    `json:"role_ids,omitempty"`
	ChannelID   int64      `json:"channel_id,omitempty"`
	Requirement Capability `json:"requirement,omitempty"`
	Revision    int64      `json:"revision"`
}

// RoleEvaluator holds a validated, private snapshot and is safe for concurrent reads.
type RoleEvaluator struct {
	policy   RolePolicy
	roles    map[int64]Role
	members  map[int64][]int64
	channels map[int64]ChannelPolicy
}

var roleColorPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

func NewRoleEvaluator(p RolePolicy) (*RoleEvaluator, error) {
	if p.Revision < 1 || p.OwnerID < 1 || p.EveryoneID < 1 {
		return nil, fmt.Errorf("invalid authorization configuration")
	}
	e := &RoleEvaluator{policy: RolePolicy{Revision: p.Revision, OwnerID: p.OwnerID, EveryoneID: p.EveryoneID}, roles: map[int64]Role{}, members: map[int64][]int64{}, channels: map[int64]ChannelPolicy{}}
	positions := map[int]bool{}
	for _, r := range p.Roles {
		if r.ID < 1 || strings.TrimSpace(r.Name) == "" || len(r.Name) > 100 || r.Position < 0 || positions[r.Position] ||
			!utf8.ValidString(r.Name) || strings.ContainsFunc(r.Name, unicode.IsControl) ||
			(r.Color != "" && !roleColorPattern.MatchString(r.Color)) ||
			!utf8.ValidString(r.Icon) || utf8.RuneCountInString(r.Icon) > 16 || strings.ContainsFunc(r.Icon, unicode.IsControl) {
			return nil, fmt.Errorf("invalid role %d", r.ID)
		}
		if _, exists := e.roles[r.ID]; exists {
			return nil, fmt.Errorf("duplicate role %d", r.ID)
		}
		positions[r.Position] = true
		seen := map[Capability]bool{}
		for _, c := range r.Permissions {
			if _, ok := capabilityInfo(c); !ok || seen[c] {
				return nil, fmt.Errorf("invalid role capability %q", c)
			}
			seen[c] = true
		}
		r.Permissions = slices.Clone(r.Permissions)
		e.roles[r.ID] = r
	}
	if r, ok := e.roles[p.EveryoneID]; !ok || r.Position != 0 || slices.Contains(r.Permissions, Administrator) {
		return nil, fmt.Errorf("invalid everyone role")
	}
	if p.DefaultMemberRoleID != 0 {
		r, ok := e.roles[p.DefaultMemberRoleID]
		if !ok || r.ID == p.EveryoneID || slices.Contains(r.Permissions, Administrator) {
			return nil, fmt.Errorf("invalid default member role")
		}
	}
	for _, m := range p.Members {
		if m.UserID < 1 {
			return nil, fmt.Errorf("invalid member")
		}
		if _, exists := e.members[m.UserID]; exists {
			return nil, fmt.Errorf("duplicate member")
		}
		seen := map[int64]bool{}
		for _, id := range m.RoleIDs {
			if _, ok := e.roles[id]; !ok || seen[id] || id == p.EveryoneID {
				return nil, fmt.Errorf("invalid member role")
			}
			seen[id] = true
		}
		e.members[m.UserID] = slices.Clone(m.RoleIDs)
	}
	for _, ch := range p.Channels {
		if ch.ChannelID < 1 || ch.ParentID < 0 || (ch.Synced && len(ch.Overrides) > 0) {
			return nil, fmt.Errorf("invalid channel policy")
		}
		if _, exists := e.channels[ch.ChannelID]; exists {
			return nil, fmt.Errorf("duplicate channel policy")
		}
		seen := map[string]bool{}
		for _, o := range ch.Overrides {
			info, ok := capabilityInfo(o.Capability)
			key := fmt.Sprintf("%d:%d:%s", o.RoleID, o.UserID, o.Capability)
			if !ok || !info.Channel || (o.RoleID > 0) == (o.UserID > 0) || o.RoleID < 0 || o.UserID < 0 || (o.Effect != Allow && o.Effect != Deny) || seen[key] {
				return nil, fmt.Errorf("invalid channel override")
			}
			if o.RoleID > 0 {
				if _, ok := e.roles[o.RoleID]; !ok {
					return nil, fmt.Errorf("unknown override role")
				}
			}
			seen[key] = true
		}
		ch.Overrides = slices.Clone(ch.Overrides)
		e.channels[ch.ChannelID] = ch
	}
	for id := range e.channels {
		visited := map[int64]bool{}
		for id != 0 {
			ch, ok := e.channels[id]
			if !ok || visited[id] {
				return nil, fmt.Errorf("invalid channel parent chain")
			}
			visited[id] = true
			id = ch.ParentID
		}
	}
	e.policy = cloneRolePolicy(p)
	return e, nil
}

// Policy returns an isolated copy of the revision held by this evaluator.
func (e *RoleEvaluator) Policy() RolePolicy { return cloneRolePolicy(e.policy) }

// Evaluate resolves capabilities only. Session, moderation, resource limits and
// target-member hierarchy remain mandatory action-level checks.
func (e *RoleEvaluator) Evaluate(userID, channelID int64, capability Capability) RoleDecision {
	d := RoleDecision{Revision: e.policy.Revision, Reason: "not_granted"}
	info, ok := capabilityInfo(capability)
	if !ok || userID < 0 || channelID < 0 {
		d.Reason = "invalid_context"
		return d
	}
	if channelID > 0 && !info.Channel {
		d.Reason = "invalid_scope"
		return d
	}
	if channelID != 0 {
		if _, ok := e.channels[channelID]; !ok {
			d.Reason = "unknown_channel"
			return d
		}
	}
	if userID == e.policy.OwnerID {
		d.Allowed = true
		d.Reason = "owner"
		return d
	}
	ids := append([]int64{e.policy.EveryoneID}, e.members[userID]...)
	for _, id := range ids {
		r := e.roles[id]
		if slices.Contains(r.Permissions, Administrator) {
			d.Allowed = true
			d.Reason = "administrator"
			d.RoleIDs = []int64{id}
			return d
		}
		if slices.Contains(r.Permissions, capability) {
			d.Allowed = true
			d.Reason = "role_grant"
			d.RoleIDs = append(d.RoleIDs, id)
		}
	}
	if channelID > 0 && info.Channel {
		ch := e.channels[channelID]
		for ch.Synced && ch.ParentID != 0 {
			ch = e.channels[ch.ParentID]
		}
		for _, o := range ch.Overrides {
			if o.Capability == capability && o.RoleID == e.policy.EveryoneID {
				d = RoleDecision{Allowed: o.Effect == Allow, Reason: "everyone_override", RoleIDs: []int64{o.RoleID}, ChannelID: ch.ChannelID, Revision: e.policy.Revision}
			}
		}
		var allows, denies []int64
		for _, o := range ch.Overrides {
			if o.Capability != capability || o.RoleID == e.policy.EveryoneID || !slices.Contains(e.members[userID], o.RoleID) {
				continue
			}
			if o.Effect == Allow {
				allows = append(allows, o.RoleID)
			} else {
				denies = append(denies, o.RoleID)
			}
		}
		if len(denies) > 0 {
			d = RoleDecision{Reason: "role_override", RoleIDs: denies, ChannelID: ch.ChannelID, Revision: e.policy.Revision}
		}
		if len(allows) > 0 {
			d = RoleDecision{Allowed: true, Reason: "role_override", RoleIDs: allows, ChannelID: ch.ChannelID, Revision: e.policy.Revision}
		}
		for _, o := range ch.Overrides {
			if userID > 0 && o.UserID == userID && o.Capability == capability {
				d = RoleDecision{Allowed: o.Effect == Allow, Reason: "member_override", ChannelID: ch.ChannelID, Revision: e.policy.Revision}
			}
		}
	}
	slices.Sort(d.RoleIDs)
	if d.Allowed {
		for _, required := range info.Requires {
			if prerequisite := e.Evaluate(userID, channelID, required); !prerequisite.Allowed {
				prerequisite.Reason = "requires"
				prerequisite.Requirement = required
				return prerequisite
			}
		}
	}
	return d
}

func (e *RoleEvaluator) HighestRole(userID int64) int {
	position := 0
	for _, id := range e.members[userID] {
		if r := e.roles[id]; r.Position > position {
			position = r.Position
		}
	}
	return position
}

// EffectiveOverrides returns a copy for the channel access editor. The caller
// must separately authorize access to this policy; it grants no content access.
func (e *RoleEvaluator) EffectiveOverrides(channelID int64) []RoleOverride {
	return slices.Clone(e.effectiveOverrides(channelID))
}

// CanManageMember is the hierarchy guard, not an action permission grant.
func (e *RoleEvaluator) CanManageMember(actorID, targetID int64) bool {
	if actorID < 1 || targetID < 0 || actorID == targetID || targetID == e.policy.OwnerID {
		return false
	}
	return actorID == e.policy.OwnerID || e.HighestRole(actorID) > e.HighestRole(targetID)
}

func (e *RoleEvaluator) CanManageRole(actorID, roleID int64) bool {
	r, ok := e.roles[roleID]
	if !ok || actorID < 1 {
		return false
	}
	if actorID == e.policy.OwnerID {
		return true
	}
	return roleID != e.policy.EveryoneID && e.Evaluate(actorID, 0, ManageRoles).Allowed && e.HighestRole(actorID) > r.Position && !slices.Contains(r.Permissions, Administrator)
}

// CanManageChannelRole authorizes only a role override in the specified channel.
// It does not permit modifying the role itself or assigning it to a member.
// Scope zero is the server baseline used when preparing a new root channel.
func (e *RoleEvaluator) CanManageChannelRole(actorID, channelID, roleID int64) bool {
	if actorID < 1 || channelID < 0 || !e.Evaluate(actorID, channelID, ManageChannelAccess).Allowed {
		return false
	}
	r, exists := e.roles[roleID]
	if !exists {
		return false
	}
	if actorID == e.policy.OwnerID {
		return true
	}
	if slices.Contains(r.Permissions, Administrator) {
		return false
	}
	return roleID == e.policy.EveryoneID || e.HighestRole(actorID) > r.Position
}
