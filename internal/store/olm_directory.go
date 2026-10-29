package store

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"errors"
	"noxa/internal/e2ee/olmdir"
	"time"
)

// "Olm" is a proper noun (the protocol name), so these messages start capitalized.
//
//nolint:staticcheck // ST1005: proper noun
var (
	ErrOlmAuthority      = errors.New("Olm device authority required")
	ErrOlmConflict       = errors.New("Olm publication conflict")
	ErrOlmAccountChanged = errors.New("Olm account-change-required")
	ErrOlmExhausted      = errors.New("Olm keys exhausted")
	ErrOlmLimit          = errors.New("Olm directory limit reached")
)

const OlmDevicePairClaimsPerDay = 2
const OlmTotalClaimsPerDay = 4096

// OlmClaim preserves the ORIGINAL signed list. KeyID selects one entry; never
// reconstruct a signature over the server's remaining-key inventory.
type OlmClaim struct {
	Bundle olmdir.Bundle `json:"bundle"`
	KeyID  string        `json:"key_id"`
}

func lockOlmDevice(ctx context.Context, tx *sql.Tx, principal, id string) (Device, error) {
	d, e := scanDevice(tx.QueryRowContext(ctx, `SELECT `+deviceColumns+` FROM user_devices WHERE principal=$1 AND device_id=$2 FOR UPDATE`, principal, id))
	if errors.Is(e, sql.ErrNoRows) || e == nil && (!d.Active() || !d.FirstDevice && len(d.ApprovalSignature) == 0) {
		return Device{}, ErrOlmAuthority
	}
	return d, e
}

// PublishOlmBundle authenticates against the locked, active existing device row.
// principal/id/server MUST be derived from the authenticated session by caller.
func (s *Store) PublishOlmBundle(ctx context.Context, principal, id, server string, b olmdir.Bundle) error {
	if b.Principal != principal || b.DeviceID != id || b.Server != server {
		return ErrOlmAuthority
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer func() { _ = tx.Rollback() }()
	if _, e = lockOlmRosters(ctx, tx, []string{principal}); e != nil {
		return e
	}
	d, e := lockOlmDevice(ctx, tx, principal, id)
	if e != nil {
		return e
	}
	block, _ := pem.Decode([]byte(d.Ed25519Public))
	if block == nil || block.Type != "PUBLIC KEY" || len(block.Bytes) != 32 {
		return ErrOlmAuthority
	}
	key := ed25519.PublicKey(block.Bytes)
	if e = b.Verify(key, server, principal, id, time.Now()); e != nil {
		return ErrOlmAuthority
	}
	encoded, e := json.Marshal(b)
	if e != nil || len(encoded) > olmdir.MaxBytes {
		return olmdir.ErrInvalid
	}
	var identity, signing string
	var generation, high, epoch int64
	var previous []byte
	e = tx.QueryRowContext(ctx, `SELECT identity,signing,generation,key_highwater,last_bundle,account_epoch FROM olm_directory_state WHERE device_id=$1`, id).Scan(&identity, &signing, &generation, &high, &previous, &epoch)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	exists := e == nil
	if exists && epoch != b.AccountEpoch || !exists && b.AccountEpoch != 1 {
		return ErrOlmAccountChanged
	}
	if exists && b.Generation <= generation {
		var retained []byte
		err := tx.QueryRowContext(ctx, `SELECT bundle FROM olm_directory_publications WHERE device_id=$1 AND generation=$2`, id, b.Generation).Scan(&retained)
		if err == nil && bytes.Equal(encoded, retained) {
			return tx.Commit()
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	if exists {
		if identity != b.Identity || signing != b.Signing {
			return ErrOlmAccountChanged
		}
		if b.Generation == generation && bytes.Equal(encoded, previous) {
			return tx.Commit()
		}
		if b.Generation != generation+1 {
			return ErrOlmConflict
		}
	} else {
		high = -1
		if b.Generation != 1 {
			return ErrOlmConflict
		}
	}
	maxID := high
	for _, k := range b.OneTimeKeys {
		n, e := olmdir.KeyNumber(k.ID)
		if e != nil || n <= high {
			return ErrOlmConflict
		}
		if n > maxID {
			maxID = n
		}
	}
	// At most 100 retained private-key obligations, including claimed keys and
	// delivery grace. Replenishment must wait until expiry+grace frees capacity.
	_, e = tx.ExecContext(ctx, `DELETE FROM olm_directory_publications WHERE device_id=$1 AND expires_at < now()-interval '24 hours'`, id)
	if e != nil {
		return e
	}
	var count, totalRecords int
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FILTER(WHERE lifecycle IN ('available','reserved','admitted')),count(*) FROM olm_directory_keys WHERE device_id=$1`, id).Scan(&count, &totalRecords); e != nil {
		return e
	}
	if count+len(b.OneTimeKeys) > olmdir.MaxKeys || totalRecords+len(b.OneTimeKeys) > 100000 {
		return ErrOlmLimit
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO olm_directory_state(device_id,identity,signing,generation,key_highwater,last_bundle,account_epoch) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(device_id) DO UPDATE SET generation=EXCLUDED.generation,key_highwater=EXCLUDED.key_highwater,last_bundle=EXCLUDED.last_bundle`, id, b.Identity, b.Signing, b.Generation, maxID, encoded, b.AccountEpoch)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO olm_directory_publications(device_id,generation,bundle,expires_at,server) VALUES($1,$2,$3,$4,$5)`, id, b.Generation, encoded, time.Unix(b.ExpiresAt, 0), server)
	if e != nil {
		return e
	}
	for _, k := range b.OneTimeKeys {
		if _, e = tx.ExecContext(ctx, `INSERT INTO olm_directory_keys(device_id,generation,key_id,account_epoch,principal,server,bundle) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, b.Generation, k.ID, b.AccountEpoch, principal, server, encoded); e != nil {
			return e
		}
	}
	if !exists {
		if e = bumpOlmRoster(ctx, tx, principal); e != nil {
			return e
		}
	}
	return tx.Commit()
}

// ClaimOlmKey is single-target and idempotent per requester principal/request ID.
// The durable account-pair and device-pair budgets survive reconnects.
func (s *Store) ClaimOlmKey(ctx context.Context, principal, id, target, targetID, requestID string, claimUntil int64, server string, expectedEpoch ...int64) (OlmClaim, error) {
	var out OlmClaim
	if claimUntil <= time.Now().Unix() {
		return out, ErrOlmExhausted
	}
	if claimUntil > time.Now().Unix()+3600 {
		return out, olmdir.ErrInvalid
	}
	if !olmdir.ValidText(principal) || !olmdir.ValidText(id) || !olmdir.ValidText(target) || !olmdir.ValidText(targetID) || !olmdir.ValidText(requestID) {
		return out, olmdir.ErrInvalid
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return out, e
	}
	defer func() { _ = tx.Rollback() }()
	if _, e = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('olm_claim:'||$1,0))`, principal); e != nil {
		return out, e
	}
	if _, e = lockOlmRosters(ctx, tx, []string{principal, target}); e != nil {
		return out, e
	}
	// Stable lock order also covers self-send; revocation updates these rows.
	pairs := [][2]string{{principal, id}, {target, targetID}}
	if id > targetID {
		pairs[0], pairs[1] = pairs[1], pairs[0]
	}
	for _, p := range pairs {
		if _, e = lockOlmDevice(ctx, tx, p[0], p[1]); e != nil {
			return out, e
		}
	}
	var encoded []byte
	var foundDevice, claimDevice string
	var storedUntil int64
	var lifecycle string
	var sourceEpoch int64
	if e = tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT account_epoch FROM olm_directory_state WHERE device_id=$1),1)`, id).Scan(&sourceEpoch); e != nil {
		return out, e
	}
	if len(expectedEpoch) > 1 || len(expectedEpoch) == 1 && expectedEpoch[0] != sourceEpoch {
		return out, ErrOlmAccountChanged
	}
	var claimedEpoch int64
	e = tx.QueryRowContext(ctx, `SELECT k.device_id,k.key_id,k.bundle,k.lifecycle,k.claimed_device,k.claim_until,k.claimed_epoch FROM olm_directory_keys k WHERE claimed_by=$1 AND request_id=$2`, principal, requestID).Scan(&foundDevice, &out.KeyID, &encoded, &lifecycle, &claimDevice, &storedUntil, &claimedEpoch)
	if e == nil {
		if foundDevice != targetID || claimDevice != id || storedUntil != claimUntil || claimedEpoch != sourceEpoch {
			return out, ErrOlmConflict
		}
		if lifecycle == "closed" {
			return out, ErrOlmExhausted
		}
	} else {
		if !errors.Is(e, sql.ErrNoRows) {
			return out, e
		}
		var count, total, devicePairCount int
		if e = tx.QueryRowContext(ctx, `SELECT count(*),count(*) FILTER(WHERE d.principal=$2),count(*) FILTER(WHERE k.claimed_device=$3 AND k.device_id=$4) FROM olm_directory_keys k JOIN user_devices d ON d.device_id=k.device_id WHERE k.claimed_by=$1 AND k.claimed_at>now()-interval '24 hours'`, principal, target, id, targetID).Scan(&total, &count, &devicePairCount); e != nil {
			return out, e
		}
		pairCapacity := OlmDevicePairClaimsPerDay
		for _, owner := range []string{principal, target} {
			var active int
			if e = tx.QueryRowContext(ctx, `SELECT LEAST(count(*),100) FROM user_devices WHERE principal=$1 AND revoked_at IS NULL AND (first_device OR octet_length(approval_signature)>0)`, owner).Scan(&active); e != nil {
				return out, e
			}
			pairCapacity *= active
		}
		if count >= pairCapacity || total >= OlmTotalClaimsPerDay || devicePairCount >= OlmDevicePairClaimsPerDay {
			return out, ErrOlmLimit
		}
		e = tx.QueryRowContext(ctx, `SELECT k.key_id,p.bundle FROM olm_directory_keys k JOIN olm_directory_publications p USING(device_id,generation) WHERE k.device_id=$1 AND p.server=$2 AND k.lifecycle='available' AND k.account_epoch=(SELECT account_epoch FROM olm_directory_state WHERE device_id=k.device_id) AND p.expires_at>now() ORDER BY k.generation,k.key_id LIMIT 1`, targetID, server).Scan(&out.KeyID, &encoded)
		if errors.Is(e, sql.ErrNoRows) {
			return out, ErrOlmExhausted
		}
		if e != nil {
			return out, e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE olm_directory_keys SET claimed_by=$3,request_id=$4,claimed_at=now(),claimed_device=$5,claim_until=$6,claimed_epoch=$7,lifecycle='reserved' WHERE device_id=$1 AND key_id=$2 AND lifecycle='available' AND account_epoch=(SELECT account_epoch FROM olm_directory_state WHERE device_id=$1)`, targetID, out.KeyID, principal, requestID, id, claimUntil, sourceEpoch); e != nil {
			return out, e
		}
	}
	if e = json.Unmarshal(encoded, &out.Bundle); e != nil {
		return OlmClaim{}, e
	}
	var targetEpoch int64
	if e = tx.QueryRowContext(ctx, `SELECT account_epoch FROM olm_directory_state WHERE device_id=$1`, targetID).Scan(&targetEpoch); e != nil {
		return OlmClaim{}, e
	}
	if targetEpoch != out.Bundle.AccountEpoch {
		return OlmClaim{}, ErrOlmAccountChanged
	}
	if out.Bundle.Server != server || out.Bundle.Principal != target {
		return OlmClaim{}, ErrOlmAuthority
	}
	if e = tx.Commit(); e != nil {
		return OlmClaim{}, e
	}
	return out, nil
}

// ExpireOlmDirectory bounds each sweep to 256 immutable publications. The
// monotonic state watermark is deliberately retained until device deletion.
func (s *Store) ExpireOlmDirectory(ctx context.Context) (int64, error) {
	r, e := s.db.ExecContext(ctx, `DELETE FROM olm_directory_publications WHERE (device_id,generation) IN (SELECT device_id,generation FROM olm_directory_publications WHERE expires_at<now()-interval '24 hours' ORDER BY expires_at LIMIT 256)`)
	if e != nil {
		return 0, e
	}
	return r.RowsAffected()
}

// OwnOlmBundle returns the requesting device's own latest signed publication
// (read-only). principal/id MUST come from the authenticated session; no
// other device's or account's data is ever returned. It is input for an
// explicit account recovery: the client verifies the signature under its own
// noXa key, and peers still verify the signed reset chain.
func (s *Store) OwnOlmBundle(ctx context.Context, principal, id string) (olmdir.Bundle, error) {
	var b olmdir.Bundle
	var raw []byte
	e := s.db.QueryRowContext(ctx, `SELECT st.last_bundle FROM olm_directory_state st JOIN user_devices d ON d.device_id=st.device_id WHERE d.principal=$1 AND st.device_id=$2`, principal, id).Scan(&raw)
	if errors.Is(e, sql.ErrNoRows) {
		return b, sql.ErrNoRows
	}
	if e != nil {
		return b, e
	}
	if e = json.Unmarshal(raw, &b); e != nil || b.Principal != principal || b.DeviceID != id {
		return olmdir.Bundle{}, ErrOlmAuthority
	}
	return b, nil
}
