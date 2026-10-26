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
	defer tx.Rollback()
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
	var generation, high int64
	var previous []byte
	e = tx.QueryRowContext(ctx, `SELECT identity,signing,generation,key_highwater,last_bundle FROM olm_directory_state WHERE device_id=$1`, id).Scan(&identity, &signing, &generation, &high, &previous)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	exists := e == nil
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
	var count int
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM olm_directory_keys WHERE device_id=$1`, id).Scan(&count); e != nil {
		return e
	}
	if count+len(b.OneTimeKeys) > olmdir.MaxKeys {
		return ErrOlmLimit
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO olm_directory_state(device_id,identity,signing,generation,key_highwater,last_bundle) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(device_id) DO UPDATE SET generation=EXCLUDED.generation,key_highwater=EXCLUDED.key_highwater,last_bundle=EXCLUDED.last_bundle`, id, b.Identity, b.Signing, b.Generation, maxID, encoded)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO olm_directory_publications(device_id,generation,bundle,expires_at,server) VALUES($1,$2,$3,$4,$5)`, id, b.Generation, encoded, time.Unix(b.ExpiresAt, 0), server)
	if e != nil {
		return e
	}
	for _, k := range b.OneTimeKeys {
		if _, e = tx.ExecContext(ctx, `INSERT INTO olm_directory_keys(device_id,generation,key_id) VALUES($1,$2,$3)`, id, b.Generation, k.ID); e != nil {
			return e
		}
	}
	return tx.Commit()
}

// ClaimOlmKey is single-target and idempotent per requester principal/request ID.
// The durable account-pair and device-pair budgets survive reconnects.
func (s *Store) ClaimOlmKey(ctx context.Context, principal, id, target, targetID, requestID string, claimUntil int64, server string) (OlmClaim, error) {
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
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('olm_claim:'||$1,0))`, principal); e != nil {
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
	var expires time.Time
	e = tx.QueryRowContext(ctx, `SELECT k.device_id,k.key_id,p.bundle,p.expires_at,k.claimed_device,k.claim_until FROM olm_directory_keys k JOIN olm_directory_publications p USING(device_id,generation) WHERE claimed_by=$1 AND request_id=$2`, principal, requestID).Scan(&foundDevice, &out.KeyID, &encoded, &expires, &claimDevice, &storedUntil)
	if e == nil {
		if foundDevice != targetID || claimDevice != id || storedUntil != claimUntil {
			return out, ErrOlmConflict
		}
		if !expires.After(time.Now()) {
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
		e = tx.QueryRowContext(ctx, `SELECT k.key_id,p.bundle FROM olm_directory_keys k JOIN olm_directory_publications p USING(device_id,generation) WHERE k.device_id=$1 AND p.server=$2 AND k.claimed_by IS NULL AND p.expires_at>now() ORDER BY k.generation,k.key_id LIMIT 1`, targetID, server).Scan(&out.KeyID, &encoded)
		if errors.Is(e, sql.ErrNoRows) {
			return out, ErrOlmExhausted
		}
		if e != nil {
			return out, e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE olm_directory_keys SET claimed_by=$3,request_id=$4,claimed_at=now(),claimed_device=$5,claim_until=$6 WHERE device_id=$1 AND key_id=$2`, targetID, out.KeyID, principal, requestID, id, claimUntil); e != nil {
			return out, e
		}
	}
	if e = json.Unmarshal(encoded, &out.Bundle); e != nil {
		return OlmClaim{}, e
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
