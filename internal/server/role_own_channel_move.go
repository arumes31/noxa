package server

import (
	"crypto/sha256"
	"encoding/json"
	"time"

	"noxa/internal/authorization"
	"noxa/internal/broadcast"
	"noxa/internal/netproto"
)

// recordSnapshot remembers the recipient-visible content of a snapshot about
// to be sent (ignoring GeneratedAt) and reports whether it differs from the
// previous one. Event-driven deliveries skip unchanged snapshots so that
// filtered-out activity (an invisible member's status, a hidden channel's
// edit) is not revealed by the mere arrival and timing of a new snapshot.
func (c *Client) recordSnapshot(snapshot *broadcast.TreeSnapshot) bool {
	if snapshot == nil {
		return true
	}
	content := *snapshot
	content.GeneratedAt = time.Time{}
	raw, err := json.Marshal(&content)
	if err != nil {
		return true
	}
	digest := sha256.Sum256(raw)
	c.mu.Lock()
	defer c.mu.Unlock()
	changed := digest != c.lastSnapshotDigest
	c.lastSnapshotDigest = digest
	return changed
}

// roleSnapshotFrame encodes an event-driven snapshot for the recipient, or
// returns nil when its authorized projection did not change.
func (s *TCPServer) roleSnapshotFrame(client *Client, e *authorization.RoleEvaluator) (*netproto.Frame, error) {
	snapshot := s.roleSnapshotWithOwnMove(client, e)
	if !client.recordSnapshot(snapshot) {
		return nil, nil
	}
	return netproto.Encode(netproto.MsgSnapshot, snapshot)
}

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
	case eventUserJoined, eventUserLeft, eventUserMoved, eventMediaDeviceChanged, eventChannelCreated, eventChannelDeleted,
		eventChannelUpdated, eventStatusChanged, eventNicknameChanged, eventMemberVoiceChanged, eventAudioStateChanged, eventVoiceQueueChanged, eventKicked:
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
