// session_resume.go lets a session survive a lost connection or an update
// restart: the server keeps its place for a short grace window and a client
// that logs in again and presents the session's resume token continues it
// with the same client ID, channel, voice state and stream watches.
package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"time"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"noxa/internal/state"
)

const (
	// resumeGraceWindow is how long a lost or updating session keeps its
	// place, awaiting a resume, before it becomes an ordinary leave.
	resumeGraceWindow = 2 * time.Minute
	// resumeTokenMax bounds the store (one entry per live or parked session).
	// Expired entries are evicted first; beyond the cap new sessions simply
	// get no token and reconnect as before.
	resumeTokenMax = 16384
	// resumeTakeoverWait bounds the wait for a half-open old connection of
	// the resuming session to finish its cleanup.
	resumeTakeoverWait = 5 * time.Second
	// eventStreamWatchResume asks a client to watch a publisher again once its
	// rebuilt peer republishes (or, for the resumed client, once listed).
	eventStreamWatchResume = "stream_watch_resume"
)

// resumeWatch names one active stream watch: the other party and the slot.
type resumeWatch struct {
	ClientID string `json:"client_id"`
	Slot     string `json:"slot"`
}

// resumeParked is what a departed session carries into its resume.
type resumeParked struct {
	clientID        string
	uniqueID        string
	channelID       int64
	channelPassword string // the channel's password hash at departure
	selfMuted       bool
	selfDeafened    bool
	serverMuted     bool
	serverDeafened  bool
	watching        []resumeWatch // publishers this session watched
	watchers        []resumeWatch // sessions watching this one
}

type resumeEntry struct {
	token    [32]byte
	hash     [32]byte
	clientID string
	uniqueID string
	binding  string
	owner    *Client       // the session that holds the token
	parked   *resumeParked // set when the session departed
	at       time.Time     // when it was parked
	claimed  bool          // a resume is taking over the live owner
	standby  *Client       // a handover standby waiting to commit (update_handover.go)
}

// resumeStore holds resume tokens in memory only: a server restart forgets
// them, and every client then reconnects as a new session. The zero value is
// ready to use.
type resumeStore struct {
	mu       sync.Mutex
	now      func() time.Time // tests replace the clock
	byHash   map[[32]byte]*resumeEntry
	byClient map[string]*resumeEntry
}

func (r *resumeStore) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

// resumeBinding is what a token is bound to: the account and the signing key
// that proved it (the key also determines the linked device). An ephemeral
// guest has neither, so its token alone is the session secret; it binds to
// "being an ephemeral guest" and the resume restores its old guest identity.
func resumeBinding(uniqueID, signingKey string) string {
	if signingKey == "" && strings.HasPrefix(uniqueID, "guest:") {
		return "guest:"
	}
	return uniqueID + "|" + signingKey
}

// issue returns a fresh token for owner, replacing any earlier one of the
// same client ID. Empty when the store is full.
func (r *resumeStore) issue(owner *Client, uniqueID, binding string) string {
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byHash == nil {
		r.byHash = map[[32]byte]*resumeEntry{}
		r.byClient = map[string]*resumeEntry{}
	}
	if old := r.byClient[owner.ID]; old != nil {
		r.deleteLocked(old)
	}
	if len(r.byClient) >= resumeTokenMax {
		r.pruneLocked(r.clock())
		if len(r.byClient) >= resumeTokenMax {
			return ""
		}
	}
	entry := &resumeEntry{token: token, hash: sha256.Sum256(token[:]), clientID: owner.ID, uniqueID: uniqueID, binding: binding, owner: owner}
	r.byHash[entry.hash] = entry
	r.byClient[owner.ID] = entry
	return base64.RawURLEncoding.EncodeToString(token[:])
}

// claim spends a presented token. It succeeds only for the bound identity
// within the grace window; a mismatch leaves the token untouched. The token
// is single use from here on. The caller closes the returned owner (a
// half-open old connection, or one still cleaning up) and waits for it to be
// gone before takeParked, so no late cleanup of the old session can touch
// the client ID the resume reuses.
func (r *resumeStore) claim(presented, binding string) (clientID string, owner *Client, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry := r.matchLocked(presented, binding)
	if entry == nil {
		return "", nil, false
	}
	if entry.parked != nil && r.clock().Sub(entry.at) > resumeGraceWindow {
		r.deleteLocked(entry)
		return "", nil, false
	}
	delete(r.byHash, entry.hash)
	entry.claimed = true
	return entry.clientID, entry.owner, true
}

// matchLocked returns the unclaimed entry of a presented token bound to
// binding, or nil. A mismatch leaves the entry untouched.
func (r *resumeStore) matchLocked(presented, binding string) *resumeEntry {
	raw, err := base64.RawURLEncoding.DecodeString(presented)
	if err != nil || len(raw) != 32 {
		return nil
	}
	entry := r.byHash[sha256.Sum256(raw)]
	if entry == nil || entry.claimed {
		return nil
	}
	tokenOK := subtle.ConstantTimeCompare(entry.token[:], raw)
	bindingOK := subtle.ConstantTimeCompare([]byte(entry.binding), []byte(binding))
	if tokenOK&bindingOK != 1 {
		return nil
	}
	return entry
}

// takeParked hands over the entry a claimed owner parked while closing.
func (r *resumeStore) takeParked(clientID string) *resumeEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry := r.byClient[clientID]
	if entry == nil || !entry.claimed || entry.parked == nil {
		return nil
	}
	r.deleteLocked(entry)
	return entry
}

// abandon releases a claim whose takeover did not finish: a parked entry
// becomes expirable again (its token stays spent), a live one is dropped so
// its owner later leaves normally.
func (r *resumeStore) abandon(clientID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry := r.byClient[clientID]
	if entry == nil || !entry.claimed {
		return
	}
	if entry.parked == nil {
		r.deleteLocked(entry)
		return
	}
	entry.claimed = false
}

// reinstate puts a taken entry back, token included, when the resuming login
// was refused afterwards (e.g. the server was full), so a retry within the
// grace window can still resume. The original park time is kept.
func (r *resumeStore) reinstate(entry *resumeEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byClient == nil || r.byClient[entry.clientID] != nil || r.clock().Sub(entry.at) > resumeGraceWindow {
		return
	}
	entry.claimed = false
	r.byHash[entry.hash] = entry
	r.byClient[entry.clientID] = entry
}

// park keeps a departed session's state for the grace window. It reports
// false when owner holds no token (none was issued, or it was revoked).
func (r *resumeStore) park(owner *Client, parked *resumeParked, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry := r.byClient[owner.ID]
	if entry == nil || entry.owner != owner {
		return false
	}
	entry.parked, entry.at = parked, now
	return true
}

// revoke forgets owner's token (explicit leave, kick, ban, revocation).
func (r *resumeStore) revoke(owner *Client) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if entry := r.byClient[owner.ID]; entry != nil && entry.owner == owner {
		r.deleteLocked(entry)
	}
}

// revokeAccount forgets every token of an account, live or parked.
func (r *resumeStore) revokeAccount(uniqueID string) {
	if uniqueID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, entry := range r.byClient {
		if entry.uniqueID == uniqueID {
			r.deleteLocked(entry)
		}
	}
}

// expire ends the parked entry of clientID parked at at, if still there.
func (r *resumeStore) expire(clientID string, at time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry := r.byClient[clientID]
	if entry == nil || entry.parked == nil || entry.claimed || !entry.at.Equal(at) {
		return false
	}
	r.deleteLocked(entry)
	return true
}

func (r *resumeStore) len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.byClient)
}

func (r *resumeStore) pruneLocked(now time.Time) {
	for _, entry := range r.byClient {
		if entry.parked != nil && !entry.claimed && now.Sub(entry.at) > resumeGraceWindow {
			r.deleteLocked(entry)
		}
	}
}

func (r *resumeStore) deleteLocked(entry *resumeEntry) {
	if r.byHash[entry.hash] == entry {
		delete(r.byHash, entry.hash)
	}
	if r.byClient[entry.clientID] == entry {
		delete(r.byClient, entry.clientID)
	}
}

// --- Client side of the server --------------------------------------------

func (c *Client) setResumeOffer(token string, announced bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resumeOffer, c.announcesResume = token, announced
}

func (c *Client) takeResumeOffer() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	token := c.resumeOffer
	c.resumeOffer = ""
	return token
}

func (c *Client) resumable() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.announcesResume
}

// claimResume spends the client's presented token, if any, before its
// session is published. A half-open old connection of the same session is
// closed silently, like a replaced one, and its cleanup awaited.
func (s *TCPServer) claimResume(ctx context.Context, client *Client, uniqueID string) *resumeEntry {
	token := client.takeResumeOffer()
	if token == "" {
		return nil
	}
	clientID, owner, ok := s.resume.claim(token, resumeBinding(uniqueID, client.sessionKey()))
	if !ok {
		return nil
	}
	if owner == nil || owner == client {
		s.resume.abandon(clientID)
		return nil
	}
	// A no-op for a session that already departed; a still open one closes
	// silently and parks for this resume.
	owner.resumeTakeover.Store(true)
	_ = owner.Conn.Close()
	if owner.gone != nil {
		timer := time.NewTimer(resumeTakeoverWait)
		defer timer.Stop()
		select {
		case <-owner.gone:
		case <-timer.C:
			s.resume.abandon(clientID)
			return nil
		case <-ctx.Done():
			s.resume.abandon(clientID)
			return nil
		}
	}
	entry := s.resume.takeParked(clientID)
	if entry == nil {
		s.resume.abandon(clientID)
	}
	return entry
}

// adoptClientID gives a resuming session its old client ID before it is
// published. It fails when that ID is still registered.
func (s *TCPServer) adoptClientID(client *Client, id string) bool {
	if id == "" || id == client.ID {
		return id != ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, taken := s.clients[id]; taken {
		return false
	}
	if s.clients[client.ID] == client {
		delete(s.clients, client.ID)
	}
	client.mu.Lock()
	client.ID = id
	client.mu.Unlock()
	s.clients[id] = client
	return true
}

// parkSession decides whether a departing session keeps its place and, if
// so, parks its state. It returns the kept departure reason (updating or
// reconnecting) or "" for an ordinary departure, whose token it revokes.
func (s *TCPServer) parkSession(client *Client, removed *state.Client, reason string, revoked bool, now time.Time) string {
	kept := ""
	switch {
	case revoked:
	case client.resumeTakeover.Load():
		kept = netproto.DepartureReconnecting
	case reason == netproto.DepartureUpdating:
		kept = reason
	case reason == netproto.DepartureConnectionLost && client.resumable():
		kept = netproto.DepartureReconnecting
	}
	if kept == "" {
		s.resume.revoke(client)
		return ""
	}
	parked := &resumeParked{
		clientID:       client.ID,
		uniqueID:       removed.UniqueID,
		channelID:      removed.ChannelID,
		selfMuted:      removed.SelfMuted,
		selfDeafened:   removed.SelfDeafened,
		serverMuted:    removed.ServerMuted,
		serverDeafened: removed.ServerDeafened,
	}
	if ch, ok := s.deps.State.GetChannel(removed.ChannelID); ok {
		parked.channelPassword = ch.PasswordHash
	}
	if pairs, ok := s.deps.Voice.(interface {
		VideoWatchPairs(string) (watching, watchers [][2]string)
	}); ok {
		watching, watchers := pairs.VideoWatchPairs(client.ID)
		for _, w := range watching {
			parked.watching = append(parked.watching, resumeWatch{w[0], w[1]})
		}
		for _, w := range watchers {
			parked.watchers = append(parked.watchers, resumeWatch{w[0], w[1]})
		}
	}
	if !s.resume.park(client, parked, now) {
		return ""
	}
	return kept
}

// restoreResumedSession puts a resumed session back into its channel with its
// voice state, quietly: no client write and no broadcast, since the session's
// snapshot and the observers' kept place already show it there. Authorization
// is evaluated afresh; a channel the session may no longer enter is skipped.
// The caller holds the role policy lease and the metadata lock.
func (s *TCPServer) restoreResumedSession(ctx context.Context, client *Client, parked *resumeParked, guest bool) {
	if s.deps.State == nil {
		return
	}
	s.deps.State.SetAudioState(client.ID, parked.selfMuted, parked.selfDeafened)
	if parked.serverMuted || parked.serverDeafened {
		muted, deafened := parked.serverMuted, parked.serverDeafened
		_, _ = s.deps.State.SetServerVoiceState(client.ID, &muted, &deafened)
	}
	lease, ok := ctx.Value(roleLeaseKey{}).(roleLease)
	if parked.channelID <= 0 || !ok {
		return
	}
	channelID, userID := parked.channelID, client.userID()
	ch, exists := s.deps.State.GetChannel(channelID)
	if !exists || ch.TextOnly || !lease.evaluator.Evaluate(userID, channelID, authorization.Connect).Allowed {
		return
	}
	if ch.PasswordHash != "" && ch.PasswordHash != parked.channelPassword && !lease.evaluator.Evaluate(userID, channelID, authorization.BypassChannelPassword).Allowed {
		return
	}
	if s.rulesOwed(ctx, client, guest) {
		return
	}
	client.lockRoleAction()
	defer client.unlockRoleAction()
	undoSeat, err := s.carryMediaSeat(client.ID, channelID)
	if err != nil {
		return
	}
	joined := func(int64) {
		// The client rebuilds its peer: connecting until the SFU reports it.
		s.markVoiceConnecting(client.ID, false)
		if s.deps.Voice != nil {
			s.refreshRolePublishers(lease.evaluator)
			s.deps.Voice.JoinChannel(client.ID, channelID)
		}
	}
	if s.deps.Channels != nil {
		backend, ok := s.deps.Channels.(interface {
			MoveClientWithinCapacity(string, int64, func(int64)) (int64, error)
		})
		if ok {
			_, err = backend.MoveClientWithinCapacity(client.ID, channelID, joined)
		} else {
			err = authorization.ErrAuthorizationUnavailable
		}
	} else if err = s.deps.State.MoveClientWithinCapacity(client.ID, channelID); err == nil {
		joined(0)
	}
	if err != nil {
		undoSeat()
	}
}

// rulesOwed reports whether this session would have to accept the rules
// before entering a channel; a failed lookup counts as owed.
func (s *TCPServer) rulesOwed(ctx context.Context, client *Client, guest bool) bool {
	if s.deps.Rules == nil {
		return false
	}
	if guest {
		_, hash, err := s.deps.Rules.Text(ctx)
		return err != nil || hash != ""
	}
	_, _, pending, err := s.deps.Rules.Pending(ctx, client.userID())
	return err != nil || pending
}

// resumeStreamWatches asks the parties of the session's former watches to
// watch again: the resumed client its publishers, and its watchers the
// resumed client once its rebuilt peer publishes again. Clients keep these
// requests only briefly and still go through the normal watch request.
func (s *TCPServer) resumeStreamWatches(client *Client, parked *resumeParked) {
	if s.deps.Broadcast == nil {
		return
	}
	send := func(recipient, publisher, slot string) {
		if payload, err := eventEnvelope(eventStreamWatchResume, map[string]string{"publisher_id": publisher, "slot": slot}); err == nil {
			_ = s.deps.Broadcast.BroadcastToClient(recipient, payload)
		}
	}
	for _, w := range parked.watching {
		send(client.ID, w.ClientID, w.Slot)
	}
	for _, w := range parked.watchers {
		if w.ClientID != client.ID {
			send(w.ClientID, client.ID, w.Slot)
		}
	}
}

// streamWatchResumeVisible validates a stream_watch_resume delivery at send
// time: it names another, currently connected publisher in a channel the
// recipient may view, and an invisible publisher only to recipients allowed
// to see connection info. A stale hint never reveals a member.
func (s *TCPServer) streamWatchResumeVisible(recipient *Client, e *authorization.RoleEvaluator, data json.RawMessage) bool {
	var event struct {
		PublisherID string `json:"publisher_id"`
		Slot        string `json:"slot"`
	}
	if json.Unmarshal(data, &event) != nil || event.PublisherID == "" || event.PublisherID == recipient.ID ||
		!slices.Contains([]string{"screen", "cam"}, event.Slot) || s.deps == nil || s.deps.State == nil {
		return false
	}
	publisher, ok := s.deps.State.GetClient(event.PublisherID)
	if !ok || publisher.ChannelID <= 0 || !e.Evaluate(recipient.userID(), publisher.ChannelID, authorization.ViewChannel).Allowed {
		return false
	}
	return !s.deps.State.PresenceHidden(publisher) || publisher.UniqueID == recipient.uniqueID() ||
		e.Evaluate(recipient.userID(), 0, authorization.ViewConnectionInfo).Allowed
}

// clientInChannel reports whether the session is a member of channelID now.
func (s *TCPServer) clientInChannel(clientID string, channelID int64) bool {
	member, present := s.deps.State.GetClient(clientID)
	return present && channelID > 0 && member.ChannelID == channelID
}
