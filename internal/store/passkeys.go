package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrPasskeyNotFound reports an unknown passkey or one of another account.
var ErrPasskeyNotFound = errors.New("passkey not found")

// Passkey is a stored WebAuthn credential. Credential is the opaque,
// verified credential record (JSON) the WebAuthn library round-trips.
type Passkey struct {
	CredentialID []byte
	Principal    string
	UserHandle   []byte
	DeviceID     string
	Name         string
	Credential   []byte
	CreatedAt    time.Time
	LastUsedAt   time.Time
}

// #nosec G101 -- column names, not a credential.
const passkeyColumns = `credential_id, principal, user_handle, device_id, name, credential, created_at, last_used_at`

func scanPasskey(row rowScanner) (Passkey, error) {
	var p Passkey
	var used sql.NullTime
	if err := row.Scan(&p.CredentialID, &p.Principal, &p.UserHandle, &p.DeviceID, &p.Name, &p.Credential, &p.CreatedAt, &used); err != nil {
		return Passkey{}, err
	}
	p.LastUsedAt = used.Time
	return p, nil
}

// PasskeyUserHandle returns the account's WebAuthn user handle, creating a
// random one for the first passkey. Callers persist it with AddPasskey.
func (s *Store) PasskeyUserHandle(ctx context.Context, principal string) ([]byte, error) {
	var handle []byte
	err := s.db.QueryRowContext(ctx, `SELECT user_handle FROM webauthn_credentials WHERE principal=$1 ORDER BY id LIMIT 1`, principal).Scan(&handle)
	if err == nil {
		return handle, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("loading passkey user handle: %w", err)
	}
	handle = make([]byte, 32)
	if _, err := rand.Read(handle); err != nil {
		return nil, err
	}
	return handle, nil
}

// AddPasskey stores a verified credential for an active device.
func (s *Store) AddPasskey(ctx context.Context, p Passkey) error {
	result, err := s.db.ExecContext(ctx, `INSERT INTO webauthn_credentials
		(credential_id, principal, user_handle, device_id, name, credential)
		SELECT $1, $2, $3, $4, $5, $6 FROM user_devices
		WHERE device_id=$4 AND principal=$2 AND revoked_at IS NULL`,
		p.CredentialID, p.Principal, p.UserHandle, p.DeviceID, NormalizeDeviceName(p.Name), p.Credential)
	if err != nil {
		return fmt.Errorf("adding passkey: %w", err)
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return ErrDeviceNotFound
	}
	return nil
}

// Passkeys lists the account's passkeys, oldest first.
func (s *Store) Passkeys(ctx context.Context, principal string) ([]Passkey, error) {
	return s.queryPasskeys(ctx, `SELECT `+passkeyColumns+` FROM webauthn_credentials WHERE principal=$1 ORDER BY id`, principal)
}

// PasskeysByHandle lists the passkeys of the account owning a user handle.
func (s *Store) PasskeysByHandle(ctx context.Context, handle []byte) ([]Passkey, error) {
	return s.queryPasskeys(ctx, `SELECT `+passkeyColumns+` FROM webauthn_credentials WHERE user_handle=$1 ORDER BY id`, handle)
}

func (s *Store) queryPasskeys(ctx context.Context, q string, arg any) ([]Passkey, error) {
	rows, err := s.db.QueryContext(ctx, q, arg)
	if err != nil {
		return nil, fmt.Errorf("listing passkeys: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Passkey
	for rows.Next() {
		p, err := scanPasskey(rows)
		if err != nil {
			return nil, fmt.Errorf("listing passkeys: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// RecordPasskeyUse stores the updated credential record (sign counter and
// flags) after a successful assertion.
func (s *Store) RecordPasskeyUse(ctx context.Context, credentialID, credential []byte) error {
	_, err := s.db.ExecContext(ctx, `UPDATE webauthn_credentials SET credential=$2, last_used_at=now() WHERE credential_id=$1`, credentialID, credential)
	if err != nil {
		return fmt.Errorf("recording passkey use: %w", err)
	}
	return nil
}

// DeletePasskey removes one of the account's passkeys.
func (s *Store) DeletePasskey(ctx context.Context, principal string, credentialID []byte) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM webauthn_credentials WHERE principal=$1 AND credential_id=$2`, principal, credentialID)
	if err != nil {
		return fmt.Errorf("deleting passkey: %w", err)
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return ErrPasskeyNotFound
	}
	return nil
}
