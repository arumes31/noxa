package server

import (
	"noxa/internal/authorization"
	"noxa/internal/broadcast"
)

func (c *Client) rememberOwnChannelMove(from, to int64, forced bool) {
	if from == to {
		return
	}
	c.mu.Lock()
	c.lastOwnChannelMove = &broadcast.OwnChannelMove{ChannelID: to, Forced: forced}
	c.mu.Unlock()
}

func roleSnapshotEvent(kind string) bool {
	switch kind {
	case eventUserJoined, eventUserLeft, eventUserMoved, eventChannelCreated, eventChannelDeleted,
		eventChannelUpdated, eventStatusChanged, eventNicknameChanged, eventMemberVoiceChanged, eventAudioStateChanged, eventKicked:
		return true
	default:
		return false
	}
}

// Called while roleMetadataMu holds membership and its cause coherent. Keep
// the cause on the session: an earlier queued event may observe the new channel
// before its move event is delivered. Only the moved recipient gets this hint.
func (s *TCPServer) roleSnapshotWithOwnMove(client *Client, e *authorization.RoleEvaluator) *broadcast.TreeSnapshot {
	snapshot := buildRoleSnapshot(s.deps.State, e, client.userID(), client.uniqueID())
	s.decorateOwnChannelMove(snapshot, client, e)
	return snapshot
}

func (s *TCPServer) decorateOwnChannelMove(snapshot *broadcast.TreeSnapshot, client *Client, e *authorization.RoleEvaluator) {
	client.mu.RLock()
	move := client.lastOwnChannelMove
	client.mu.RUnlock()
	if snapshot == nil || move == nil || move.ChannelID <= 0 || !e.Evaluate(client.userID(), move.ChannelID, authorization.ViewChannel).Allowed {
		return
	}
	member, ok := s.deps.State.GetClient(client.ID)
	if !ok || member.ChannelID != move.ChannelID {
		return
	}
	var containsSelf func([]*broadcast.ChannelNode) bool
	containsSelf = func(nodes []*broadcast.ChannelNode) bool {
		for _, node := range nodes {
			for _, member := range node.Clients {
				if member.ClientID == client.ID && member.ChannelID == move.ChannelID {
					return true
				}
			}
			if containsSelf(node.Children) {
				return true
			}
		}
		return false
	}
	if containsSelf(snapshot.RootChannels) {
		copy := *move
		snapshot.OwnChannelMove = &copy
	}
}
