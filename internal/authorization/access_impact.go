package authorization

import "context"

type AccessImpactChange struct {
	Capability Capability `json:"capability"`
	Before     bool       `json:"before"`
	After      bool       `json:"after"`
}

type MemberAccessImpact struct {
	UserID  int64                `json:"user_id"`
	Changes []AccessImpactChange `json:"changes"`
}

// ChannelAccessImpact describes one channel and the supplied subjects at the
// committed base revision. It is not a committed change or a server-wide count.
type ChannelAccessImpact struct {
	Revision  int64                `json:"revision"`
	ChannelID int64                `json:"channel_id"`
	Members   []MemberAccessImpact `json:"members"`
}

// PreviewChannelAccess applies the same pre-change authorization and validation
// as saving, but evaluates a private candidate without persistence or effects.
// Subjects are bounded to a roster page plus the anonymous guest baseline.
func PreviewChannelAccess(ctx context.Context, p RolePolicy, actorID int64, change RoleChange, userIDs []int64) (ChannelAccessImpact, error) {
	return PreviewChannelAccessInScope(ctx, p, actorID, change, 0, userIDs)
}

// PreviewChannelAccessInScope compares the edited channel (scopeID zero) or a
// currently managed synced descendant, while authorizing the complete draft.
func PreviewChannelAccessInScope(ctx context.Context, p RolePolicy, actorID int64, change RoleChange, scopeID int64, userIDs []int64) (ChannelAccessImpact, error) {
	if err := ctx.Err(); err != nil {
		return ChannelAccessImpact{}, err
	}
	if change.Kind != ChannelAccessSet || change.Channel.ChannelID < 1 || !validImpactSubjects(userIDs) {
		return ChannelAccessImpact{}, ErrRoleInvalid
	}
	before, err := NewRoleEvaluator(p)
	if err != nil {
		return ChannelAccessImpact{}, err
	}
	if actorID < 1 || !before.Evaluate(actorID, change.Channel.ChannelID, ManageChannelAccess).Allowed {
		return ChannelAccessImpact{}, ErrRoleForbidden
	}
	scopeID, err = before.impactScope(actorID, change.Channel.ChannelID, scopeID)
	if err != nil {
		return ChannelAccessImpact{}, err
	}
	candidate, err := ApplyRoleChange(p, actorID, change)
	if err != nil {
		return ChannelAccessImpact{}, err
	}
	after, err := NewRoleEvaluator(candidate)
	if err != nil {
		return ChannelAccessImpact{}, err
	}
	return compareChannelAccess(ctx, before, after, p.Revision, scopeID, userIDs)
}

func validImpactSubjects(userIDs []int64) bool {
	if len(userIDs) < 1 || len(userIDs) > 101 {
		return false
	}
	seen := make(map[int64]bool, len(userIDs))
	for _, id := range userIDs {
		if id < 0 || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

func compareChannelAccess(ctx context.Context, before, after *RoleEvaluator, revision, channelID int64, userIDs []int64) (ChannelAccessImpact, error) {
	result := ChannelAccessImpact{Revision: revision, ChannelID: channelID, Members: make([]MemberAccessImpact, 0, len(userIDs))}
	capabilities := Capabilities()
	for _, id := range userIDs {
		if err := ctx.Err(); err != nil {
			return ChannelAccessImpact{}, err
		}
		member := MemberAccessImpact{UserID: id, Changes: []AccessImpactChange{}}
		for _, capability := range capabilities {
			if !capability.Channel {
				continue
			}
			old := before.Evaluate(id, result.ChannelID, capability.Key).Allowed
			next := after.Evaluate(id, result.ChannelID, capability.Key).Allowed
			if old != next {
				member.Changes = append(member.Changes, AccessImpactChange{Capability: capability.Key, Before: old, After: next})
			}
		}
		result.Members = append(result.Members, member)
	}
	return result, nil
}
