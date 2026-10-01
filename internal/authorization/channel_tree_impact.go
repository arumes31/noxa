package authorization

import "context"

// PreviewChannelTree checks creation or movement without allocating a stored
// channel ID. Creation reports channel_id=0 and denied-before for every grant:
// no channel existed. Settings, passwords and operational limits are not previewed.
func PreviewChannelTree(ctx context.Context, p RolePolicy, actorID int64, change ChannelTreeChange, userIDs []int64) (ChannelAccessImpact, error) {
	return PreviewChannelTreeInScope(ctx, p, actorID, change, 0, userIDs)
}

// PreviewChannelTreeInScope optionally inspects a synced descendant of a moved
// channel. New channels have no descendants and require previewScopeID zero.
func PreviewChannelTreeInScope(ctx context.Context, p RolePolicy, actorID int64, change ChannelTreeChange, previewScopeID int64, userIDs []int64) (ChannelAccessImpact, error) {
	if err := ctx.Err(); err != nil {
		return ChannelAccessImpact{}, err
	}
	if !validImpactSubjects(userIDs) || (change.Kind != ChannelCreate && change.Kind != ChannelMove) {
		return ChannelAccessImpact{}, ErrRoleInvalid
	}
	scopeID := change.ChannelID
	if change.Kind == ChannelCreate {
		if change.ChannelID != 0 || change.Access.ChannelID != 0 || change.SyncToParent || change.OrderIndex != nil || previewScopeID != 0 {
			return ChannelAccessImpact{}, ErrRoleInvalid
		}
		scopeID = change.ParentID
	} else if change.Temporary || change.Access.ChannelID != 0 || change.Access.ParentID != 0 || change.Access.Synced || len(change.Access.Overrides) != 0 {
		return ChannelAccessImpact{}, ErrRoleInvalid
	}
	before, err := NewRoleEvaluator(p)
	if err != nil {
		return ChannelAccessImpact{}, err
	}
	if actorID < 1 || !before.Evaluate(actorID, scopeID, ManageChannelAccess).Allowed {
		return ChannelAccessImpact{}, ErrRoleForbidden
	}
	if change.Kind == ChannelMove {
		previewScopeID, err = before.impactScope(actorID, change.ChannelID, previewScopeID)
		if err != nil {
			return ChannelAccessImpact{}, err
		}
	}
	if change.Kind == ChannelCreate {
		// Use an unused in-memory ID, including when stored IDs reach MaxInt64.
		change.ChannelID = 1
		for {
			if _, exists := before.channels[change.ChannelID]; !exists {
				break
			}
			change.ChannelID++
		}
		change.Access.ChannelID = change.ChannelID
		previewScopeID = change.ChannelID
	}
	candidate, err := ApplyChannelTreeChange(p, actorID, change)
	if err != nil {
		return ChannelAccessImpact{}, err
	}
	after, err := NewRoleEvaluator(candidate)
	if err != nil {
		return ChannelAccessImpact{}, err
	}
	result, err := compareChannelAccess(ctx, before, after, p.Revision, previewScopeID, userIDs)
	if change.Kind == ChannelCreate {
		result.ChannelID = 0
	}
	return result, err
}
