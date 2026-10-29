package store

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"errors"
	"noxa/internal/e2ee/admission"
	"noxa/internal/e2ee/olmdir"
	"time"
)

type OlmResetReceipt = olmdir.ResetReceipt

func (s *Store) ResetOlmAccount(ctx context.Context, a OlmSource, r olmdir.Reset, b olmdir.Bundle) (OlmResetReceipt, error) {
	var out OlmResetReceipt
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return out, e
	}
	defer func() { _ = tx.Rollback() }()
	if e = sourceLock(ctx, tx, a, false); e != nil {
		return out, e
	}
	encoded, e := json.Marshal(r)
	if e != nil {
		return out, e
	}
	var prior, receipt []byte
	e = tx.QueryRowContext(ctx, `SELECT record,receipt FROM olm_resets WHERE device_id=$1 AND transition_id=$2`, a.Device, r.TransitionID).Scan(&prior, &receipt)
	digest, de := olmdir.PublicationDigest(b)
	if de != nil || digest != r.PublicationDigest {
		return out, ErrOlmConflict
	}
	if e == nil {
		if !bytes.Equal(prior, encoded) || r.Server != a.Server {
			return out, ErrOlmConflict
		}
		if e = json.Unmarshal(receipt, &out); e != nil {
			return out, e
		}
		return out, tx.Commit()
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return out, e
	}
	var identity, signing string
	var epoch, generation, high int64
	e = tx.QueryRowContext(ctx, `SELECT account_epoch,identity,signing,generation,key_highwater FROM olm_directory_state WHERE device_id=$1`, a.Device).Scan(&epoch, &identity, &signing, &generation, &high)
	if e != nil {
		return out, e
	}
	key, _ := pem.Decode([]byte(a.Signing))
	if key == nil || r.Verify(ed25519.PublicKey(key.Bytes), a.Server, a.Principal, a.Device, epoch, identity, signing) != nil || r.Generation != generation+1 || a.AccountEpoch != epoch {
		return out, ErrOlmAuthority
	}
	if b.AccountEpoch != r.NewEpoch || b.Generation != r.Generation || b.Identity != r.NewIdentity || b.Signing != r.NewSigning || b.Verify(ed25519.PublicKey(key.Bytes), a.Server, a.Principal, a.Device, time.Now()) != nil {
		return out, ErrOlmAuthority
	}
	var unresolved, records int
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FILTER(WHERE lifecycle IN ('available','reserved','admitted')),count(*) FROM olm_directory_keys WHERE device_id=$1`, a.Device).Scan(&unresolved, &records); e != nil {
		return out, e
	}
	if unresolved+len(b.OneTimeKeys) > 100 || records+len(b.OneTimeKeys) > 100000 {
		return out, ErrOlmLimit
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO olm_account_history(device_id,account_epoch,identity,signing,key_highwater) VALUES($1,$2,$3,$4,$5)`, a.Device, epoch, identity, signing, high); e != nil {
		return out, e
	}
	bundle, _ := json.Marshal(b)
	newHigh := int64(-1)
	for _, k := range b.OneTimeKeys {
		n, e := olmdir.KeyNumber(k.ID)
		if e != nil {
			return out, e
		}
		if n > newHigh {
			newHigh = n
		}
	}
	if _, e = tx.ExecContext(ctx, `UPDATE olm_directory_state SET account_epoch=$2,identity=$3,signing=$4,generation=$5,key_highwater=$6,last_bundle=$7 WHERE device_id=$1`, a.Device, b.AccountEpoch, b.Identity, b.Signing, b.Generation, newHigh, bundle); e != nil {
		return out, e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO olm_directory_publications(device_id,generation,server,bundle,expires_at) VALUES($1,$2,$3,$4,$5)`, a.Device, b.Generation, a.Server, bundle, time.Unix(b.ExpiresAt, 0)); e != nil {
		return out, e
	}
	for _, k := range b.OneTimeKeys {
		if _, e = tx.ExecContext(ctx, `INSERT INTO olm_directory_keys(device_id,generation,key_id,account_epoch,principal,server,bundle) VALUES($1,$2,$3,$4,$5,$6,$7)`, a.Device, b.Generation, k.ID, b.AccountEpoch, a.Principal, a.Server, bundle); e != nil {
			return out, e
		}
	}
	out = OlmResetReceipt{Server: a.Server, Device: a.Device, TransitionID: r.TransitionID, AccountEpoch: r.NewEpoch, Digest: admission.Digest(encoded), PublicationDigest: digest}
	receipt, _ = json.Marshal(out)
	if _, e = tx.ExecContext(ctx, `INSERT INTO olm_resets(device_id,transition_id,record,receipt) VALUES($1,$2,$3,$4)`, a.Device, r.TransitionID, encoded, receipt); e != nil {
		return out, e
	}
	if e = bumpOlmRoster(ctx, tx, a.Principal); e != nil {
		return out, e
	}
	return out, tx.Commit()
}
func (s *Store) OlmResetChain(ctx context.Context, a OlmSource, target string, after int64) ([]olmdir.Reset, error) {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return nil, e
	}
	defer func() { _ = tx.Rollback() }()
	if e = sourceLock(ctx, tx, a, false); e != nil {
		return nil, e
	}
	rows, e := tx.QueryContext(ctx, `SELECT record FROM olm_resets WHERE device_id=$1 AND (convert_from(record,'UTF8')::jsonb->>'old_epoch')::bigint >= $2 ORDER BY (convert_from(record,'UTF8')::jsonb->>'old_epoch')::bigint LIMIT 32`, target, after)
	if e != nil {
		return nil, e
	}
	defer func() { _ = rows.Close() }()
	out := []olmdir.Reset{}
	for rows.Next() {
		var b []byte
		var r olmdir.Reset
		if e = rows.Scan(&b); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(b, &r); e != nil {
			return nil, e
		}
		if r.Server == a.Server {
			out = append(out, r)
		}
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	_ = rows.Close()
	return out, tx.Commit()
}
