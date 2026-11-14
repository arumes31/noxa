// update_handover.go keeps the new process of an updating desktop in standby
// until it commits, then moves the session to it (handover-v1). The standby
// proves the same account and device and presents the live session's resume
// token, but stays unauthenticated: it holds no client ID, channel or
// presence and receives nothing. MsgHandoverCommit completes it through the
// regular resume path, which closes the old connection silently.
package server

import (
	"context"
	"errors"
	"time"

	"noxa/internal/netproto"
)

// handoverStandbyWindow bounds a standby; past it the connection is closed and
// the old session simply continues. Tests shorten it.
var handoverStandbyWindow = 20 * time.Second

// handoverCommitWindow bounds an accepted commit: closing the old session and
// completing the login.
var handoverCommitWindow = 15 * time.Second

var errHandoverUnavailable = errors.New(netproto.ReasonHandoverUnavailable)

// handoverStandby is a connection's handover login. A requested login has a
// zero deadline; a standby has its token, target session and deadline.
type handoverStandby struct {
	token      string
	clientID   string
	identity   authIdentity
	deadline   time.Time
	committing bool
	slotHeld   bool // the old session's slot is reserved for this commit
}

// requestHandover records whether this Authenticate asks for a standby. It
// fails for a connection that is already in standby.
func (c *Client) requestHandover(requested bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.handover != nil && !c.handover.deadline.IsZero() {
		return false
	}
	c.handover = nil
	if requested {
		c.handover = &handoverStandby{}
	}
	return true
}

func (c *Client) handoverRequested() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.handover != nil && c.handover.deadline.IsZero()
}

// inHandoverStandby reports a standby that has not started its commit.
func (c *Client) inHandoverStandby() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.handover != nil && !c.handover.deadline.IsZero() && !c.handover.committing
}

// handoverToken is the committing standby's token, empty otherwise.
func (c *Client) handoverToken() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.handover == nil || !c.handover.committing {
		return ""
	}
	return c.handover.token
}

// releaseHandoverSlot drops a commit's slot reservation once.
func (s *TCPServer) releaseHandoverSlot(c *Client) {
	c.mu.Lock()
	held := c.handover != nil && c.handover.slotHeld
	var id string
	if held {
		c.handover.slotHeld = false
		id = c.handover.clientID
	}
	c.mu.Unlock()
	if held {
		s.mu.Lock()
		if s.handoverReserved[id]--; s.handoverReserved[id] <= 0 {
			delete(s.handoverReserved, id)
		}
		s.mu.Unlock()
	}
}

// reserveHandoverSlot keeps clientID's slot counted across its takeover.
func (s *TCPServer) reserveHandoverSlot(clientID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.handoverReserved == nil {
		s.handoverReserved = map[string]int{}
	}
	s.handoverReserved[clientID]++
}

// sessionCount counts distinct sessions for the capacity limit: authenticated
// client IDs and IDs reserved for handover commits, except excluding.
func (s *TCPServer) sessionCount(excluding string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make(map[string]struct{}, len(s.clients)+len(s.handoverReserved))
	for id, c := range s.clients {
		if c.isAuthed() {
			ids[id] = struct{}{}
		}
	}
	for id := range s.handoverReserved {
		ids[id] = struct{}{}
	}
	delete(ids, excluding)
	return len(ids)
}

// handoverTarget is the session a committing standby takes over, else "".
func (c *Client) handoverTarget() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.handover == nil || !c.handover.committing {
		return ""
	}
	return c.handover.clientID
}

func (c *Client) handoverCommitting() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.handover != nil && c.handover.committing
}

// handoverDeadline is the standby's end, zero outside a standby.
func (c *Client) handoverDeadline() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.handover == nil || c.handover.committing {
		return time.Time{}
	}
	return c.handover.deadline
}

// dropHandover clears the connection's handover state and returns the session
// a still pending standby was waiting for.
func (c *Client) dropHandover() (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	h := c.handover
	c.handover = nil
	if h == nil || h.deadline.IsZero() {
		return "", false
	}
	return h.clientID, true
}

// beginHandover marks the token's live session as having a pending standby.
// Nothing is spent: the token, the owner and its state stay as they are until
// the commit claims them. A parked or claimed session, a mismatched identity
// or a second pending standby is refused.
func (r *resumeStore) beginHandover(presented, binding string, standby *Client) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry := r.matchLocked(presented, binding)
	if entry == nil || entry.parked != nil || entry.owner == nil || entry.standby != nil {
		return "", false
	}
	entry.standby = standby
	return entry.clientID, true
}

// endHandover releases standby's mark on clientID's entry, if still there.
func (r *resumeStore) endHandover(clientID string, standby *Client) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if entry := r.byClient[clientID]; entry != nil && entry.standby == standby {
		entry.standby = nil
	}
}

// enterHandoverStandby answers a handover login whose identity checks passed.
func (s *TCPServer) enterHandoverStandby(client *Client, id authIdentity) error {
	token := client.takeResumeOffer()
	clientID, ok := s.resume.beginHandover(token, resumeBinding(id.uniqueID, client.sessionKey()), client)
	if !ok {
		client.dropHandover()
		_ = s.writeMessage(client, netproto.MsgAuthResponse, netproto.AuthResponse{Reason: netproto.ReasonHandoverUnavailable})
		return errHandoverUnavailable
	}
	client.mu.Lock()
	client.handover = &handoverStandby{token: token, clientID: clientID, identity: id, deadline: time.Now().Add(handoverStandbyWindow)}
	client.mu.Unlock()
	return s.writeMessage(client, netproto.MsgAuthResponse, netproto.AuthResponse{
		AuthorizationModel: s.requiredAuthorizationModel(),
		OK:                 true,
		Standby:            true,
		ClientID:           clientID,
		UniqueID:           id.uniqueID,
	})
}

// handleHandoverCommit completes a standby login as a resume of its session.
// The claim happens only now; a refused commit closes the standby and leaves
// a still live old session untouched.
func (s *TCPServer) handleHandoverCommit(ctx context.Context, client *Client) error {
	client.mu.Lock()
	h := client.handover
	ok := h != nil && !h.deadline.IsZero() && !h.committing && time.Now().Before(h.deadline)
	var standby handoverStandby
	if ok {
		h.committing = true
		standby = *h
	}
	client.mu.Unlock()
	if h == nil {
		return s.sendErrorFor(client, netproto.MsgHandoverCommit, errCodeMalformed, "no update handover is pending")
	}
	if !ok {
		_ = s.writeMessage(client, netproto.MsgAuthResponse, netproto.AuthResponse{Reason: netproto.ReasonHandoverUnavailable})
		return errHandoverUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, handoverCommitWindow)
	defer cancel()
	// Admission first: a commit refused for a ban or a signed-out device must
	// leave the live old session untouched. Capacity cannot refuse it: the
	// old session's slot is reserved for it below.
	if reason, err := s.handoverAdmission(ctx, client, standby.identity.uniqueID); reason != "" || err != nil {
		if reason == "" {
			reason = "internal error"
		}
		client.dropHandover()
		s.resume.endHandover(standby.clientID, client)
		_ = s.writeMessage(client, netproto.MsgAuthResponse, netproto.AuthResponse{Reason: reason})
		return errHandoverUnavailable
	}
	s.resume.endHandover(standby.clientID, client)
	client.setResumeOffer(standby.token, true)
	// The old session's slot passes to this commit: reserved before the old
	// connection closes, released when this session is published (or fails).
	client.mu.Lock()
	client.handover.slotHeld = true
	client.mu.Unlock()
	s.reserveHandoverSlot(standby.clientID)
	err := s.finishAuth(ctx, client, standby.identity)
	s.releaseHandoverSlot(client)
	client.dropHandover()
	return err
}

// handoverAdmission runs the identity checks a commit must pass before the
// old session is closed. finishAuth repeats them atomically afterwards.
func (s *TCPServer) handoverAdmission(ctx context.Context, client *Client, uniqueID string) (string, error) {
	if s.deps == nil || s.deps.Auth == nil {
		return "", errHandoverUnavailable
	}
	if reason, err := s.banRejectReason(ctx, client, uniqueID, remoteIP(client.Conn)); reason != "" || err != nil {
		return reason, err
	}
	return s.sessionDeviceRejection(ctx, client, uniqueID)
}

// afterHandoverTakeover lets tests act between the old session's cleanup and
// the commit's publication.
var afterHandoverTakeover func()
