package main

// call_media_e2ee.go: media-key targeting for end-to-end encrypted private
// calls (#12).
//
// Private-call media keys use the same "media-key" ratchet control as channel
// media, with the scope {type: "call-media", id: <call id>}. As for channels,
// the native side refuses a target the page could not legitimately address:
// the server's current call state must show this session as the accepted
// participant that answered, and the target as another accepted participant
// that answered on exactly that device. The state is fetched from the server
// (briefly cached), never taken from the page.

import (
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"

	"noxa/internal/netproto"
)

const callMediaScopeType = "call-media"

// callMediaStateTTL bounds how long one fetched call state answers target
// checks: a rekey sends one control per participant within milliseconds.
const callMediaStateTTL = 2 * time.Second

var (
	errCallMediaScope  = errors.New("media key scope is not your current private call")
	errCallMediaTarget = errors.New("media key target is not an accepted device of your private call")
)

type callMediaStateCache struct {
	mu      sync.Mutex
	id      string
	call    netproto.CallSession
	fetched time.Time
}

// callMediaState returns the server's current state of a call this session
// can read, reusing a state fetched within callMediaStateTTL unless fresh is
// set. cached reports whether the state came from the cache.
func (m *connManager) callMediaState(id string, fresh bool) (call netproto.CallSession, cached bool, err error) {
	c := &m.callMediaCache
	c.mu.Lock()
	if !fresh && c.id == id && time.Since(c.fetched) < callMediaStateTTL {
		call := c.call
		c.mu.Unlock()
		return call, true, nil
	}
	c.mu.Unlock()
	result, err := m.privateCallRequest(netproto.CallRequest{Action: "get", ID: id})
	if err != nil || result.Call == nil {
		return netproto.CallSession{}, false, errCallMediaScope
	}
	c.mu.Lock()
	c.id, c.call, c.fetched = id, *result.Call, time.Now()
	c.mu.Unlock()
	return *result.Call, false, nil
}

// checkCallMediaKeyTarget allows a call media key only within a live call in
// which this session answered and only to another participant's answering
// device.
func (m *connManager) checkCallMediaKeyTarget(req MediaKeyRequest) error {
	parsed, err := uuid.Parse(req.Scope.ID)
	if err != nil || parsed.String() != req.Scope.ID {
		return errCallMediaScope
	}
	if req.Principal == "" || req.Device == "" {
		return errCallMediaTarget
	}
	m.mu.Lock()
	uid, clientID, deviceID := m.uniqueID, m.clientID, m.deviceID
	m.mu.Unlock()
	if uid == "" || clientID == "" || deviceID == "" || req.Device == deviceID {
		return errCallMediaTarget
	}
	call, cached, err := m.callMediaState(req.Scope.ID, false)
	if err != nil {
		return err
	}
	err = callMediaTargetAllowed(call, uid, clientID, deviceID, req.Principal, req.Device)
	if err != nil && cached {
		// A participant who answered after the cached state: ask the server
		// once more before refusing. The check still fails closed.
		if call, _, err = m.callMediaState(req.Scope.ID, true); err != nil {
			return err
		}
		err = callMediaTargetAllowed(call, uid, clientID, deviceID, req.Principal, req.Device)
	}
	return err
}

// callMediaTargetAllowed is the pure check behind checkCallMediaKeyTarget.
func callMediaTargetAllowed(call netproto.CallSession, uid, clientID, deviceID, principal, device string) error {
	if call.EndedAt != 0 {
		return errCallMediaScope
	}
	self, ok := call.Participant(uid)
	if !ok || self.State != "accepted" || self.ClientID != clientID || (self.DeviceID != "" && self.DeviceID != deviceID) {
		return errCallMediaScope
	}
	if principal == uid {
		return errCallMediaTarget
	}
	target, ok := call.Participant(principal)
	if !ok || target.State != "accepted" || target.ClientID == "" || target.DeviceID == "" || target.DeviceID != device {
		return errCallMediaTarget
	}
	return nil
}
