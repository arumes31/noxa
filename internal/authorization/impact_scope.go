package authorization

import (
	"context"
	"slices"
)

// ImpactDescendants lists only managed descendants whose entire path inherits
// from sourceID. Custom branches are independent and excluded, even if a child
// of that custom branch is itself synced. No policy or resource names are exposed.
func (e *RoleEvaluator) ImpactDescendants(ctx context.Context, actorID, sourceID int64) ([]int64, error) {
	if actorID < 1 || sourceID < 1 || !e.Evaluate(actorID, sourceID, ManageChannelAccess).Allowed {
		return nil, ErrRoleForbidden
	}
	ids := []int64{}
	for id := range e.channels {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if id != sourceID && e.syncedFrom(id, sourceID) && e.Evaluate(actorID, id, ManageChannelAccess).Allowed {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids, nil
}

func (e *RoleEvaluator) syncedFrom(channelID, sourceID int64) bool {
	for channelID != sourceID {
		ch, exists := e.channels[channelID]
		if !exists || !ch.Synced || ch.ParentID == 0 {
			return false
		}
		channelID = ch.ParentID
	}
	return true
}

func (e *RoleEvaluator) impactScope(actorID, sourceID, scopeID int64) (int64, error) {
	if scopeID < 0 {
		return 0, ErrRoleInvalid
	}
	if scopeID == 0 {
		scopeID = sourceID
	}
	if !e.syncedFrom(scopeID, sourceID) || !e.Evaluate(actorID, scopeID, ManageChannelAccess).Allowed {
		return 0, ErrRoleForbidden
	}
	return scopeID, nil
}
