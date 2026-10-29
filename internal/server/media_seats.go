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
//   - seats held in the destination by other sessions of the same principal
//     and device (a reconnect whose old session is not reaped yet) are not
//     counted and are replaced.
//
// Counting is O(1) per admission frame (no roster copy); the cap check walks
// at most one channel's seats.

import (
	"errors"
	"sync"

	"noxa/internal/netproto"
)

// errMediaE2EEChannelFull refuses a join or move of encrypted voice into a
// full channel; its text starts with the netproto reason code the clients
// localize.
var errMediaE2EEChannelFull = errors.New(mediaE2EEError(netproto.MediaE2EEReasonChannelFull, "this encrypted voice channel is full"))

type mediaSeatID struct{ principal, device string }

type mediaSeats struct {
	mu        sync.Mutex
	byChannel map[int64]map[string]mediaSeatID // channel -> client -> identity
	byClient  map[string]int64
}

// othersLocked counts the seats in ch held by other sessions, not counting
// stale sessions of the same identity.
func (m *mediaSeats) othersLocked(ch int64, client string, id mediaSeatID) int {
	n := 0
	for c, sid := range m.byChannel[ch] {
		if c != client && (id.device == "" || sid != id) {
			n++
		}
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
// seated in ch keeps its seat. It returns an undo that restores the previous
// seat (replaced stale sessions are not restored).
func (m *mediaSeats) take(ch int64, client string, id mediaSeatID, limit int) (func(), bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.takeLocked(ch, client, id, limit)
}

func (m *mediaSeats) takeLocked(ch int64, client string, id mediaSeatID, limit int) (func(), bool) {
	prev, had := m.byClient[client]
	if had && prev == ch {
		m.byChannel[ch][client] = id
		return func() {}, true
	}
	if m.othersLocked(ch, client, id) >= limit {
		return nil, false
	}
	if id.device != "" {
		for c, sid := range m.byChannel[ch] {
			if c != client && sid == id {
				m.removeLocked(c)
			}
		}
	}
	m.putLocked(ch, client, id)
	return func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if cur, ok := m.byClient[client]; !ok || cur != ch {
			return // released or moved meanwhile
		}
		if had {
			m.putLocked(prev, client, id)
		} else {
			m.removeLocked(client)
		}
	}, true
}

// carry moves a seated client's seat to ch (see take); an unseated client
// carries nothing.
// The read and the transfer share one critical section, so a concurrent
// release can never be undone by a move.
func (m *mediaSeats) carry(ch int64, client string, limit int) (func(), bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	prev, had := m.byClient[client]
	if !had {
		return func() {}, true
	}
	return m.takeLocked(ch, client, m.byChannel[prev][client], limit)
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

// takeMediaSeat seats client in channel for an encrypted offer. ok is false
// when the channel is full.
func (s *TCPServer) takeMediaSeat(client *Client, channel int64) (undo func(), ok bool) {
	if !s.mediaE2EERequired() || channel <= 0 {
		return func() {}, true
	}
	return s.mediaSeats.take(channel, client.ID, mediaSeatID{principal: client.uniqueID(), device: client.currentDeviceID()}, MediaE2EEMaxVoice)
}

// carryMediaSeat moves a seated session's seat with a channel move; a full
// destination refuses the move with errMediaE2EEChannelFull.
func (s *TCPServer) carryMediaSeat(clientID string, channel int64) (func(), error) {
	if !s.mediaE2EERequired() {
		return func() {}, nil
	}
	undo, ok := s.mediaSeats.carry(channel, clientID, MediaE2EEMaxVoice)
	if !ok {
		return nil, errMediaE2EEChannelFull
	}
	return undo, nil
}
