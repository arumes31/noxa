package server

import (
	"context"
	"noxa/internal/authorization"
	"noxa/internal/broadcast"
	"noxa/internal/state"
	"time"
)

// buildRoleSnapshot omits inaccessible channels and their members. A visible
// custom child of a hidden parent appears at the root, without its parent's ID.
// Counts describe only the visible tree; invisible presence is separately gated.
func buildRoleSnapshot(sm *state.Manager, evaluator *authorization.RoleEvaluator, actorID int64, uniqueID string) *broadcast.TreeSnapshot {
	snapshot, _ := buildRoleSnapshotContext(context.Background(), sm, evaluator, actorID, uniqueID)
	return snapshot
}

func buildRoleSnapshotContext(ctx context.Context, sm *state.Manager, evaluator *authorization.RoleEvaluator, actorID int64, uniqueID string) (*broadcast.TreeSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	showStats := evaluator.Evaluate(actorID, 0, authorization.ViewConnectionInfo).Allowed
	snapshot := broadcast.BuildSnapshot(sm, showStats, uniqueID)
	authority := evaluator.Evaluate(actorID, 0, authorization.Administrator)
	snapshot.CanSetInvisible = authority.Allowed
	snapshot.CanViewAuditLog = evaluator.Evaluate(actorID, 0, authorization.ViewAuditLog).Allowed
	switch {
	case authority.Reason == "owner", authority.Reason == "administrator":
		snapshot.OwnAuthority = authority.Reason
	case actorID == 0:
		snapshot.OwnAuthority = "guest"
	default:
		snapshot.OwnAuthority = "member"
	}
	decorateMembers := func(members []*broadcast.ClientInfo) error {
		for _, member := range members {
			if err := ctx.Err(); err != nil {
				return err
			}
			member.Roles = evaluator.MemberAppearance(member.UserID)
			for _, role := range member.Roles {
				if image := evaluator.RoleImage(role.ID); image != "" {
					if snapshot.RoleIcons == nil {
						snapshot.RoleIcons = make(map[int64]string)
					}
					snapshot.RoleIcons[role.ID] = image
				}
			}
			// Membership and flags are read in a snapshot independently of
			// movement cleanup. Never publish controls denied in that scope.
			member.PrioritySpeaker = member.PrioritySpeaker && member.ChannelID > 0 && evaluator.Evaluate(member.UserID, member.ChannelID, authorization.PrioritySpeaker).Allowed
			member.Sharing = member.Sharing && member.ChannelID > 0 && evaluator.Evaluate(member.UserID, member.ChannelID, authorization.ShareScreen).Allowed
			if !showStats && member.UniqueID != uniqueID {
				member.ConnectedAt = time.Time{}
			}
		}
		return nil
	}
	if err := decorateMembers(snapshot.UnassignedClients); err != nil {
		return nil, err
	}
	snapshot.TotalChannels = 0
	snapshot.TotalClients = len(snapshot.UnassignedClients)
	roots := []*broadcast.ChannelNode{}
	var visit func(*broadcast.ChannelNode, *broadcast.ChannelNode) error
	visit = func(node, visibleParent *broadcast.ChannelNode) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		children := node.Children
		node.Children = nil
		if evaluator.Evaluate(actorID, node.ChannelID, authorization.ViewChannel).Allowed {
			node.Access = &broadcast.ChannelAccess{
				CanManageVoiceQueue: evaluator.Evaluate(actorID, node.ChannelID, authorization.MuteMembers).Allowed,
				Restricted: !evaluator.Evaluate(0, node.ChannelID, authorization.ViewChannel).Allowed ||
					!evaluator.Evaluate(0, node.ChannelID, authorization.Connect).Allowed,
				CanConnect:      evaluator.Evaluate(actorID, node.ChannelID, authorization.Connect).Allowed,
				CanManageAccess: actorID > 0 && evaluator.Evaluate(actorID, node.ChannelID, authorization.ManageChannelAccess).Allowed,
			}
			// Count only the presence already visible to this snapshot recipient.
			for _, member := range node.Clients {
				if member.UserID > 0 {
					node.Access.MemberCount++
				} else {
					node.Access.GuestCount++
				}
			}
			if err := decorateMembers(node.Clients); err != nil {
				return err
			}
			snapshot.TotalChannels++
			snapshot.TotalClients += len(node.Clients)
			node.ClientCount = len(node.Clients)
			if visibleParent == nil {
				node.ParentID = 0
				roots = append(roots, node)
			} else {
				node.ParentID = visibleParent.ChannelID
				visibleParent.Children = append(visibleParent.Children, node)
			}
			visibleParent = node
		} else {
			visibleParent = nil
		}
		for _, child := range children {
			if err := visit(child, visibleParent); err != nil {
				return err
			}
		}
		return nil
	}
	for _, root := range snapshot.RootChannels {
		if err := visit(root, nil); err != nil {
			return nil, err
		}
	}
	snapshot.RootChannels = roots
	return snapshot, ctx.Err()
}
