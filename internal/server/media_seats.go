package server

// media_seats.go: seats of encrypted channel voice (Task 2a fix3 review P2-1,
// P3-3..P3-5).
//
// A session holds a seat in the channel whose encrypted voice accepted its
// offer. Seats are the only thing the 64-member cap (MediaE2EEMaxVoice)
// counts, and the rekey budgets scale with them:
//   - an offer takes the seat under the session's channel-control lock,
//     before the voice backend answers, with the channel ID checked by that
//     lock; a failed offer gives a newly taken seat back;
//   - a move of a seated session carries the seat to the destination, and is
//     refused (media_e2ee_channel_full) when the destination is full: the
//     move keeps its voice peer, so it is a join of encrypted voice as well;
//   - leaving the channel parks the seat (the voice peer lives on, so the
//     next join meets the cap); it is released when the session's voice
//     ends: the session is removed or disconnects;
//   - a seat held in the destination by a DEAD session of the same principal
//     and device (a reconnect whose old session is not reaped yet) is not
//     counted and is replaced, and that session's voice is ended; a live
//     second session of the same device holds a seat of its own;
//   - a move of a session with a media device (a live voice peer) but no
//     seat takes a seat with the cap: a missing seat is never a free pass;
//   - a permission eviction and a channel deletion park the seat.
//
// Invariant: the sessions with a voice peer routed into an encrypted channel
// hold its seats, and a channel never has more than MediaE2EEMaxVoice seats.
//
// Counting is O(1) per admission frame (no roster copy); the cap check walks
// at most one channel's seats.

import (
	"sync"

	"noxa/internal/netproto"
)

// errMediaE2EEChannelFull refuses a join or move of encrypted voice into a
// full channel; its text starts with the netproto reason code the clients
// localize.
var errMediaE2EEChannelFull = netproto.ErrMediaE2EEChannelFull

type mediaSeatID struct{ principal, device string }

type mediaSeats struct {
	mu        sync.Mutex
	byChannel map[int64]map[string]mediaSeatID // channel -> client -> identity
	byClient  map[string]int64
}

// seatLive reports whether a session is still live (registered, not
// revoked, not disconnect-cleaned). A nil seatLive treats every session as
// live (nothing is ever replaced).
type seatLive func(client string) bool

func (l seatLive) alive(client string) bool { return l == nil || l(client) }

// replacedSeat is a dead session's seat that a take replaced.
type replacedSeat struct {
	client string
	ch     int64
	id     mediaSeatID
}

// othersLocked counts the seats in ch held by other sessions. Only a DEAD
// session of the same identity (a reconnect whose old session is not reaped
// yet) is not counted: a live second session of the same device holds a
// seat of its own (fix4 review P2-1).
func (m *mediaSeats) othersLocked(ch int64, client string, id mediaSeatID, live seatLive) int {
	n := 0
	for c, sid := range m.byChannel[ch] {
		if c == client || (id.device != "" && sid == id && !live.alive(c)) {
			continue
		}
		n++
	}
	return n
}

func (m *mediaSeats) removeLocked(client string) {
	ch, ok := m.byClient[client]
	if !ok {
		return
	}
	delete(m.byClient, client)
	if seats := m.byChannel[ch]; seats != nil {
		delete(seats, client)
		if len(seats) == 0 {
			delete(m.byChannel, ch)
		}
	}
}

func (m *mediaSeats) putLocked(ch int64, client string, id mediaSeatID) {
	if m.byChannel == nil {
		m.byChannel = map[int64]map[string]mediaSeatID{}
		m.byClient = map[string]int64{}
	}
	m.removeLocked(client)
	if m.byChannel[ch] == nil {
		m.byChannel[ch] = map[string]mediaSeatID{}
	}
	m.byChannel[ch][client] = id
	m.byClient[client] = ch
}

// take seats client in ch unless ch already has limit other seats. A session
// seated in ch keeps its seat. Seats of dead sessions of the same identity
// are replaced and returned (the caller ends their voice). The undo restores
// the previous seat, and every replaced seat whose session is live again.
func (m *mediaSeats) take(ch int64, client string, id mediaSeatID, limit int, live seatLive) (func(), []string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.takeLocked(ch, client, id, limit, live)
}

func (m *mediaSeats) takeLocked(ch int64, client string, id mediaSeatID, limit int, live seatLive) (func(), []string, bool) {
	prev, had := m.byClient[client]
	if had && prev == ch {
		m.byChannel[ch][client] = id
		return func() {}, nil, true
	}
	if m.othersLocked(ch, client, id, live) >= limit {
		return nil, nil, false
	}
	var replaced []replacedSeat
	if id.device != "" {
		for c, sid := range m.byChannel[ch] {
			if c != client && sid == id && !live.alive(c) {
				replaced = append(replaced, replacedSeat{client: c, ch: ch, id: sid})
				m.removeLocked(c)
			}
		}
	}
	m.putLocked(ch, client, id)
	ids := make([]string, len(replaced))
	for i, r := range replaced {
		ids[i] = r.client
	}
	return func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if cur, ok := m.byClient[client]; ok && cur == ch {
			if had {
				m.putLocked(prev, client, id)
			} else {
				m.removeLocked(client)
			}
		}
		for _, r := range replaced {
			if _, seated := m.byClient[r.client]; !seated && live.alive(r.client) {
				m.putLocked(r.ch, r.client, r.id)
			}
		}
	}, ids, true
}

// carry moves a client's seat to ch (see take). A client without a seat but
// with encrypted voice (fallback: a media device, so a live peer) takes a
// seat with the cap: a missing seat is never a free pass. A client without
// either carries nothing. The read and the transfer share one critical
// section, so a concurrent release can never be undone by a move.
func (m *mediaSeats) carry(ch int64, client string, fallback *mediaSeatID, limit int, live seatLive) (func(), []string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	prev, had := m.byClient[client]
	if had {
		return m.takeLocked(ch, client, m.byChannel[prev][client], limit, live)
	}
	if fallback != nil {
		return m.takeLocked(ch, client, *fallback, limit, live)
	}
	return func() {}, nil, true
}

// park keeps a seated client seated in no channel (it left its channel but
// its voice peer lives on); its next move carries the seat (and meets the
// cap) again.
func (m *mediaSeats) park(client string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ch, ok := m.byClient[client]; ok && ch != 0 {
		m.putLocked(0, client, m.byChannel[ch][client])
	}
}

func (m *mediaSeats) release(client string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.removeLocked(client)
}

// seat is the channel the client is seated in (0 when none).
func (m *mediaSeats) seat(client string) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.byClient[client]
}

// others is the number of seats in ch not held by client.
func (m *mediaSeats) others(ch int64, client string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	seats := m.byChannel[ch]
	n := len(seats)
	if _, ok := seats[client]; ok {
		n--
	}
	return n
}

// mediaSessionLive reports whether the session is registered, authenticated
// and neither revoked nor cleaned up.
func (s *TCPServer) mediaSessionLive(id string) bool {
	c, ok := s.clientByID(id)
	return ok && c.needsDisconnectCleanup() && !c.sessionRevoked()
}

// endReplacedVoice ends the channel voice of sessions whose seat was replaced
// (they are dead; the peer and media device must not outlive the seat).
func (s *TCPServer) endReplacedVoice(ids []string) {
	for _, id := range ids {
		if s.deps != nil && s.deps.Voice != nil {
			_ = s.deps.Voice.ClosePeer(id)
		}
		if s.deps != nil && s.deps.State != nil && s.deps.State.SetMediaDevice(id, "") {
			s.broadcastEvent(eventMediaDeviceChanged, mediaDeviceEvent{ClientID: id})
		}
	}
}

// takeMediaSeat seats client in channel for an encrypted offer. ok is false
// when the channel is full.
func (s *TCPServer) takeMediaSeat(client *Client, channel int64) (undo func(), ok bool) {
	if !s.mediaE2EERequired() || channel <= 0 {
		return func() {}, true
	}
	undo, replaced, ok := s.mediaSeats.take(channel, client.ID, mediaSeatID{principal: client.uniqueID(), device: client.currentDeviceID()}, MediaE2EEMaxVoice, s.mediaSessionLive)
	s.endReplacedVoice(replaced)
	return undo, ok
}

// carryMediaSeat moves a session's seat with a channel move; a session with
// a media device but no seat takes one. A full destination refuses the move
// with errMediaE2EEChannelFull.
func (s *TCPServer) carryMediaSeat(clientID string, channel int64) (func(), error) {
	if !s.mediaE2EERequired() {
		return func() {}, nil
	}
	var fallback *mediaSeatID
	if s.deps != nil && s.deps.State != nil {
		if c, ok := s.deps.State.GetClient(clientID); ok && c.MediaDevice != "" {
			fallback = &mediaSeatID{principal: c.UniqueID, device: c.MediaDevice}
		}
	}
	undo, replaced, ok := s.mediaSeats.carry(channel, clientID, fallback, MediaE2EEMaxVoice, s.mediaSessionLive)
	if !ok {
		return nil, errMediaE2EEChannelFull
	}
	s.endReplacedVoice(replaced)
	return undo, nil
}
