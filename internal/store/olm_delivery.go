package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/lib/pq"
	"noxa/internal/e2ee/admission"
	"noxa/internal/e2ee/olmdir"
	"sort"
)

type OlmDelivery struct {
	SourceBundle olmdir.Bundle      `json:"source_bundle"`
	TargetBundle olmdir.Bundle      `json:"target_bundle"`
	ID           string             `json:"id"`
	Digest       string             `json:"digest"`
	Manifest     admission.Manifest `json:"manifest"`
	Copy         admission.Copy     `json:"copy"`
}

type OlmDeliveryPage struct {
	Deliveries []OlmDelivery `json:"deliveries"`
	After      string        `json:"after"`
	HasMore    bool          `json:"has_more"`
}

// WithOlmDeliveries holds membership and current device locks through a bounded
// write. Returning from the callback NEVER acknowledges reception.
func (s *Store) WithOlmDeliveries(ctx context.Context, a OlmSource, after string, deliver func(OlmDeliveryPage) error) error {
	// At most two copies plus manifests fit comfortably below the 1 MiB frame.
	rows, e := s.db.QueryContext(ctx, `SELECT d.id,m.manifest FROM olm_deliveries d JOIN olm_messages m ON m.id=d.message_id WHERE d.principal=$1 AND d.device_id=$2 AND d.account_epoch=$3 AND m.server=$4 AND d.state='pending' AND d.id>$5 ORDER BY d.id LIMIT 2`, a.Principal, a.Device, a.AccountEpoch, a.Server, after)
	if e != nil {
		return e
	}
	ids := []string{}
	groups := map[string]bool{}
	for rows.Next() {
		var id string
		var b []byte
		if e = rows.Scan(&id, &b); e != nil {
			rows.Close()
			return e
		}
		var m admission.Manifest
		if e = json.Unmarshal(b, &m); e != nil {
			rows.Close()
			return e
		}
		ids = append(ids, id)
		if m.Scope == "group" {
			groups[m.Conversation] = true
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	conn, e := s.db.Conn(ctx)
	if e != nil {
		return e
	}
	defer conn.Close()
	tx, e := conn.BeginTx(context.WithoutCancel(ctx), nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	gs := []string{}
	for g := range groups {
		gs = append(gs, g)
	}
	sort.Strings(gs)
	allowed := map[string]int64{}
	for _, g := range gs {
		c, e := lockConversation(ctx, tx, g, false)
		if errors.Is(e, sql.ErrNoRows) {
			continue
		}
		if e != nil {
			return e
		}
		member, ok := c.Member(a.Principal)
		if ok && !member.Pending {
			allowed[g] = member.JoinedEpoch
		}
	}
	if e = sourceLock(ctx, tx, a, false); e != nil {
		return e
	}
	rows, e = tx.QueryContext(ctx, `SELECT d.id,d.digest,m.manifest,d.copy FROM olm_deliveries d JOIN olm_messages m ON m.id=d.message_id WHERE d.id=ANY($1) AND d.state='pending' ORDER BY d.id`, pq.Array(ids))
	if e != nil {
		return e
	}
	out := []OlmDelivery{}
	for rows.Next() {
		var d OlmDelivery
		var mb, cb []byte
		if e = rows.Scan(&d.ID, &d.Digest, &mb, &cb); e != nil {
			rows.Close()
			return e
		}
		if e = json.Unmarshal(mb, &d.Manifest); e != nil {
			rows.Close()
			return e
		}
		if e = json.Unmarshal(cb, &d.Copy); e != nil {
			rows.Close()
			return e
		}
		if d.Manifest.Scope == "group" {
			joined, ok := allowed[d.Manifest.Conversation]
			if !ok || d.Manifest.Epoch < joined {
				continue
			}
		}
		out = append(out, d)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for i := range out {
		d := &out[i]
		var source, target []byte
		e = tx.QueryRowContext(ctx, `SELECT CASE WHEN source_device=$2 THEN source_bundle ELSE target_bundle END,CASE WHEN source_device=$2 THEN target_bundle ELSE source_bundle END FROM olm_sessions WHERE server=$1 AND ((source_device=$2 AND source_epoch=$3 AND target_device=$4 AND target_epoch=$5) OR (target_device=$2 AND target_epoch=$3 AND source_device=$4 AND source_epoch=$5)) AND session_id=$6`, d.Manifest.Server, d.Manifest.Device, d.Manifest.AccountEpoch, d.Copy.Device, d.Copy.AccountEpoch, d.Copy.SessionID).Scan(&source, &target)
		if e != nil {
			return e
		}
		if e = json.Unmarshal(source, &d.SourceBundle); e != nil {
			return e
		}
		if e = json.Unmarshal(target, &d.TargetBundle); e != nil {
			return e
		}
	}
	page := OlmDeliveryPage{Deliveries: out, After: after, HasMore: len(ids) == 2}
	if len(ids) > 0 {
		page.After = ids[len(ids)-1]
	}
	encoded, e := json.Marshal(page)
	if e != nil {
		return e
	}
	if len(encoded) > 768<<10 {
		return ErrOlmLimit
	}
	if e = deliver(page); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) AckOlmDelivery(ctx context.Context, a OlmSource, id, digest string) error {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = sourceLock(ctx, tx, a, false); e != nil {
		return e
	}
	var principal, device, hash, server string
	var epoch int64
	e = tx.QueryRowContext(ctx, `SELECT d.principal,d.device_id,d.account_epoch,d.digest,m.server FROM olm_deliveries d JOIN olm_messages m ON m.id=d.message_id WHERE d.id=$1 FOR UPDATE OF d`, id).Scan(&principal, &device, &epoch, &hash, &server)
	if errors.Is(e, sql.ErrNoRows) || e == nil && (principal != a.Principal || device != a.Device || epoch != a.AccountEpoch || server != a.Server) {
		return ErrOlmAuthority
	}
	if e != nil {
		return e
	}
	if digest != hash {
		return ErrOlmConflict
	}
	if _, e = tx.ExecContext(ctx, `UPDATE olm_deliveries SET state='received',received_at=COALESCE(received_at,now()) WHERE id=$1`, id); e != nil {
		return e
	}
	// Only the exact admitted first delivery releases an initial-key obligation.
	_, e = tx.ExecContext(ctx, `UPDATE olm_directory_keys k SET lifecycle='received' FROM olm_sessions s WHERE s.initial_delivery=$1 AND k.device_id=s.target_device AND k.account_epoch=s.target_epoch AND k.request_id=s.claim_id AND k.claimed_device=s.source_device AND k.claimed_epoch=s.source_epoch AND k.lifecycle='admitted'`, id)
	if e != nil {
		return e
	}
	return tx.Commit()
}

type OlmKeyLifecycle struct {
	Server       string `json:"server"`
	Device       string `json:"device"`
	AccountEpoch int64  `json:"account_epoch"`
	KeyID        string `json:"key_id"`
	State        string `json:"state"`
	Proof        string `json:"proof"`
}

// CloseOlmKeys serializes authoritative unused-key closure with claims and
// admission. Public expiry alone never deletes private keys or admitted packets.
func (s *Store) CloseOlmKeys(ctx context.Context, a OlmSource, keys []string) ([]OlmKeyLifecycle, error) {
	if len(keys) < 1 || len(keys) > 100 {
		return nil, admission.ErrInvalid
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	if e = sourceLock(ctx, tx, a, false); e != nil {
		return nil, e
	}
	ids := append([]string(nil), keys...)
	sort.Strings(ids)
	out := []OlmKeyLifecycle{}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			return nil, admission.ErrInvalid
		}
		seen[id] = true
		var state, server string
		var until sql.NullInt64
		var expired bool
		e = tx.QueryRowContext(ctx, `SELECT lifecycle,server,claim_until,NOT EXISTS(SELECT 1 FROM olm_directory_publications p WHERE p.device_id=k.device_id AND p.generation=k.generation AND p.expires_at>now()) OR k.account_epoch<>(SELECT account_epoch FROM olm_directory_state WHERE device_id=k.device_id) FROM olm_directory_keys k WHERE device_id=$1 AND account_epoch=$2 AND key_id=$3 FOR UPDATE`, a.Device, a.AccountEpoch, id).Scan(&state, &server, &until, &expired)
		if e != nil {
			return nil, e
		}
		if server != a.Server {
			return nil, ErrOlmAuthority
		}
		var canClose bool
		if state == "available" && expired {
			canClose = true
		}
		if state == "reserved" {
			if e = tx.QueryRowContext(ctx, `SELECT $1<=EXTRACT(EPOCH FROM now())::bigint`, until.Int64).Scan(&canClose); e != nil {
				return nil, e
			}
		}
		if canClose {
			state = "closed"
			if _, e = tx.ExecContext(ctx, `UPDATE olm_directory_keys SET lifecycle='closed' WHERE device_id=$1 AND account_epoch=$2 AND key_id=$3`, a.Device, a.AccountEpoch, id); e != nil {
				return nil, e
			}
		}
		record := OlmKeyLifecycle{Server: a.Server, Device: a.Device, AccountEpoch: a.AccountEpoch, KeyID: id, State: state}
		b, _ := json.Marshal(record)
		record.Proof = admission.Digest(b)
		out = append(out, record)
	}
	return out, tx.Commit()
}
