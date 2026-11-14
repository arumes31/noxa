// voice_connecting.go tracks which channel members are still connecting their
// voice: from entering a channel (or resuming) until the SFU reports their
// peer connected. Snapshots mark them "connecting" so clients can show it.
package server

import (
	"sync"
	"time"

	"noxa/internal/broadcast"
)

// voiceConnectGrace ends "connecting" for a member that never brings up a
// voice peer (no microphone, a text-only client), so the mark cannot stick.
const voiceConnectGrace = 15 * time.Second

type voiceConnecting struct {
	mu    sync.Mutex
	since map[string]time.Time
}

// markVoiceConnecting starts the mark. announce sends a snapshot refresh;
// callers whose own broadcast follows (a move, a resume) pass false.
func (s *TCPServer) markVoiceConnecting(clientID string, announce bool) {
	now := time.Now()
	s.connecting.mu.Lock()
	if s.connecting.since == nil {
		s.connecting.since = map[string]time.Time{}
	}
	if _, ok := s.connecting.since[clientID]; ok {
		s.connecting.mu.Unlock()
		return
	}
	s.connecting.since[clientID] = now
	s.connecting.mu.Unlock()
	if announce {
		s.announceConnectingFlip()
	}
	time.AfterFunc(voiceConnectGrace, func() { s.clearVoiceConnecting(clientID, now) })
}

// clearVoiceConnecting ends the mark (only the one started at since, unless
// since is zero) and announces the flip.
func (s *TCPServer) clearVoiceConnecting(clientID string, since time.Time) {
	s.connecting.mu.Lock()
	at, ok := s.connecting.since[clientID]
	if !ok || (!since.IsZero() && !at.Equal(since)) {
		s.connecting.mu.Unlock()
		return
	}
	delete(s.connecting.since, clientID)
	s.connecting.mu.Unlock()
	s.announceConnectingFlip()
}

// forgetVoiceConnecting drops the mark silently (leave, disconnect): the
// membership change already refreshes every snapshot.
func (s *TCPServer) forgetVoiceConnecting(clientID string) {
	s.connecting.mu.Lock()
	delete(s.connecting.since, clientID)
	s.connecting.mu.Unlock()
}

func (s *TCPServer) voiceConnectingNow(clientID string) bool {
	s.connecting.mu.Lock()
	defer s.connecting.mu.Unlock()
	_, ok := s.connecting.since[clientID]
	return ok
}

// onPeerState is the SFU's report: connected clears the mark, a lost peer of
// a member still in a channel sets it again until the peer recovers.
func (s *TCPServer) onPeerState(clientID string, connected bool) {
	if connected {
		s.clearVoiceConnecting(clientID, time.Time{})
		return
	}
	if s.deps == nil || s.deps.State == nil {
		return
	}
	if member, ok := s.deps.State.GetClient(clientID); ok && member.ChannelID > 0 {
		s.markVoiceConnecting(clientID, true)
	}
}

// announceConnectingFlip refreshes every recipient's filtered snapshot once.
func (s *TCPServer) announceConnectingFlip() {
	if s.deps != nil && s.deps.Broadcast != nil {
		s.deps.Broadcast.BroadcastEvent(roleResyncDelivery, []byte("{}"))
	}
}

// decorateConnecting marks channel members still connecting their voice.
func (s *TCPServer) decorateConnecting(snapshot *broadcast.TreeSnapshot) {
	if snapshot == nil {
		return
	}
	s.connecting.mu.Lock()
	defer s.connecting.mu.Unlock()
	if len(s.connecting.since) == 0 {
		return
	}
	var walk func([]*broadcast.ChannelNode)
	walk = func(nodes []*broadcast.ChannelNode) {
		for _, node := range nodes {
			for _, member := range node.Clients {
				if _, ok := s.connecting.since[member.ClientID]; ok && member.ChannelID > 0 {
					member.Connecting = true
				}
			}
			walk(node.Children)
		}
	}
	walk(snapshot.RootChannels)
}
