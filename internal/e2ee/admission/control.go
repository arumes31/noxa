package admission

import (
	"bytes"
	"encoding/json"

	"noxa/internal/e2ee/olmdir"
)

// Media controls (purpose "control") are short-lived, explicitly targeted
// operations for exactly one participating device (Task 2 media key
// distribution). They use the same ratchet, admission and receive machinery
// as chat, but:
//
//   - the manifest has scope "direct", no reference, and exactly one
//     non-local target, whose principal is the manifest's peer (the sender's
//     own other device is allowed: peer == sender). No implicit fanout to the
//     peer's or the sender's other devices;
//   - the server accepts a control whose single target matches its current
//     directory state (not the complete roster), with a deadline of at most
//     MaxControlTTL seconds, and bounds pending controls per target device;
//   - the core binding purpose is "media-key";
//   - the application body (ControlBody, inside the ciphertext) binds kind,
//     scope, epoch and expiry; receivers store an expired control's
//     disposition and ACK it without delivering its data;
//   - clients never show controls in chat, activity, search, history
//     transfer or export, and never reference them by edit/delete.

// MaxControlTTL bounds a control's admission deadline and its expiry.
const MaxControlTTL = 300

// MaxControlData bounds a control's opaque data.
const MaxControlData = 4096

// MaxPendingControls bounds pending (unacknowledged) controls per target device.
const MaxPendingControls = 64

// ControlScope names what a control is about (e.g. {"call", <id>} or
// {"channel", <id>}); the receiving application checks it is current.
type ControlScope struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// ControlBody is the authenticated application body of a control.
type ControlBody struct {
	Kind      string          `json:"kind"`
	Scope     ControlScope    `json:"scope"`
	Epoch     int64           `json:"epoch"`
	ExpiresAt int64           `json:"expires_at"`
	Data      json.RawMessage `json:"data,omitempty"`
}

func validControlWord(s string) bool {
	if len(s) < 1 || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

// ParseControlBody validates a control body's shape (not its expiry).
func ParseControlBody(body json.RawMessage) (ControlBody, error) {
	var b ControlBody
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if len(body) == 0 || len(body) > MaxControlData+1024 || d.Decode(&b) != nil || d.More() {
		return ControlBody{}, ErrInvalid
	}
	if !validControlWord(b.Kind) || !validControlWord(b.Scope.Type) || !olmdir.ValidText(b.Scope.ID) || b.Epoch < 0 || !safe(b.ExpiresAt) ||
		len(b.Data) > MaxControlData || len(b.Data) > 0 && !json.Valid(b.Data) {
		return ControlBody{}, ErrInvalid
	}
	return b, nil
}

// ValidateControlSend checks a control a sender is about to seal: valid,
// not expired, and expiring within MaxControlTTL.
func ValidateControlSend(body json.RawMessage, now int64) (ControlBody, error) {
	b, err := ParseControlBody(body)
	if err != nil || b.ExpiresAt <= now || b.ExpiresAt > now+MaxControlTTL {
		return ControlBody{}, ErrInvalid
	}
	return b, nil
}

// ControlTarget returns the single non-local target of a control manifest.
func ControlTarget(m Manifest) (Target, bool) {
	if m.Purpose != "control" || len(m.Targets) != 2 {
		return Target{}, false
	}
	for _, t := range m.Targets {
		if !t.Local {
			return t, t.Principal == m.Peer
		}
	}
	return Target{}, false
}
