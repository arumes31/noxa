package admission

import (
	"bytes"
	"encoding/json"
	"regexp"

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

// MaxPendingControls bounds pending (unacknowledged) controls per sender
// device and target device, so one sender's unreadable controls can never
// block other senders' controls to that device.
const MaxPendingControls = 64

// ControlPendingTTL: a control copy still pending this many seconds after
// its manifest deadline is cleared by server maintenance (unless it opened a
// device-pair session), so unreadable controls cannot accumulate.
const ControlPendingTTL = 86400

// MaxControlEpoch keeps epochs JS-safe (both clients accept the same range).
const MaxControlEpoch = 9007199254740991

// ControlClockSkew tolerates sender/receiver clock differences when a
// receiver bounds expires_at.
const ControlClockSkew = 300

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
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '_' && c != '.' {
			return false
		}
	}
	return true
}

// exactKeys requires a JSON object with only the given (case-sensitive)
// member names, each at most once (encoding/json alone matches names
// case-insensitively; the JS mirror does not).
func exactKeys(raw json.RawMessage, allowed ...string) bool {
	d := json.NewDecoder(bytes.NewReader(raw))
	if t, err := d.Token(); err != nil || t != json.Delim('{') {
		return false
	}
	seen := map[string]bool{}
	for d.More() {
		t, err := d.Token()
		k, ok := t.(string)
		if err != nil || !ok || seen[k] {
			return false
		}
		known := false
		for _, a := range allowed {
			known = known || a == k
		}
		if !known {
			return false
		}
		seen[k] = true
		var skip json.RawMessage
		if d.Decode(&skip) != nil {
			return false
		}
	}
	return true
}

// integerLiteral: epoch and expires_at must be plain JSON integers (not
// null, 1.0 or 1e2), exactly as the JS mirror requires.
var integerLiteral = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)

// controlDataSize is the data's compact JSON size (the JS mirror measures
// JSON.stringify of the parsed value, which is the same for honest data).
func controlDataSize(data json.RawMessage) int {
	var b bytes.Buffer
	if json.Compact(&b, data) != nil {
		return MaxControlData + 1
	}
	return b.Len()
}

// ParseControlBody validates a control body's shape (not its expiry).
func ParseControlBody(body json.RawMessage) (ControlBody, error) {
	var b ControlBody
	var raw struct {
		Scope     json.RawMessage `json:"scope"`
		Epoch     json.RawMessage `json:"epoch"`
		ExpiresAt json.RawMessage `json:"expires_at"`
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if len(body) == 0 || len(body) > MaxControlData+1024 || !exactKeys(body, "kind", "scope", "epoch", "expires_at", "data") ||
		json.Unmarshal(body, &raw) != nil || !exactKeys(raw.Scope, "type", "id") || !integerLiteral.Match(raw.Epoch) || !integerLiteral.Match(raw.ExpiresAt) ||
		d.Decode(&b) != nil || d.More() {
		return ControlBody{}, ErrInvalid
	}
	if !validControlWord(b.Kind) || !validControlWord(b.Scope.Type) || !olmdir.ValidText(b.Scope.ID) || b.Epoch < 0 || b.Epoch > MaxControlEpoch || !safe(b.ExpiresAt) ||
		len(b.Data) > 0 && (!json.Valid(b.Data) || controlDataSize(b.Data) > MaxControlData) {
		return ControlBody{}, ErrInvalid
	}
	return b, nil
}

// ControlDisposition classifies a received control body (the exact
// authenticated bytes): "delivered", "expired", or "invalid" (malformed, or
// an expiry further ahead than a sender may set plus clock skew). The JS
// client mirrors it exactly (shared vectors in testdata/control-vectors.json).
func ControlDisposition(body json.RawMessage, now int64) string {
	b, err := ParseControlBody(body)
	switch {
	case err != nil || b.ExpiresAt > now+MaxControlTTL+ControlClockSkew:
		return "invalid"
	case b.ExpiresAt <= now:
		return "expired"
	}
	return "delivered"
}

// ControlArchiveBody is what a client keeps at rest for a control: kind,
// scope, epoch and expiry, never the data (future media keys). The data is
// handed to the control handlers from memory at commit time.
func ControlArchiveBody(body json.RawMessage) json.RawMessage {
	b, err := ParseControlBody(body)
	if err != nil {
		return nil
	}
	b.Data = nil
	out, _ := json.Marshal(b)
	return out
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
