package main

import (
	"sync"
	"sync/atomic"
)

// peerKeyLookup is the outcome of a directory lookup for a peer's E2E key.
// Only a clean answer that the peer published no key is peerKeyAbsent:
// timeouts, rate limits, disconnects and malformed or mismatched replies are
// peerKeyFailed and must never be read as "the peer has no key".
type peerKeyLookup uint8

const (
	peerKeyFailed peerKeyLookup = iota
	peerKeyFound
	peerKeyAbsent
)

// dmServerFallbackAllowed decides whether a DM may use the
// client→server→client path (the server can read it). That is only allowed
// when the server cleanly said the peer has no E2E key AND this client has no
// E2E relationship with the peer (a known key or end-to-end history). Any
// other case is a send error: plaintext is never an option, and a network
// blip or a hostile server must not silently downgrade a conversation.
func dmServerFallbackAllowed(lookup peerKeyLookup, knownE2E bool) bool {
	return lookup == peerKeyAbsent && !knownE2E
}

// e2ePeerCapacity bounds the in-memory set of peers with E2E history.
const e2ePeerCapacity = 4096

// e2ePeerSet remembers peers this client exchanged end-to-end DMs with. It
// outlives reconnects (unlike the directory cache), so a peer whose key
// disappears from the directory is not silently downgraded.
type e2ePeerSet struct {
	mu       sync.Mutex
	peers    map[string]uint64
	sequence uint64
	// warmed is set once the local DM history has been scanned.
	warmed atomic.Bool
}

func newE2EPeerSet() *e2ePeerSet { return &e2ePeerSet{peers: make(map[string]uint64)} }

func (s *e2ePeerSet) record(uid string) {
	if s == nil || uid == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.peers[uid]; !exists && len(s.peers) >= e2ePeerCapacity {
		var oldestUID string
		var oldest uint64
		for candidate, used := range s.peers {
			if oldestUID == "" || used < oldest {
				oldestUID, oldest = candidate, used
			}
		}
		delete(s.peers, oldestUID)
	}
	s.sequence++
	s.peers[uid] = s.sequence
}

func (s *e2ePeerSet) has(uid string) bool {
	if s == nil || uid == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.peers[uid]
	return ok
}

// hasE2E reports whether the peer's stored conversation holds a body this
// client opened end to end.
func (s dmHistoryStore) hasE2E(peer string) bool {
	l, err := s.load(peer)
	if err != nil {
		return false
	}
	for _, m := range l.Messages {
		if dmEncMode(m) == "e2e" {
			return true
		}
	}
	return false
}

// e2ePeers lists the stored conversations with end-to-end history.
func (s dmHistoryStore) e2ePeers() []string {
	var out []string
	for _, p := range s.peers() {
		if s.hasE2E(p.UniqueID) {
			out = append(out, p.UniqueID)
		}
	}
	return out
}

// knownE2EPeer reports cheap local evidence of an E2E relationship: the
// in-memory set, a cached directory key, or (when withHistory) the peer's
// local DM log. withHistory reads a file, so the read loop never sets it.
func (m *connManager) knownE2EPeer(uid string, withHistory bool) bool {
	if m.e2ePeers.has(uid) {
		return true
	}
	if m.pubKeys != nil {
		if _, ok := m.pubKeys.get(uid); ok {
			return true
		}
	}
	if !withHistory {
		return false
	}
	store, ok := m.historyStore()
	if !ok || !store.hasE2E(uid) {
		return false
	}
	m.e2ePeers.record(uid)
	return true
}

// historyStore opens this tab identity's local DM history, if configured.
func (m *connManager) historyStore() (dmHistoryStore, bool) {
	if m.dmHistory == nil {
		return dmHistoryStore{}, false
	}
	id, err := m.identity()
	if err != nil {
		return dmHistoryStore{}, false
	}
	store, err := m.dmHistory(id)
	if err != nil {
		return dmHistoryStore{}, false
	}
	return store, true
}

// warmE2EPeers loads the peers with end-to-end history once per manager, so
// the read loop can flag a server-encrypted DM from such a peer without
// touching the disk. It runs in the background after a connection is up.
func (m *connManager) warmE2EPeers() {
	if m.e2ePeers == nil || m.dmHistory == nil || !m.e2ePeers.warmed.CompareAndSwap(false, true) {
		return
	}
	go guardCrash("e2ePeerWarmup", func() {
		store, ok := m.historyStore()
		if !ok {
			return
		}
		for _, uid := range store.e2ePeers() {
			m.e2ePeers.record(uid)
		}
	})
}
