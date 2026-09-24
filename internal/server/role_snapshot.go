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
	snapshot.CanSetInvisible = evaluator.Evaluate(actorID, 0, authorization.Administrator).Allowed
	decorateMembers := func(members []*broadcast.ClientInfo) error {
		for _, member := range members {
			if err := ctx.Err(); err != nil {
				return err
			}
			member.Roles = evaluator.MemberAppearance(member.UserID)
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
