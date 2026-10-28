package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"noxa/internal/e2ee/admission"
	"noxa/internal/e2ee/olmdir"
	"sort"
	"time"
)

var ErrOlmRetired = errors.New("Olm attempt retired")
var ErrOlmIncomplete = errors.New("Olm attempt incomplete")

type OlmSource struct {
	Server, Principal, Device, Signing string
	AccountEpoch                       int64
	AllowGroups                        bool
}

func (a OlmSource) matches(m admission.Manifest) bool {
	return a.Server == m.Server && a.Principal == m.Sender && a.Device == m.Device && a.AccountEpoch == m.AccountEpoch
}
func sourceAuthority(ctx context.Context, tx *sql.Tx, a OlmSource, current bool) error {
	d, e := lockOlmDevice(ctx, tx, a.Principal, a.Device)
	if e != nil {
		return e
	}
	if a.Signing == "" || d.Ed25519Public != a.Signing {
		return ErrOlmAuthority
	}
	if current {
		var epoch int64
		e = tx.QueryRowContext(ctx, `SELECT account_epoch FROM olm_directory_state WHERE device_id=$1`, a.Device).Scan(&epoch)
		if errors.Is(e, sql.ErrNoRows) || e == nil && epoch != a.AccountEpoch {
			return ErrOlmAccountChanged
		}
		return e
	}
	return nil
}
func admissionBudget(ctx context.Context, tx *sql.Tx) error {
	_, e := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('olm-admission-budget',0))`)
	if e != nil {
		return e
	}
	// Keep lock order, but never monopolize global admission behind a slow
	// authorized delivery handoff. Lock timeout aborts this transaction and is
	// retryable; it must not create a terminal receipt.
	_, e = tx.ExecContext(ctx, `SET LOCAL lock_timeout = '100ms'`)
	return e
}
func sourceLock(ctx context.Context, tx *sql.Tx, a OlmSource, current bool) error {
	if _, e := lockOlmRosters(ctx, tx, []string{a.Principal}); e != nil {
		return e
	}
	return sourceAuthority(ctx, tx, a, current)
}
func admissionPrincipals(ctx context.Context, tx *sql.Tx, a OlmSource, scope, peer, conversation string, revision, epoch int64) ([]string, error) {
	if scope == "direct" {
		if !olmdir.ValidText(peer) {
			return nil, admission.ErrInvalid
		}
		ps := []string{a.Principal}
		if peer != a.Principal {
			ps = append(ps, peer)
		}
		return ps, nil
	}
	if scope != "group" || !a.AllowGroups {
		return nil, admission.ErrInvalid
	}
	c, e := lockConversation(ctx, tx, conversation, true)
	if e != nil {
		return nil, e
	}
	member, ok := c.Member(a.Principal)
	if !ok || member.Pending {
		return nil, ErrOlmAuthority
	}
	if revision != 0 && (c.Revision != revision || c.Epoch != epoch) {
		return nil, ErrOlmConflict
	}
	ps := []string{}
	for _, m := range c.Members {
		if !m.Pending {
			ps = append(ps, m.UniqueID)
		}
	}
	if len(ps) > 16 {
		return nil, ErrOlmLimit
	}
	return ps, nil
}
func (s *Store) OlmSnapshot(ctx context.Context, a OlmSource, scope, peer, conversation string) (OlmRosterSnapshot, error) {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return OlmRosterSnapshot{}, e
	}
	defer tx.Rollback()
	ps, e := admissionPrincipals(ctx, tx, a, scope, peer, conversation, 0, 0)
	if e != nil {
		return OlmRosterSnapshot{}, e
	}
	snap, e := readOlmRoster(ctx, tx, ps, a.Device)
	if e != nil {
		return snap, e
	}
	if e = sourceAuthority(ctx, tx, a, true); e != nil {
		return snap, e
	}
	if scope == "group" {
		c, e := lockConversation(ctx, tx, conversation, false)
		if e != nil {
			return snap, e
		}
		snap.Revision = c.Revision
		snap.Epoch = c.Epoch
	}
	return snap, tx.Commit()
}
func receipt(m admission.Manifest, state, reason string) admission.Receipt {
	b, _ := m.Bytes()
	return admission.Receipt{State: state, AttemptID: m.AttemptID, Digest: admission.Digest(b), Server: m.Server, AccountEpoch: m.AccountEpoch, LogicalID: m.LogicalID, Reason: reason}
}
func loadAttempt(ctx context.Context, tx *sql.Tx, a OlmSource, id string) (admission.Manifest, admission.Receipt, error) {
	var m admission.Manifest
	var r admission.Receipt
	var b, rb []byte
	seq, se := admission.AttemptSequence(id)
	if se != nil {
		return m, r, se
	}
	var retired int64
	if e := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT retired_through FROM olm_attempt_fences WHERE server=$1 AND device_id=$2 AND account_epoch=$3),0)`, a.Server, a.Device, a.AccountEpoch).Scan(&retired); e != nil {
		return m, r, e
	}
	if seq <= retired {
		return m, admission.Receipt{State: "retired", AttemptID: id, Server: a.Server, AccountEpoch: a.AccountEpoch}, ErrOlmRetired
	}
	e := tx.QueryRowContext(ctx, `SELECT manifest,receipt FROM olm_attempts WHERE server=$1 AND device_id=$2 AND account_epoch=$3 AND attempt_id=$4 AND sender=$5 FOR UPDATE`, a.Server, a.Device, a.AccountEpoch, id, a.Principal).Scan(&b, &rb)
	if e != nil {
		return m, r, e
	}
	if e = json.Unmarshal(b, &m); e != nil {
		return m, r, e
	}
	e = json.Unmarshal(rb, &r)
	return m, r, e
}
func saveReceipt(ctx context.Context, tx *sql.Tx, a OlmSource, r admission.Receipt) error {
	b, e := json.Marshal(r)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `UPDATE olm_attempts SET state=$5,receipt=$6 WHERE server=$1 AND device_id=$2 AND account_epoch=$3 AND attempt_id=$4`, a.Server, a.Device, a.AccountEpoch, r.AttemptID, r.State, b)
	if e != nil {
		return e
	}
	if r.State != "staging" {
		_, e = tx.ExecContext(ctx, `DELETE FROM olm_parts WHERE server=$1 AND device_id=$2 AND account_epoch=$3 AND attempt_id=$4`, a.Server, a.Device, a.AccountEpoch, r.AttemptID)
	}
	return e
}
func insertAttempt(ctx context.Context, tx *sql.Tx, a OlmSource, m admission.Manifest, state string) (admission.Receipt, error) {
	r := receipt(m, state, "")
	seq, _ := admission.AttemptSequence(m.AttemptID)
	var retired int64
	if e := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT retired_through FROM olm_attempt_fences WHERE server=$1 AND device_id=$2 AND account_epoch=$3),0)`, a.Server, a.Device, a.AccountEpoch).Scan(&retired); e != nil {
		return r, e
	}
	if seq <= retired {
		return r, ErrOlmRetired
	}
	if seq > retired+10000 {
		return r, ErrOlmLimit
	}
	b, _ := m.Bytes()
	rb, _ := json.Marshal(r)
	// Expiry removes persisted parts before releasing quota, while retaining
	// bounded terminal receipts and immutable retry identity.
	if _, e := expireOlmAttempts(ctx, tx, 4, a.Device); e != nil {
		return r, e
	}
	if _, e := expireOlmAttempts(ctx, tx, 200, ""); e != nil {
		return r, e
	}
	// Retained fences are bounded, never removed and reinterpreted as fresh IDs.
	var retained, open, total int
	var wire int64
	if e := tx.QueryRowContext(ctx, `SELECT count(*),count(*) FILTER(WHERE state='staging') FROM olm_attempts WHERE device_id=$1`, a.Device).Scan(&retained, &open); e != nil {
		return r, e
	}
	if e := tx.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(octet_length(manifest)),0) FROM olm_attempts WHERE state='staging'`).Scan(&total, &wire); e != nil {
		return r, e
	}
	if retained >= 10000 || state == "staging" && (open >= 4 || total >= 1024 || wire >= 128<<20) {
		return r, ErrOlmLimit
	}
	_, e := tx.ExecContext(ctx, `INSERT INTO olm_attempts(server,sender,device_id,account_epoch,attempt_id,logical_id,manifest,digest,deadline,state,receipt) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, a.Server, a.Principal, a.Device, a.AccountEpoch, m.AttemptID, m.LogicalID, b, r.Digest, m.Deadline, state, rb)
	return r, e
}
func (s *Store) BeginOlmAttempt(ctx context.Context, a OlmSource, m admission.Manifest) (admission.Receipt, error) {
	var empty admission.Receipt
	if e := m.Validate(); e != nil {
		return empty, e
	}
	if !a.matches(m) {
		return empty, ErrOlmAuthority
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return empty, e
	}
	defer tx.Rollback()
	if e = admissionBudget(ctx, tx); e != nil {
		return empty, e
	}
	old, r, e := loadAttempt(ctx, tx, a, m.AttemptID)
	if errors.Is(e, ErrOlmRetired) {
		if e = sourceLock(ctx, tx, a, true); e != nil {
			return empty, e
		}
		return r, tx.Commit()
	}
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return empty, e
	}
	found := e == nil
	if e = sourceLock(ctx, tx, a, true); e != nil {
		return empty, e
	}
	if found {
		b, _ := m.Bytes()
		ob, _ := old.Bytes()
		if !bytes.Equal(b, ob) {
			return empty, ErrOlmConflict
		}
		return r, tx.Commit()
	}
	now := time.Now().Unix()
	if m.Deadline <= now || m.Deadline > now+3600 || m.Purpose == "control" && m.Deadline > now+admission.MaxControlTTL {
		return empty, admission.ErrInvalid
	}
	r, e = insertAttempt(ctx, tx, a, m, "staging")
	if e != nil {
		return empty, e
	}
	return r, tx.Commit()
}

func (s *Store) PutOlmPart(ctx context.Context, a OlmSource, id string, index int, data []byte) error {
	if !olmdir.ValidText(id) || len(data) > admission.MaxPartBytes || index < 0 || index >= admission.MaxParts {
		return admission.ErrInvalid
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = admissionBudget(ctx, tx); e != nil {
		return e
	}
	m, r, e := loadAttempt(ctx, tx, a, id)
	if errors.Is(e, ErrOlmRetired) {
		if e = sourceLock(ctx, tx, a, true); e != nil {
			return e
		}
		return ErrOlmRetired
	}
	if e != nil {
		return e
	}
	if e = sourceLock(ctx, tx, a, true); e != nil {
		return e
	}
	if index >= len(m.Parts) || m.Parts[index].Length != len(data) || m.Parts[index].Digest != admission.Digest(data) {
		return ErrOlmConflict
	}
	if r.State == "accepted" {
		return tx.Commit()
	}
	if r.State != "staging" {
		return ErrOlmConflict
	}
	if m.Deadline <= time.Now().Unix() {
		r.State = "rejected"
		r.Reason = "deadline"
		if e = saveReceipt(ctx, tx, a, r); e != nil {
			return e
		}
		if e = tx.Commit(); e != nil {
			return e
		}
		return ErrOlmConflict
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO olm_parts(server,device_id,account_epoch,attempt_id,part_number,data) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, a.Server, a.Device, a.AccountEpoch, id, index, data)
	if e != nil {
		return e
	}
	return tx.Commit()
}

func (s *Store) OlmAttemptStatus(ctx context.Context, a OlmSource, id string) (admission.Receipt, error) {
	var empty admission.Receipt
	if !olmdir.ValidText(id) {
		return empty, admission.ErrInvalid
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return empty, e
	}
	defer tx.Rollback()
	_, r, e := loadAttempt(ctx, tx, a, id)
	if errors.Is(e, ErrOlmRetired) {
		if e = sourceLock(ctx, tx, a, false); e != nil {
			return empty, e
		}
		return r, tx.Commit()
	}
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return empty, e
	}
	if e = sourceLock(ctx, tx, a, false); e != nil {
		return empty, e
	}
	if r.State == "" {
		r = admission.Receipt{State: "absent", AttemptID: id, Server: a.Server, AccountEpoch: a.AccountEpoch}
	}
	return r, tx.Commit()
}

func (s *Store) AbortOlmAttempt(ctx context.Context, a OlmSource, m admission.Manifest) (admission.Receipt, error) {
	var empty admission.Receipt
	if e := m.Validate(); e != nil {
		return empty, e
	}
	if !a.matches(m) {
		return empty, ErrOlmAuthority
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return empty, e
	}
	defer tx.Rollback()
	if e = admissionBudget(ctx, tx); e != nil {
		return empty, e
	}
	old, r, e := loadAttempt(ctx, tx, a, m.AttemptID)
	if errors.Is(e, ErrOlmRetired) {
		if e = sourceLock(ctx, tx, a, false); e != nil {
			return empty, e
		}
		return r, tx.Commit()
	}
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return empty, e
	}
	found := e == nil
	if e = sourceLock(ctx, tx, a, false); e != nil {
		return empty, e
	}
	if !found {
		r, e = insertAttempt(ctx, tx, a, m, "aborted")
	} else {
		b, _ := m.Bytes()
		ob, _ := old.Bytes()
		if !bytes.Equal(b, ob) {
			return empty, ErrOlmConflict
		}
		if r.State == "staging" {
			r.State = "aborted"
			e = saveReceipt(ctx, tx, a, r)
		}
	}
	if e != nil {
		return empty, e
	}
	return r, tx.Commit()
}

// controlCoverage: a control covers exactly its one named target (current
// directory state) and the local target, under current roster versions.
func controlCoverage(m admission.Manifest, snap OlmRosterSnapshot) bool {
	target, ok := admission.ControlTarget(m)
	if !ok || len(m.Rosters) != len(snap.Rosters) {
		return false
	}
	rs := map[string]int64{}
	for _, r := range m.Rosters {
		rs[r.Principal] = r.Version
	}
	for _, r := range snap.Rosters {
		if rs[r.Principal] != r.Version {
			return false
		}
	}
	found, local := false, false
	for _, t := range snap.Targets {
		for _, mt := range m.Targets {
			if mt == t {
				found = found || !t.Local && t == target
				local = local || t.Local
			}
		}
	}
	return found && local
}

func equalCoverage(m admission.Manifest, snap OlmRosterSnapshot) bool {
	if m.Purpose == "control" {
		return controlCoverage(m, snap)
	}
	if len(m.Targets) != len(snap.Targets) || len(m.Rosters) != len(snap.Rosters) {
		return false
	}
	rs := map[string]int64{}
	for _, r := range m.Rosters {
		rs[r.Principal] = r.Version
	}
	for _, r := range snap.Rosters {
		if rs[r.Principal] != r.Version {
			return false
		}
	}
	ts := map[string]admission.Target{}
	for _, t := range m.Targets {
		ts[t.Device] = t
	}
	for _, t := range snap.Targets {
		if ts[t.Device] != t {
			return false
		}
	}
	return true
}
func (s *Store) FinalizeOlmAttempt(ctx context.Context, a OlmSource, id string) (admission.Receipt, error) {
	var empty admission.Receipt
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return empty, e
	}
	defer tx.Rollback()
	if e = admissionBudget(ctx, tx); e != nil {
		return empty, e
	}
	m, r, e := loadAttempt(ctx, tx, a, id)
	if errors.Is(e, ErrOlmRetired) {
		if e = sourceLock(ctx, tx, a, true); e != nil {
			return empty, e
		}
		return r, tx.Commit()
	}
	if e != nil {
		return empty, e
	}
	// Terminal retry authenticates the source before returning the immutable result;
	// current target roster/membership cannot rewrite acceptance.
	if r.State != "staging" {
		if e = sourceLock(ctx, tx, a, true); e != nil {
			return empty, e
		}
		return r, tx.Commit()
	}
	reject := func(reason string) (admission.Receipt, error) {
		r.State = "rejected"
		r.Reason = reason
		if e := saveReceipt(ctx, tx, a, r); e != nil {
			return empty, e
		}
		return r, tx.Commit()
	}
	ps, e := admissionPrincipals(ctx, tx, a, m.Scope, m.Peer, m.Conversation, m.Revision, m.Epoch)
	if e != nil {
		if ae := sourceLock(ctx, tx, a, true); ae != nil {
			return empty, ae
		}
		if errors.Is(e, ErrOlmConflict) || errors.Is(e, ErrOlmAuthority) || errors.Is(e, sql.ErrNoRows) {
			return reject("membership")
		}
		return empty, e
	}
	snap, e := readOlmRoster(ctx, tx, ps, a.Device)
	if e != nil {
		return empty, e
	}
	if e = sourceAuthority(ctx, tx, a, true); e != nil {
		return empty, e
	}
	if m.Deadline <= time.Now().Unix() {
		return reject("deadline")
	}
	if !equalCoverage(m, snap) {
		return reject("roster")
	}
	parts := make([][]byte, len(m.Parts))
	rows, e := tx.QueryContext(ctx, `SELECT part_number,data FROM olm_parts WHERE server=$1 AND device_id=$2 AND account_epoch=$3 AND attempt_id=$4 ORDER BY part_number`, a.Server, a.Device, a.AccountEpoch, id)
	if e != nil {
		return empty, e
	}
	for rows.Next() {
		var n int
		var p []byte
		if e = rows.Scan(&n, &p); e != nil {
			rows.Close()
			return empty, e
		}
		if n < 0 || n >= len(parts) {
			rows.Close()
			return empty, admission.ErrInvalid
		}
		parts[n] = p
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return empty, e
	}
	for _, p := range parts {
		if p == nil {
			return empty, ErrOlmIncomplete
		}
	}
	copies, e := admission.Open(m, parts)
	if e != nil {
		return reject("invalid")
	}
	for _, c := range copies {
		if admission.CheckEnvelope(m, c) != nil {
			return reject("binding")
		}
	}
	var exists bool
	if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM olm_messages WHERE server=$1 AND sender=$2 AND logical_id=$3)`, a.Server, a.Principal, m.LogicalID).Scan(&exists); e != nil {
		return empty, e
	}
	if exists {
		return reject("logical-accepted")
	}
	if m.Purpose == "edit" || m.Purpose == "delete" {
		var original []byte
		e = tx.QueryRowContext(ctx, `SELECT manifest FROM olm_messages WHERE server=$1 AND sender=$2 AND logical_id=$3`, a.Server, a.Principal, m.Reference).Scan(&original)
		if errors.Is(e, sql.ErrNoRows) {
			return reject("reference")
		}
		if e != nil {
			return empty, e
		}
		var old admission.Manifest
		if e = json.Unmarshal(original, &old); e != nil {
			return empty, e
		}
		if old.Purpose != "chat" || old.Scope != m.Scope || old.Peer != m.Peer || old.Conversation != m.Conversation {
			return reject("reference")
		}
	}
	// All quota checks precede inserts. Pending data is never evicted for capacity.
	var global int64
	if e = tx.QueryRowContext(ctx, `SELECT COALESCE(sum(octet_length(copy)),0) FROM olm_deliveries WHERE state='pending'`).Scan(&global); e != nil {
		return empty, e
	}
	if global+admission.MaxWireBytes > 1<<30 {
		return empty, ErrOlmLimit
	}
	for _, c := range copies {
		var count int
		var size int64
		if e = tx.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(octet_length(copy)),0) FROM olm_deliveries WHERE device_id=$1 AND state='pending'`, c.Device).Scan(&count, &size); e != nil {
			return empty, e
		}
		if count >= 4096 || size+int64(len(c.Envelope))+1024 > 64<<20 {
			return empty, ErrOlmLimit
		}
		if m.Purpose == "control" {
			// Controls are bounded separately so they can never crowd out
			// messages (and are never a fanout).
			var controls int
			if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM olm_deliveries d JOIN olm_messages o ON o.id=d.message_id
				WHERE d.device_id=$1 AND d.state='pending' AND convert_from(o.manifest,'UTF8')::jsonb->>'purpose'='control'`, c.Device).Scan(&controls); e != nil {
				return empty, e
			}
			if controls >= admission.MaxPendingControls {
				return empty, ErrOlmLimit
			}
		}
	}
	// Device locks already cover target OTKs; claim operations follow the same
	// roster/device order, without acquiring their requester budget from here.
	sort.Slice(copies, func(i, j int) bool { return copies[i].Device < copies[j].Device })
	messageID := uuid.NewString()
	manifest, _ := m.Bytes()
	if _, e = tx.ExecContext(ctx, `INSERT INTO olm_messages(id,server,sender,device_id,account_epoch,logical_id,manifest) VALUES($1,$2,$3,$4,$5,$6,$7)`, messageID, a.Server, a.Principal, a.Device, a.AccountEpoch, m.LogicalID, manifest); e != nil {
		return empty, e
	}
	for _, c := range copies {
		deliveryID := uuid.NewString()
		data, _ := json.Marshal(c)
		var sessionClaim string
		e = tx.QueryRowContext(ctx, `SELECT claim_id FROM olm_sessions WHERE server=$1 AND ((source_device=$2 AND source_epoch=$3 AND target_device=$4 AND target_epoch=$5) OR (target_device=$2 AND target_epoch=$3 AND source_device=$4 AND source_epoch=$5)) AND session_id=$6`, a.Server, a.Device, a.AccountEpoch, c.Device, c.AccountEpoch, c.SessionID).Scan(&sessionClaim)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return empty, e
		}
		if e == nil {
			if c.ClaimID != "" && c.ClaimID != sessionClaim {
				return empty, ErrOlmConflict
			}
		} else {
			if c.ClaimID == "" {
				return empty, ErrOlmConflict
			}
			var state, claimSource, claimPrincipal, server, keyID string
			var targetBundle, sourceBundle []byte
			var sourceEpoch, deadline int64
			e = tx.QueryRowContext(ctx, `SELECT lifecycle,claimed_device,claimed_by,claimed_epoch,claim_until,server,key_id,bundle FROM olm_directory_keys WHERE device_id=$1 AND account_epoch=$2 AND request_id=$3 AND claimed_by=$4 AND claimed_device=$5 AND claimed_epoch=$6 AND server=$7 FOR UPDATE`, c.Device, c.AccountEpoch, c.ClaimID, a.Principal, a.Device, a.AccountEpoch, a.Server).Scan(&state, &claimSource, &claimPrincipal, &sourceEpoch, &deadline, &server, &keyID, &targetBundle)
			if errors.Is(e, sql.ErrNoRows) {
				return empty, ErrOlmConflict
			}
			if e != nil {
				return empty, e
			}
			if state != "reserved" || claimSource != a.Device || claimPrincipal != a.Principal || sourceEpoch != a.AccountEpoch || server != a.Server || deadline < m.Deadline || deadline <= time.Now().Unix() {
				return empty, ErrOlmConflict
			}
			if _, e = tx.ExecContext(ctx, `UPDATE olm_directory_keys SET lifecycle='admitted' WHERE device_id=$1 AND account_epoch=$2 AND key_id=$3`, c.Device, c.AccountEpoch, keyID); e != nil {
				return empty, e
			}
			if e = tx.QueryRowContext(ctx, `SELECT last_bundle FROM olm_directory_state WHERE device_id=$1`, a.Device).Scan(&sourceBundle); e != nil {
				return empty, e
			}
			if _, e = tx.ExecContext(ctx, `INSERT INTO olm_sessions(server,source_device,source_epoch,target_device,target_epoch,session_id,claim_id,initial_delivery,source_bundle,target_bundle) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, a.Server, a.Device, a.AccountEpoch, c.Device, c.AccountEpoch, c.SessionID, c.ClaimID, deliveryID, sourceBundle, targetBundle); e != nil {
				return empty, e
			}
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO olm_deliveries(id,message_id,principal,device_id,account_epoch,copy,digest) VALUES($1,$2,$3,$4,$5,$6,$7)`, deliveryID, messageID, c.Principal, c.Device, c.AccountEpoch, data, admission.Digest(data)); e != nil {
			return empty, e
		}
	}
	r.State = "accepted"
	r.MessageID = messageID
	if e = saveReceipt(ctx, tx, a, r); e != nil {
		return empty, e
	}
	return r, tx.Commit()
}
