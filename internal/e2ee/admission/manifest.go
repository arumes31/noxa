// Package admission defines the final, inactive Olm message transaction wire.
package admission

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"noxa/internal/e2ee/olmdir"
	"strconv"
)

const Version = "noxa-olm-admission-v1"
const MaxTargets = 160
const MaxPartBytes = 512 << 10
const MaxWireBytes = 4 << 20
const MaxEnvelopeBytes = 24 << 10
const MaxParts = 8
const MaxManifestBytes = 128 << 10

var ErrInvalid = errors.New("invalid Olm admission")

type Roster struct {
	Principal string `json:"principal"`
	Version   int64  `json:"version"`
}
type Target struct {
	Principal    string `json:"principal"`
	Device       string `json:"device"`
	AccountEpoch int64  `json:"account_epoch"`
	Identity     string `json:"identity"`
	Local        bool   `json:"local,omitempty"`
}
type Part struct {
	Digest string `json:"digest"`
	Length int    `json:"length"`
}
type Manifest struct {
	Version      string   `json:"version"`
	Server       string   `json:"server"`
	Sender       string   `json:"sender"`
	Device       string   `json:"device"`
	AccountEpoch int64    `json:"account_epoch"`
	AttemptID    string   `json:"attempt_id"`
	LogicalID    string   `json:"logical_id"`
	Purpose      string   `json:"purpose"`
	Reference    string   `json:"reference,omitempty"`
	Scope        string   `json:"scope"`
	Peer         string   `json:"peer,omitempty"`
	Conversation string   `json:"conversation,omitempty"`
	Revision     int64    `json:"revision,omitempty"`
	Epoch        int64    `json:"epoch,omitempty"`
	Deadline     int64    `json:"deadline"`
	Rosters      []Roster `json:"rosters"`
	Targets      []Target `json:"targets"`
	Parts        []Part   `json:"parts"`
}
type Copy struct {
	Principal    string          `json:"principal"`
	Device       string          `json:"device"`
	AccountEpoch int64           `json:"account_epoch"`
	SessionID    string          `json:"session_id"`
	ClaimID      string          `json:"claim_id,omitempty"`
	Envelope     json.RawMessage `json:"envelope"`
}
type Receipt struct {
	State        string `json:"state"`
	AttemptID    string `json:"attempt_id"`
	Digest       string `json:"digest"`
	Server       string `json:"server"`
	AccountEpoch int64  `json:"account_epoch"`
	LogicalID    string `json:"logical_id"`
	MessageID    string `json:"message_id,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

func Digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func validDigest(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && hex.EncodeToString(b) == s
}
func safe(n int64) bool { return n > 0 && n <= 9007199254740991 }
func AttemptSequence(id string) (int64, error) {
	n, e := strconv.ParseInt(id, 10, 64)
	if e != nil || !safe(n) || strconv.FormatInt(n, 10) != id {
		return 0, ErrInvalid
	}
	return n, nil
}
func (m Manifest) Validate() error {
	if _, e := AttemptSequence(m.AttemptID); e != nil {
		return e
	}
	if m.Version != Version || len(m.Server) != 75 || m.Server[:11] != "tls-sha256:" || !validDigest(m.Server[11:]) || !safe(m.AccountEpoch) || !safe(m.Deadline) || len(m.Targets) < 1 || len(m.Targets) > MaxTargets || len(m.Rosters) < 1 || len(m.Rosters) > 16 || len(m.Parts) < 1 || len(m.Parts) > MaxParts {
		return ErrInvalid
	}
	for _, s := range []string{m.Sender, m.Device, m.AttemptID, m.LogicalID} {
		if !olmdir.ValidText(s) {
			return ErrInvalid
		}
	}
	switch m.Purpose {
	case "chat":
		if m.Reference != "" {
			return ErrInvalid
		}
	case "edit", "delete":
		if !olmdir.ValidText(m.Reference) {
			return ErrInvalid
		}
	case "control":
		// One explicitly targeted device, never a fanout (control.go).
		if m.Reference != "" || m.Scope != "direct" || len(m.Targets) != 2 {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	switch m.Scope {
	case "direct":
		if !olmdir.ValidText(m.Peer) || m.Conversation != "" || m.Revision != 0 || m.Epoch != 0 || len(m.Rosters) > 2 {
			return ErrInvalid
		}
	case "group":
		if !olmdir.ValidText(m.Conversation) || m.Peer != "" || !safe(m.Revision) || !safe(m.Epoch) {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	roster := map[string]bool{}
	for _, r := range m.Rosters {
		if !olmdir.ValidText(r.Principal) || !safe(r.Version) || roster[r.Principal] {
			return ErrInvalid
		}
		roster[r.Principal] = true
	}
	if !roster[m.Sender] || m.Scope == "direct" && !roster[m.Peer] {
		return ErrInvalid
	}
	seen := map[string]bool{}
	local := 0
	for _, t := range m.Targets {
		if !roster[t.Principal] || !olmdir.ValidText(t.Device) || !safe(t.AccountEpoch) || !olmdir.ValidText(t.Identity) || seen[t.Device] {
			return ErrInvalid
		}
		seen[t.Device] = true
		if t.Local {
			local++
			if t.Principal != m.Sender || t.Device != m.Device || t.AccountEpoch != m.AccountEpoch {
				return ErrInvalid
			}
		}
	}
	if local != 1 {
		return ErrInvalid
	}
	if _, ok := ControlTarget(m); m.Purpose == "control" && !ok {
		return ErrInvalid
	}
	total := 0
	for _, p := range m.Parts {
		if !validDigest(p.Digest) || p.Length < 2 || p.Length > MaxPartBytes {
			return ErrInvalid
		}
		total += p.Length
	}
	b, e := json.Marshal(m)
	if e != nil || len(b) > MaxManifestBytes || len(b)+total > MaxWireBytes {
		return ErrInvalid
	}
	return nil
}
func (m Manifest) Bytes() ([]byte, error) {
	if e := m.Validate(); e != nil {
		return nil, e
	}
	return json.Marshal(m)
}
func Freeze(m *Manifest, copies []Copy) ([][]byte, error) {
	if m == nil || len(copies) > MaxTargets-1 {
		return nil, ErrInvalid
	}
	var parts [][]byte
	batch := []Copy{}
	for _, c := range copies {
		if len(c.Envelope) > MaxEnvelopeBytes {
			return nil, ErrInvalid
		}
		next := append(append([]Copy{}, batch...), c)
		b, e := json.Marshal(next)
		if e != nil {
			return nil, ErrInvalid
		}
		if len(b) > MaxPartBytes {
			b, _ = json.Marshal(batch)
			parts = append(parts, b)
			batch = []Copy{c}
		} else {
			batch = next
		}
	}
	b, e := json.Marshal(batch)
	if e != nil {
		return nil, e
	}
	parts = append(parts, b)
	m.Parts = nil
	for _, p := range parts {
		m.Parts = append(m.Parts, Part{Digest: Digest(p), Length: len(p)})
	}
	if _, e = Open(*m, parts); e != nil {
		return nil, e
	}
	return parts, nil
}
func Open(m Manifest, parts [][]byte) ([]Copy, error) {
	if e := m.Validate(); e != nil || len(parts) != len(m.Parts) {
		return nil, ErrInvalid
	}
	targets := map[string]Target{}
	for _, t := range m.Targets {
		if !t.Local {
			targets[t.Device] = t
		}
	}
	var out []Copy
	for i, b := range parts {
		if len(b) != m.Parts[i].Length || Digest(b) != m.Parts[i].Digest {
			return nil, ErrInvalid
		}
		var cs []Copy
		if e := Decode(b, &cs, MaxPartBytes); e != nil {
			return nil, e
		}
		for _, c := range cs {
			t, ok := targets[c.Device]
			if !ok || t.Principal != c.Principal || t.AccountEpoch != c.AccountEpoch || !olmdir.ValidText(c.SessionID) || len(c.Envelope) < 2 || len(c.Envelope) > MaxEnvelopeBytes || !json.Valid(c.Envelope) || c.ClaimID != "" && !olmdir.ValidText(c.ClaimID) {
				return nil, ErrInvalid
			}
			delete(targets, c.Device)
			out = append(out, c)
		}
	}
	if len(targets) != 0 {
		return nil, ErrInvalid
	}
	return out, nil
}

// Decode rejects aliases, duplicate members, unknown fields and trailing values.
func Decode(b []byte, out any, max int) error {
	if len(b) == 0 || len(b) > max {
		return ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(b))
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 16 {
			return ErrInvalid
		}
		t, e := d.Token()
		if e != nil {
			return ErrInvalid
		}
		delim, ok := t.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				k, e := d.Token()
				s, ok := k.(string)
				if e != nil || !ok || seen[s] || s == "" {
					return ErrInvalid
				}
				for _, c := range s {
					if (c < 'a' || c > 'z') && c != '_' && (c < '0' || c > '9') {
						return ErrInvalid
					}
				}
				seen[s] = true
				if e = walk(depth + 1); e != nil {
					return e
				}
			}
		case '[':
			for d.More() {
				if e = walk(depth + 1); e != nil {
					return e
				}
			}
		default:
			return ErrInvalid
		}
		_, e = d.Token()
		return e
	}
	if e := walk(0); e != nil {
		return ErrInvalid
	}
	if _, e := d.Token(); !errors.Is(e, io.EOF) {
		return ErrInvalid
	}
	d = json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e := d.Decode(out); e != nil {
		return ErrInvalid
	}
	return nil
}
