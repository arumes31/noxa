package server

import (
	"crypto/sha256"
	"encoding/json"
	"time"

	"noxa/internal/authorization"
	"noxa/internal/broadcast"
	"noxa/internal/netproto"
)

// snapshotDigest hashes the recipient-visible content of a snapshot,
// ignoring GeneratedAt.
func snapshotDigest(snapshot *broadcast.TreeSnapshot) ([32]byte, bool) {
	if snapshot == nil {
		return [32]byte{}, false
	}
	content := *snapshot
	content.GeneratedAt = time.Time{}
	raw, err := json.Marshal(&content)
	if err != nil {
		return [32]byte{}, false
	}
	return sha256.Sum256(raw), true
}

// recordSnapshot remembers a snapshot that was successfully written to the
// client. Event-driven status deliveries skip unchanged snapshots so filtered
// activity (an invisible member's status) is not revealed by the arrival and
// timing of an identical snapshot.
func (c *Client) recordSnapshot(snapshot *broadcast.TreeSnapshot) {
	digest, ok := snapshotDigest(snapshot)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snapshotStaged = false
	if ok {
		c.lastSnapshotDigest = digest
	} else {
		c.lastSnapshotDigest = [32]byte{}
	}
}

// snapshotUnchanged reports whether snapshot matches the last one written.
func (c *Client) snapshotUnchanged(snapshot *broadcast.TreeSnapshot) bool {
	digest, ok := snapshotDigest(snapshot)
	if !ok {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return digest == c.lastSnapshotDigest
}

// stageSnapshot holds a snapshot digest until its frame is written; the
// delivery path commits it only after a successful write (it runs under
// roleMetadataMu, so stage and commit cannot interleave across deliveries).
func (c *Client) stageSnapshot(snapshot *broadcast.TreeSnapshot) {
	digest, ok := snapshotDigest(snapshot)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snapshotStagedDigest, c.snapshotStaged = digest, ok
}

func (c *Client) commitStagedSnapshot() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.snapshotStaged {
		c.lastSnapshotDigest, c.snapshotStaged = c.snapshotStagedDigest, false
	}
}

// roleSnapshotFullFrame always encodes a snapshot and stages its digest.
func (s *TCPServer) roleSnapshotFullFrame(client *Client, e *authorization.RoleEvaluator) (*netproto.Frame, error) {
	snapshot := s.roleSnapshotWithOwnMove(client, e)
	client.stageSnapshot(snapshot)
	return netproto.Encode(netproto.MsgSnapshot, snapshot)
}

// roleSnapshotFrame encodes an event-driven snapshot for the recipient, or
// returns nil when its authorized projection did not change.
func (s *TCPServer) roleSnapshotFrame(client *Client, e *authorization.RoleEvaluator) (*netproto.Frame, error) {
	snapshot := s.roleSnapshotWithOwnMove(client, e)
	if client.snapshotUnchanged(snapshot) {
		return nil, nil
	}
	client.stageSnapshot(snapshot)
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
		eventChannelUpdated, eventStatusChanged, eventNicknameChanged, eventMemberVoiceChanged, eventAudioStateChanged, eventVoiceQueueChanged, eventKicked, roleResyncDelivery:
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
	userID := client.userID()
	s.presence.decorate(snapshot, func(channelID int64) bool {
		return channelID <= 0 || e.Evaluate(userID, channelID, authorization.ViewChannel).Allowed
	}, time.Now())
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
