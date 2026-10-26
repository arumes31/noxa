package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

// Device kinds.
const (
	DeviceKindDesktop = "desktop"
	DeviceKindWeb     = "web"
)

// Device revocation reasons.
const (
	DeviceRevokedByUser        = "user"
	DeviceRevokedByAdminReset  = "admin_reset"
	DeviceRevokedIdle          = "idle"
	DeviceRevokedByIntegration = "integration"
)

var (
	// ErrDeviceNotFound reports an unknown device, or one owned by another
	// principal.
	ErrDeviceNotFound = errors.New("device not found")
	// ErrDeviceLimit reports that the principal already has the maximum
	// number of active devices.
	ErrDeviceLimit = errors.New("device limit reached")
	// ErrDeviceKeyInUse reports an identity key already registered, even on
	// a revoked device.
	ErrDeviceKeyInUse = errors.New("device key already registered")
)

// Device is one linked device of an account.
type Device struct {
	DeviceID          string
	Principal         string
	UserID            int64 // 0 for linked guests
	Kind              string
	Name              string
	Ed25519Public     string
	X25519Public      string
	FirstDevice       bool
	ApprovedBy        string
	ApprovalSignature []byte
	PasskeyProof      []byte
	CreatedAt         time.Time
	LastSeenAt        time.Time
	ExpiryWarnedAt    time.Time
	RevokedAt         time.Time
	RevokedReason     string
}

// Active reports whether the device is not revoked.
func (d Device) Active() bool { return d.RevokedAt.IsZero() }

// NewDevice describes a device to register.
type NewDevice struct {
	Principal         string
	UserID            int64
	Kind              string
	Name              string
	Ed25519Public     string
	X25519Public      string
	ApprovedBy        string
	ApprovalSignature []byte
	PasskeyProof      []byte
}

const deviceColumns = `device_id, principal, COALESCE(user_id, 0), kind, name, ed25519_public,
	x25519_public, first_device, approved_by, approval_signature, created_at, last_seen_at,
	expiry_warned_at, revoked_at, revoked_reason, passkey_proof`

type rowScanner interface{ Scan(dest ...any) error }

func scanDevice(row rowScanner) (Device, error) {
	var d Device
	var warned, revoked sql.NullTime
	err := row.Scan(&d.DeviceID, &d.Principal, &d.UserID, &d.Kind, &d.Name, &d.Ed25519Public,
		&d.X25519Public, &d.FirstDevice, &d.ApprovedBy, &d.ApprovalSignature, &d.CreatedAt,
		&d.LastSeenAt, &warned, &revoked, &d.RevokedReason, &d.PasskeyProof)
	if err != nil {
		return Device{}, err
	}
	d.ExpiryWarnedAt = warned.Time
	d.RevokedAt = revoked.Time
	return d, nil
}

// NormalizeDeviceName trims a device name and bounds it to 64 characters.
func NormalizeDeviceName(name string) string {
	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		return "Device"
	}
	runes := []rune(name)
	if len(runes) > 64 {
		runes = runes[:64]
	}
	return string(runes)
}

// AddDevice registers a device under the principal's active-device limit.
// The principal's first active device is marked as its first device, which
// idle expiry never revokes.
func (s *Store) AddDevice(ctx context.Context, d NewDevice, limit int) (Device, error) {
	if d.Principal == "" || d.Ed25519Public == "" {
		return Device{}, errors.New("adding device: principal and identity key are required")
	}
	if d.Kind != DeviceKindDesktop && d.Kind != DeviceKindWeb {
		return Device{}, fmt.Errorf("adding device: invalid kind %q", d.Kind)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Device{}, fmt.Errorf("adding device: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// Serialize registrations per principal so concurrent links cannot both
	// pass the limit check.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('user_devices:' || $1, 0))`, d.Principal); err != nil {
		return Device{}, fmt.Errorf("adding device: locking principal: %w", err)
	}
	if _, err = lockOlmRosters(ctx, tx, []string{d.Principal}); err != nil {
		return Device{}, err
	}
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM user_devices WHERE ed25519_public=$1)`, d.Ed25519Public).Scan(&exists); err != nil {
		return Device{}, fmt.Errorf("adding device: %w", err)
	}
	if exists {
		return Device{}, ErrDeviceKeyInUse
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM user_devices WHERE principal=$1 AND revoked_at IS NULL`, d.Principal).Scan(&active); err != nil {
		return Device{}, fmt.Errorf("adding device: counting devices: %w", err)
	}
	if limit > 0 && active >= limit {
		return Device{}, ErrDeviceLimit
	}
	var userID any
	if d.UserID > 0 {
		userID = d.UserID
	}
	row := tx.QueryRowContext(ctx, `INSERT INTO user_devices
		(device_id, principal, user_id, kind, name, ed25519_public, x25519_public, first_device, approved_by, approval_signature, passkey_proof)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING `+deviceColumns,
		uuid.NewString(), d.Principal, userID, d.Kind, NormalizeDeviceName(d.Name), d.Ed25519Public,
		d.X25519Public, active == 0, d.ApprovedBy, d.ApprovalSignature, d.PasskeyProof)
	device, err := scanDevice(row)
	if err != nil {
		return Device{}, fmt.Errorf("adding device: %w", err)
	}
	if err = bumpOlmRoster(ctx, tx, d.Principal); err != nil {
		return Device{}, err
	}
	if err := tx.Commit(); err != nil {
		return Device{}, fmt.Errorf("adding device: %w", err)
	}
	return device, nil
}

// DeviceByKey returns the device registered with the identity key, revoked
// or not.
func (s *Store) DeviceByKey(ctx context.Context, ed25519Public string) (Device, error) {
	d, err := scanDevice(s.db.QueryRowContext(ctx, `SELECT `+deviceColumns+` FROM user_devices WHERE ed25519_public=$1`, ed25519Public))
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, ErrDeviceNotFound
	}
	if err != nil {
		return Device{}, fmt.Errorf("loading device: %w", err)
	}
	return d, nil
}

// DeviceByID returns the principal's device, revoked or not.
func (s *Store) DeviceByID(ctx context.Context, principal, deviceID string) (Device, error) {
	d, err := scanDevice(s.db.QueryRowContext(ctx, `SELECT `+deviceColumns+` FROM user_devices WHERE principal=$1 AND device_id=$2`, principal, deviceID))
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, ErrDeviceNotFound
	}
	if err != nil {
		return Device{}, fmt.Errorf("loading device: %w", err)
	}
	return d, nil
}

// ListDevices returns the principal's devices, oldest first.
func (s *Store) ListDevices(ctx context.Context, principal string, includeRevoked bool) ([]Device, error) {
	q := `SELECT ` + deviceColumns + ` FROM user_devices WHERE principal=$1`
	if !includeRevoked {
		q += ` AND revoked_at IS NULL`
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY created_at, id`, principal)
	if err != nil {
		return nil, fmt.Errorf("listing devices: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var devices []Device
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, fmt.Errorf("listing devices: %w", err)
		}
		devices = append(devices, d)
	}
	return devices, rows.Err()
}

// TouchDevice records activity and clears a pending idle-expiry warning.
func (s *Store) TouchDevice(ctx context.Context, deviceID string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE user_devices SET last_seen_at=GREATEST(last_seen_at, $2), expiry_warned_at=NULL
		WHERE device_id=$1 AND revoked_at IS NULL`, deviceID, at)
	if err != nil {
		return fmt.Errorf("touching device: %w", err)
	}
	return nil
}

// RenameDevice changes an active device's display name.
func (s *Store) RenameDevice(ctx context.Context, principal, deviceID, name string) (Device, error) {
	d, err := scanDevice(s.db.QueryRowContext(ctx, `UPDATE user_devices SET name=$3
		WHERE principal=$1 AND device_id=$2 AND revoked_at IS NULL RETURNING `+deviceColumns,
		principal, deviceID, NormalizeDeviceName(name)))
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, ErrDeviceNotFound
	}
	if err != nil {
		return Device{}, fmt.Errorf("renaming device: %w", err)
	}
	return d, nil
}

// revokingDevices wraps a revoking UPDATE ... RETURNING * so the same
// statement deletes the revoked devices' passkeys, push subscriptions and
// pending history copies and returns their rows.
func revokingDevices(update string) string {
	return endingDevices(update, true)
}

func endingDevices(update string, removePasskeys bool) string {
	drop := ""
	if removePasskeys {
		drop = `dropped AS (DELETE FROM webauthn_credentials w USING revoked WHERE w.device_id = revoked.device_id),`
	}
	return `WITH revoked AS (` + update + `),
		` + drop + `
		unsubscribed AS (DELETE FROM push_subscriptions p USING revoked WHERE p.device_id = revoked.device_id),
		forgotten AS (DELETE FROM device_history_chunks h USING revoked WHERE h.device_id = revoked.device_id)
		SELECT ` + deviceColumns + ` FROM revoked ORDER BY created_at, id`
}

// SignOutDevice invalidates this device key and removes device-bound push/history
// state, but retains passkeys so the account can sign in again with a fresh key.
func (s *Store) SignOutDevice(ctx context.Context, principal, deviceID string) (Device, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Device{}, err
	}
	defer tx.Rollback()
	if _, err = lockOlmRosters(ctx, tx, []string{principal}); err != nil {
		return Device{}, err
	}
	d, err := scanDevice(tx.QueryRowContext(ctx, endingDevices(`UPDATE user_devices SET revoked_at=now(), revoked_reason='user'
		WHERE principal=$1 AND device_id=$2 AND revoked_at IS NULL RETURNING *`, false), principal, deviceID))
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, ErrDeviceNotFound
	}
	if err != nil {
		return Device{}, fmt.Errorf("signing out device: %w", err)
	}
	if err = bumpOlmRoster(ctx, tx, principal); err != nil {
		return Device{}, err
	}
	if err = tx.Commit(); err != nil {
		return Device{}, err
	}
	return d, nil
}

// RevokeDevice revokes a device and any passkeys retained by a prior sign-out.
func (s *Store) RevokeDevice(ctx context.Context, principal, deviceID, reason string) (Device, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Device{}, err
	}
	defer tx.Rollback()
	if _, err = lockOlmRosters(ctx, tx, []string{principal}); err != nil {
		return Device{}, err
	}
	d, err := scanDevice(tx.QueryRowContext(ctx, revokingDevices(`UPDATE user_devices SET revoked_at=now(), revoked_reason=$3
		WHERE principal=$1 AND device_id=$2 AND (revoked_at IS NULL OR EXISTS
			(SELECT 1 FROM webauthn_credentials w WHERE w.device_id=user_devices.device_id)) RETURNING *`),
		principal, deviceID, reason))
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, ErrDeviceNotFound
	}
	if err != nil {
		return Device{}, fmt.Errorf("revoking device: %w", err)
	}
	if err = bumpOlmRoster(ctx, tx, principal); err != nil {
		return Device{}, err
	}
	if err = tx.Commit(); err != nil {
		return Device{}, err
	}
	return d, nil
}

// updateLockedDevices locks the complete selected set before changing any row.
// The device-ID order matches Olm claims; limiting the UPDATE to captured IDs
// prevents newly eligible rows from acquiring locks out of order. The caller's
// update must use $1 for those IDs and return deviceColumns (possibly through
// revokingDevices, retaining its atomic dependent-data cleanup).
func (s *Store) updateLockedDevices(ctx context.Context, predicate string, selectionArgs []any, update string, updateArgs ...any) ([]Device, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	principals, err := lockMutationPrincipals(ctx, tx, predicate, selectionArgs)
	if err != nil {
		return nil, err
	}
	selectionArgs = append(append([]any(nil), selectionArgs...), pq.Array(principals))
	rows, err := tx.QueryContext(ctx, `SELECT device_id FROM user_devices WHERE (`+predicate+`) AND principal=ANY($`+strconv.Itoa(len(selectionArgs))+`) ORDER BY device_id COLLATE "C" FOR UPDATE`, selectionArgs...)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	args := append([]any{pq.Array(ids)}, updateArgs...)
	rows, err = tx.QueryContext(ctx, update, args...)
	if err != nil {
		return nil, err
	}
	var out []Device
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, d)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if err = bumpMutationPrincipals(ctx, tx, ids); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

// ResetDevices revokes every active device of the principal except keep
// (may be empty) and returns the revoked devices.
func (s *Store) ResetDevices(ctx context.Context, principal, keep, reason string) ([]Device, error) {
	revoked, err := s.updateLockedDevices(ctx,
		`principal=$1 AND device_id<>$2 AND (revoked_at IS NULL OR EXISTS
			(SELECT 1 FROM webauthn_credentials w WHERE w.device_id=user_devices.device_id))`,
		[]any{principal, keep},
		revokingDevices(`UPDATE user_devices SET revoked_at=now(), revoked_reason=$2 WHERE device_id=ANY($1) RETURNING *`), reason)
	if err != nil {
		return nil, fmt.Errorf("resetting devices: %w", err)
	}
	return revoked, nil
}

// ExpireIdleDevices marks devices idle since warnBefore as warned and revokes
// devices idle since revokeBefore. First devices never expire. It returns the
// newly warned and the revoked devices. Each phase commits separately, as before,
// releasing all ordered row locks before the next phase selects its own set.
func (s *Store) ExpireIdleDevices(ctx context.Context, warnBefore, revokeBefore time.Time) (warned, revoked []Device, err error) {
	revoked, err = s.updateLockedDevices(ctx,
		`revoked_at IS NULL AND NOT first_device AND last_seen_at < $1`, []any{revokeBefore},
		revokingDevices(`UPDATE user_devices SET revoked_at=now(), revoked_reason='idle' WHERE device_id=ANY($1) RETURNING *`))
	if err != nil {
		return nil, nil, fmt.Errorf("revoking idle devices: %w", err)
	}
	warned, err = s.updateLockedDevices(ctx,
		`revoked_at IS NULL AND NOT first_device AND expiry_warned_at IS NULL AND last_seen_at < $1`, []any{warnBefore},
		`UPDATE user_devices SET expiry_warned_at=now() WHERE device_id=ANY($1) RETURNING `+deviceColumns)
	if err != nil {
		return nil, revoked, fmt.Errorf("warning idle devices: %w", err)
	}
	return warned, revoked, nil
}
