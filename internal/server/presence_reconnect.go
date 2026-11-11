package server

import (
	"slices"
	"sync"
	"time"

	"noxa/internal/broadcast"
	"noxa/internal/netproto"
)

// A dropped connection is reported as lost, and the same device's next login
// as a reconnect, so observers hear "lost the connection" / "reconnected"
// instead of "left" / "joined".
const (
	// presenceDepartureWindow keeps a departure reason long enough for the
	// snapshots that announce it.
	presenceDepartureWindow = 30 * time.Second
	// presenceReconnectWindow is how long a lost session can come back as a reconnect.
	presenceReconnectWindow = 2 * time.Minute
	// presenceTrackerMax bounds each map; churn beyond it only drops old cues.
	presenceTrackerMax = 1024
)

type presenceDeparture struct {
	reason    string
	channelID int64
	hidden    bool
	at        time.Time
}

// presenceTracker remembers recent departure reasons and reconnects. The zero
// value is ready to use.
type presenceTracker struct {
	mu          sync.Mutex
	departures  map[string]presenceDeparture // client_id -> departure
	lost        map[string]time.Time         // session key -> when it was lost
	reconnected map[string]time.Time         // client_id -> when it reconnected
}

// noteDeparture records why a session left. A deliberate leave clears the
// session key, so a later login of that device is an ordinary join.
func (p *presenceTracker) noteDeparture(clientID, key, reason string, channelID int64, hidden bool, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pruneLocked(now)
	delete(p.reconnected, clientID)
	if key != "" {
		switch reason {
		case netproto.DepartureConnectionLost:
			p.lost = setBounded(p.lost, key, now)
		case "":
			delete(p.lost, key)
		}
	}
	if reason != "" {
		if p.departures == nil {
			p.departures = map[string]presenceDeparture{}
		}
		if len(p.departures) >= presenceTrackerMax {
			for id := range p.departures { // arbitrary eviction keeps the map bounded
				delete(p.departures, id)
				break
			}
		}
		p.departures[clientID] = presenceDeparture{reason: reason, channelID: channelID, hidden: hidden, at: now}
	}
}

// noteJoin reports whether a new session is a reconnect: its device was lost
// recently, or an older session of the same device is still registered.
func (p *presenceTracker) noteJoin(clientID, key string, overlapping bool, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pruneLocked(now)
	_, lost := p.lost[key]
	if key != "" {
		delete(p.lost, key)
	}
	if !lost && !overlapping {
		return false
	}
	p.reconnected = setBounded(p.reconnected, clientID, now)
	return true
}

// decorate adds the recent departures the recipient may see and the present
// members that just reconnected. visible decides channel visibility.
func (p *presenceTracker) decorate(snapshot *broadcast.TreeSnapshot, visible func(channelID int64) bool, now time.Time) {
	if snapshot == nil {
		return
	}
	present := map[string]bool{}
	var walk func([]*broadcast.ChannelNode)
	walk = func(nodes []*broadcast.ChannelNode) {
		for _, node := range nodes {
			for _, member := range node.Clients {
				present[member.ClientID] = true
			}
			walk(node.Children)
		}
	}
	walk(snapshot.RootChannels)
	for _, member := range snapshot.UnassignedClients {
		present[member.ClientID] = true
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pruneLocked(now)
	for id, departure := range p.departures {
		if departure.hidden || present[id] || !visible(departure.channelID) {
			continue
		}
		if snapshot.Departures == nil {
			snapshot.Departures = map[string]string{}
		}
		snapshot.Departures[id] = departure.reason
	}
	for id := range p.reconnected {
		if present[id] {
			snapshot.Reconnected = append(snapshot.Reconnected, id)
		}
	}
	slices.Sort(snapshot.Reconnected) // stable output keeps snapshot digests stable
}

func (p *presenceTracker) pruneLocked(now time.Time) {
	for id, departure := range p.departures {
		if now.Sub(departure.at) > presenceDepartureWindow {
			delete(p.departures, id)
		}
	}
	for key, at := range p.lost {
		if now.Sub(at) > presenceReconnectWindow {
			delete(p.lost, key)
		}
	}
	for id, at := range p.reconnected {
		if now.Sub(at) > presenceReconnectWindow {
			delete(p.reconnected, id)
		}
	}
}

func setBounded(m map[string]time.Time, key string, now time.Time) map[string]time.Time {
	if m == nil {
		m = map[string]time.Time{}
	}
	if _, ok := m[key]; !ok && len(m) >= presenceTrackerMax {
		for old := range m { // arbitrary eviction keeps the map bounded
			delete(m, old)
			break
		}
	}
	m[key] = now
	return m
}

// presenceSessionKey identifies one device of an account across sessions.
// deviceBound is false when only the account is known, which is too coarse
// to tell a reconnect from a second device that is already online.
func (c *Client) presenceSessionKey() (key string, deviceBound bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.UniqueID == "" {
		return "", false
	}
	device := c.deviceID
	if device == "" {
		device = c.authSigningKey
	}
	return c.UniqueID + "|" + device, device != ""
}

// departureReason classifies why an authenticated session ended.
func (c *Client) departureReason() string {
	if c.replaced.Load() {
		return netproto.DepartureReplaced
	}
	if c.saidGoodbye.Load() {
		return ""
	}
	var reason netproto.ControlCloseReason
	if read := c.readClose.Load(); read != nil {
		reason = *read
	}
	if cause := c.closeCause.Load(); cause != nil {
		reason = *cause
	}
	switch reason {
	case "", netproto.ControlCloseKicked, netproto.ControlCloseShutdown, netproto.ControlClosePreauthRejected, netproto.ControlCloseAuthTimeout:
		return ""
	case netproto.ControlCloseEOF:
		// A clean close is deliberate unless the client promised a goodbye first.
		c.mu.RLock()
		promised := c.announcesGoodbye
		c.mu.RUnlock()
		if !promised {
			return ""
		}
	}
	return netproto.DepartureConnectionLost
}

// markReplacedSessions flags older registered sessions of the same device as
// replaced by client and reports whether there were any.
func (s *TCPServer) markReplacedSessions(client *Client) bool {
	key, deviceBound := client.presenceSessionKey()
	if !deviceBound {
		return false
	}
	s.mu.RLock()
	others := make([]*Client, 0, 1)
	for _, other := range s.clients {
		if other != client {
			others = append(others, other)
		}
	}
	s.mu.RUnlock()
	found := false
	for _, other := range others {
		if otherKey, bound := other.presenceSessionKey(); bound && otherKey == key {
			other.replaced.Store(true)
			found = true
		}
	}
	return found
}
