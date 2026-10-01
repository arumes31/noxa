package authorization

import (
	"fmt"
	"slices"
)

type ChannelChangeKind string

const (
	ChannelCreate ChannelChangeKind = "channel_create"
	ChannelMove   ChannelChangeKind = "channel_move"
	ChannelDelete ChannelChangeKind = "channel_delete"
	ChannelEdit   ChannelChangeKind = "channel_edit"
)

// ChannelTreeChange describes only a policy/tree transition. It is deliberately
// separate from RoleChange: a channel lifecycle transaction must commit the
// resource row, policy, audit and revision together. Passwords and other channel
// settings never belong in this object or its audit representation.
// ChannelCreate uses a store-allocated ID and an explicit initial access policy.
// ChannelMove defaults to keeping effective access; SyncToParent is opt-in.
type ChannelTreeChange struct {
	Kind             ChannelChangeKind `json:"kind"`
	ExpectedRevision int64             `json:"expected_revision"`
	ChannelID        int64             `json:"channel_id"`
	ParentID         int64             `json:"parent_id"`
	Temporary        bool              `json:"temporary"`
	Access           ChannelPolicy     `json:"access"`
	SyncToParent     bool              `json:"sync_to_parent"`
	// OrderIndex optionally changes placement in the same move transaction.
	OrderIndex *int32 `json:"order_index,omitempty"`
}

// ApplyChannelTreeChange authorizes against the old policy and returns an
// independent snapshot. It does not mutate resources, activate policy, or
// authorize automatic cleanup. Serving callers must use the lifecycle barrier.
func ApplyChannelTreeChange(p RolePolicy, actorID int64, change ChannelTreeChange) (RolePolicy, error) {
	if change.OrderIndex != nil && change.Kind != ChannelMove {
		return RolePolicy{}, ErrRoleInvalid
	}
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
	if change.ChannelID <= 0 || change.ParentID < 0 {
		return RolePolicy{}, ErrRoleInvalid
	}
	next := cloneRolePolicy(p)
	switch change.Kind {
	case ChannelEdit:
		if !e.Evaluate(actorID, change.ChannelID, ManageChannels).Allowed {
			return RolePolicy{}, ErrRoleForbidden
		}
	case ChannelCreate:
		if _, exists := e.channels[change.ChannelID]; exists {
			return RolePolicy{}, ErrRoleInvalid
		}
		if change.Access.ChannelID != change.ChannelID || change.Access.ParentID != change.ParentID {
			return RolePolicy{}, ErrRoleInvalid
		}
		allowed := e.Evaluate(actorID, change.ParentID, ManageChannels).Allowed
		if change.Temporary {
			allowed = allowed || e.Evaluate(actorID, change.ParentID, CreateTemporaryChannels).Allowed
		}
		if !allowed {
			return RolePolicy{}, ErrRoleForbidden
		}
		next.Channels = append(next.Channels, change.Access)
		candidate, err := NewRoleEvaluator(next)
		if err != nil {
			return RolePolicy{}, fmt.Errorf("%w: %v", ErrRoleInvalid, err)
		}
		// Creating a custom child can bypass its parent's restrictions even
		// when the custom list is empty. Treat that as an access change.
		if !change.Access.Synced {
			if !e.Evaluate(actorID, change.ParentID, ManageChannelAccess).Allowed {
				return RolePolicy{}, ErrRoleForbidden
			}
			inherited := e.effectiveOverrides(change.ParentID)
			custom := candidate.effectiveOverrides(change.ChannelID)
			if err := authorizeOverrideChanges(e, actorID, change.ParentID, inherited, custom); err != nil {
				return RolePolicy{}, err
			}
		}
	case ChannelMove:
		old, exists := e.channels[change.ChannelID]
		if !exists {
			return RolePolicy{}, ErrRoleInvalid
		}
		if !e.Evaluate(actorID, old.ChannelID, ManageChannels).Allowed || !e.Evaluate(actorID, change.ParentID, ManageChannels).Allowed {
			return RolePolicy{}, ErrRoleForbidden
		}
		for _, id := range e.channelSubtree(old.ChannelID) {
			if !e.Evaluate(actorID, id, ManageChannels).Allowed {
				return RolePolicy{}, ErrRoleForbidden
			}
		}
		if change.SyncToParent && !e.Evaluate(actorID, old.ChannelID, ManageChannelAccess).Allowed {
			return RolePolicy{}, ErrRoleForbidden
		}
		for i := range next.Channels {
			ch := &next.Channels[i]
			if ch.ChannelID != change.ChannelID {
				continue
			}
			ch.ParentID = change.ParentID
			if change.SyncToParent {
				ch.Synced, ch.Overrides = true, nil
			} else {
				ch.Synced = false
				ch.Overrides = slices.Clone(e.effectiveOverrides(old.ChannelID))
			}
		}
		candidate, err := NewRoleEvaluator(next)
		if err != nil {
			return RolePolicy{}, fmt.Errorf("%w: %v", ErrRoleInvalid, err)
		}
		if err := authorizeChangedChannelAccess(e, candidate, actorID); err != nil {
			return RolePolicy{}, err
		}
	case ChannelDelete:
		if _, exists := e.channels[change.ChannelID]; !exists {
			return RolePolicy{}, ErrRoleInvalid
		}
		removed := map[int64]bool{}
		for _, id := range e.channelSubtree(change.ChannelID) {
			if !e.Evaluate(actorID, id, ManageChannels).Allowed {
				return RolePolicy{}, ErrRoleForbidden
			}
			removed[id] = true
		}
		next.Channels = slices.DeleteFunc(next.Channels, func(ch ChannelPolicy) bool { return removed[ch.ChannelID] })
	default:
		return RolePolicy{}, ErrRoleInvalid
	}
	next.Revision++
	validated, err := NewRoleEvaluator(next)
	if err != nil {
		return RolePolicy{}, fmt.Errorf("%w: %v", ErrRoleInvalid, err)
	}
	// Match the channel manager's existing maximum ancestor walk.
	for id := range validated.channels {
		depth := 0
		for id != 0 {
			depth++
			if depth > 64 {
				return RolePolicy{}, ErrRoleInvalid
			}
			id = validated.channels[id].ParentID
		}
	}
	return validated.Policy(), nil
}

func (e *RoleEvaluator) channelSubtree(root int64) []int64 {
	var result []int64
	for _, channel := range e.policy.Channels {
		for id := channel.ChannelID; id != 0; id = e.channels[id].ParentID {
			if id == root {
				result = append(result, channel.ChannelID)
				break
			}
		}
	}
	return result
}
