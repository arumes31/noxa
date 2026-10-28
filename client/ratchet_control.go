package main

// ratchet_control.go: explicitly targeted, expiring media controls
// (1c-iii-b item 3). This is the API Task 2 (encrypted channel/call media
// key distribution) consumes; the wire contract is internal/e2ee/admission
// control.go.
//
// Sending: SendRatchetControl seals one control for exactly one device of
// one principal (the sender's own other device is allowed) through the same
// ratchet/admission machinery as chat. The server admits it only for that
// device's current directory state, with a deadline of at most
// admission.MaxControlTTL seconds, bounded per target device. There is no
// fanout to other devices and no reseal after rejection or abort.
//
// Receiving: an authenticated control is consumed and committed like any
// copy (session advance, one-time-key obligation, exact receive ACK). Only
// its disposition is kept: "delivered" (handed to OnRatchetControl
// handlers exactly once, when committed), "expired" or "invalid" (data
// dropped, acknowledged, never handed out, so an old key is never
// installed). Controls never appear in chat, activity, search, history
// transfer or export, and are never shown as placeholders.

import (
	"encoding/json"
	"log"
	"slices"

	"noxa/internal/e2ee/admission"
	"noxa/internal/e2ee/olmdir"
)

// RatchetControlIntent is one control to send.
type RatchetControlIntent struct {
	// LogicalID is a fresh unique ID per control (a reused ID returns the
	// original outcome, never a new control).
	LogicalID string
	Principal string // target principal (may be this account)
	Device    string // the single target device
	Body      admission.ControlBody
}

// RatchetControl is one delivered, authenticated, unexpired control.
type RatchetControl struct {
	Sender       string                 `json:"sender"`
	SenderDevice string                 `json:"sender_device"`
	LogicalID    string                 `json:"logical_id"`
	Kind         string                 `json:"kind"`
	Scope        admission.ControlScope `json:"scope"`
	Epoch        int64                  `json:"epoch"`
	ExpiresAt    int64                  `json:"expires_at"`
	Data         json.RawMessage        `json:"data,omitempty"`
	Delivery     string                 `json:"delivery_id"`
}

// ratchetControlDisposition classifies a received control body.
func ratchetControlDisposition(body json.RawMessage, now int64) string {
	b, err := admission.ParseControlBody(body)
	switch {
	case err != nil:
		return "invalid"
	case b.ExpiresAt <= now:
		return "expired"
	}
	return "delivered"
}

// onControl registers a handler for delivered controls.
func (c *ratchetConsumer) onControl(fn func(RatchetControl)) {
	c.serial.Lock()
	defer c.serial.Unlock()
	c.controls = append(c.controls, fn)
}

// emitControl hands a newly committed, delivered control from another
// device to the handlers. Replays (isNew=false), own sends and expired or
// invalid controls are never handed out.
func (c *ratchetConsumer) emitControl(rec RatchetRecord, isNew bool) error {
	if !isNew || rec.Status != "delivered" || rec.Transport.Delivery == "" || rec.SenderDevice == c.d.Namespace.Device {
		return nil
	}
	b, err := admission.ParseControlBody(rec.Body)
	if err != nil {
		return nil
	}
	out := RatchetControl{Sender: rec.Sender, SenderDevice: rec.SenderDevice, LogicalID: rec.LogicalID, Kind: b.Kind, Scope: b.Scope,
		Epoch: b.Epoch, ExpiresAt: b.ExpiresAt, Data: b.Data, Delivery: rec.Transport.Delivery}
	for _, fn := range c.controls {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("ratchet: control handler: %v", r)
				}
			}()
			fn(out)
		}()
		if err := c.check(); err != nil {
			return err
		}
	}
	return nil
}

// sendControl seals and admits one control.
func (c *ratchetConsumer) sendControl(in RatchetControlIntent) (RatchetOutcome, error) {
	if !olmdir.ValidText(in.Principal) || !olmdir.ValidText(in.Device) {
		return RatchetOutcome{}, rfail("invalid")
	}
	body, err := json.Marshal(in.Body)
	if err != nil {
		return RatchetOutcome{}, rfail("invalid")
	}
	return c.send(ratchetIntent{LogicalID: in.LogicalID, Scope: "direct", Peer: in.Principal, Device: in.Device, Purpose: "control", Body: body})
}

// SendRatchetControl sends one control on this connection's ratchet session.
// The outcome's Receipt state "accepted" means the server admitted it for
// the target device; any error is a coded ratchet error (ratchetCode).
func (m *connManager) SendRatchetControl(in RatchetControlIntent) (RatchetOutcome, error) {
	c, err := m.ratchetConsumerOn(nil)
	if err != nil {
		return RatchetOutcome{}, err
	}
	return c.sendControl(in)
}

// OnRatchetControl registers a handler for controls delivered to this
// device. Handlers run on the receive path after the durable commit and
// before the ACK; they must be quick and must not call back into the
// ratchet session. Registration survives reconnects.
func (m *connManager) OnRatchetControl(fn func(RatchetControl)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ratchetControls = append(m.ratchetControls, fn)
}

// dispatchRatchetControl fans a delivered control out to the handlers.
func (m *connManager) dispatchRatchetControl(ctl RatchetControl) {
	m.mu.Lock()
	handlers := slices.Clone(m.ratchetControls)
	m.mu.Unlock()
	for _, fn := range handlers {
		fn(ctl)
	}
}
