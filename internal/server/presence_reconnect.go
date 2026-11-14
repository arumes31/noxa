package server

import (
	"slices"
	"sync"
	"time"

	"noxa/internal/broadcast"
	"noxa/internal/netproto"
	"noxa/internal/state"
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
	// presenceStaleAfter: a live session answers the 15 s server ping, so an
	// older session of the same device silent this long is a dead link that a
	// reconnect superseded, not a parallel tab.
	presenceStaleAfter = 25 * time.Second
	// presenceUpdateWindow keeps the place of a member that quit to install an
	// update; a return within it is quiet, otherwise it becomes a leave.
	presenceUpdateWindow = 60 * time.Second
)

// presenceUpdating is the kept place of a member restarting into an update.
type presenceUpdating struct {
	ghost  broadcast.ClientInfo
	hidden bool
	at     time.Time
}

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
	updating    map[string]presenceUpdating  // session key -> kept place while updating
	returned    map[string]time.Time         // client_id -> when it came back from an update
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
		case "", netproto.DepartureUpdating:
			delete(p.lost, key)
		}
	}
	// An updating member keeps its place (noteUpdating) instead of a departure.
	if reason != "" && reason != netproto.DepartureUpdating {
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

// noteUpdating keeps the place of a member that quit to install an update.
func (p *presenceTracker) noteUpdating(key string, ghost broadcast.ClientInfo, hidden bool, now time.Time) {
	if key == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pruneLocked(now)
	if p.updating == nil {
		p.updating = map[string]presenceUpdating{}
	}
	if _, ok := p.updating[key]; !ok && len(p.updating) >= presenceTrackerMax {
		for old := range p.updating { // arbitrary eviction keeps the map bounded
			delete(p.updating, old)
			break
		}
	}
	ghost.Updating = true
	p.updating[key] = presenceUpdating{ghost: ghost, hidden: hidden, at: now}
}

// noteReturn reports whether a new session is the quiet return of a member
// that was updating. The kept place then vanishes as replaced.
func (p *presenceTracker) noteReturn(clientID, key string, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pruneLocked(now)
	kept, ok := p.updating[key]
	if key == "" || !ok || now.Sub(kept.at) > presenceUpdateWindow {
		return false
	}
	delete(p.updating, key)
	if p.departures == nil {
		p.departures = map[string]presenceDeparture{}
	}
	p.departures[kept.ghost.ClientID] = presenceDeparture{reason: netproto.DepartureReplaced, channelID: kept.ghost.ChannelID, hidden: kept.hidden, at: now}
	p.returned = setBounded(p.returned, clientID, now)
	return true
}

// expireUpdating ends the kept place of clientID that did not come back and
// reports whether it was still kept (its end is then an ordinary leave) and
// whether the member was invisible.
func (p *presenceTracker) expireUpdating(key, clientID string) (expired, hidden bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	kept, ok := p.updating[key]
	if !ok || kept.ghost.ClientID != clientID {
		return false, false
	}
	delete(p.updating, key)
	return true, kept.hidden
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
// members that just reconnected or returned from an update, and keeps the
// places of members still updating. visible decides channel visibility.
func (p *presenceTracker) decorate(snapshot *broadcast.TreeSnapshot, visible func(channelID int64) bool, recipientKey string, now time.Time) {
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
	p.placeUpdatingLocked(snapshot, present, visible, recipientKey, now)
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
	for id := range p.returned {
		if present[id] {
			snapshot.Returned = append(snapshot.Returned, id)
		}
	}
	slices.Sort(snapshot.Reconnected) // stable output keeps snapshot digests stable
	slices.Sort(snapshot.Returned)
}

// placeUpdatingLocked puts the kept places the recipient may see back into
// their channels, marked as updating. The recipient's own device is left out:
// its new session must not see its old one.
func (p *presenceTracker) placeUpdatingLocked(snapshot *broadcast.TreeSnapshot, present map[string]bool, visible func(int64) bool, recipientKey string, now time.Time) {
	keys := make([]string, 0, len(p.updating))
	for key := range p.updating {
		keys = append(keys, key)
	}
	slices.Sort(keys) // stable output keeps snapshot digests stable
	for _, key := range keys {
		kept := p.updating[key]
		if key == recipientKey || kept.hidden || present[kept.ghost.ClientID] || now.Sub(kept.at) > presenceUpdateWindow || !visible(kept.ghost.ChannelID) {
			continue
		}
		ghost := kept.ghost
		if ghost.ChannelID <= 0 {
			snapshot.UnassignedClients = append(snapshot.UnassignedClients, &ghost)
		} else if node := findChannelNode(snapshot.RootChannels, ghost.ChannelID); node != nil {
			node.Clients = append(node.Clients, &ghost)
		} else {
			continue
		}
		present[ghost.ClientID] = true
	}
}

func findChannelNode(nodes []*broadcast.ChannelNode, channelID int64) *broadcast.ChannelNode {
	for _, node := range nodes {
		if node.ChannelID == channelID {
			return node
		}
		if found := findChannelNode(node.Children, channelID); found != nil {
			return found
		}
	}
	return nil
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
	for id, at := range p.returned {
		if now.Sub(at) > presenceReconnectWindow {
			delete(p.returned, id)
		}
	}
	for key, kept := range p.updating {
		// Normally expireUpdating ends it on time; this only bounds a missed timer.
		if now.Sub(kept.at) > 2*presenceUpdateWindow {
			delete(p.updating, key)
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
		if c.updating.Load() {
			return netproto.DepartureUpdating
		}
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

// markReplacedSessions flags older, silent sessions of the same device as
// replaced by client and reports whether there were any. Sessions still
// talking are parallel (a second tab) and stay untouched.
func (s *TCPServer) markReplacedSessions(client *Client, now time.Time) bool {
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
		if otherKey, bound := other.presenceSessionKey(); bound && otherKey == key && now.Sub(other.lastActivity()) > presenceStaleAfter {
			other.replaced.Store(true)
			found = true
		}
	}
	return found
}

// updatingGhost is the display-only copy of a departed member that keeps its
// place while it updates. Media and presence tokens are left out on purpose.
func updatingGhost(member *state.Client) broadcast.ClientInfo {
	return broadcast.ClientInfo{
		ClientID:      member.ClientID,
		UserID:        member.UserID,
		IsGuest:       member.UserID == 0,
		UniqueID:      member.UniqueID,
		Nickname:      member.Nickname,
		ChannelID:     member.ChannelID,
		ConnectedAt:   member.ConnectedAt,
		Status:        member.Status,
		StatusMessage: member.StatusMessage,
		IsBot:         member.IsBot,
		OnMobile:      member.OnMobile,
	}
}

// expireUpdatingDeparture turns a kept place whose member did not come back
// into an ordinary leave.
func (s *TCPServer) expireUpdatingDeparture(key, clientID, uniqueID, nickname string) {
	expired, hidden := s.presence.expireUpdating(key, clientID)
	if !expired {
		return // came back in time
	}
	if hidden {
		// An invisible member's leave is admin-only (381), as in onDisconnect.
		// Role snapshots filter it per recipient; the legacy event names nobody.
		s.broadcastToAdmins(eventUserLeft, userEvent{ClientID: clientID})
		return
	}
	s.broadcastEvent(eventUserLeft, userEvent{ClientID: clientID, UniqueID: uniqueID, Nickname: nickname})
}
