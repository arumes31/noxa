package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// PushSubscription is a web device's Web Push subscription and its
// notification preferences.
type PushSubscription struct {
	DeviceID     string
	Principal    string
	Endpoint     string
	P256dh       []byte
	Auth         []byte
	HideDetails  bool
	AlwaysNotify bool
	// Filters is the device's netproto.PushFilters JSON ("{}" when unset).
	Filters   []byte
	CreatedAt time.Time
	UpdatedAt time.Time
	Failures  int
}

const pushColumns = `device_id, principal, endpoint, p256dh, auth_secret, hide_details, always_notify, filters, created_at, updated_at, failures`

func scanPush(row rowScanner) (PushSubscription, error) {
	var p PushSubscription
	err := row.Scan(&p.DeviceID, &p.Principal, &p.Endpoint, &p.P256dh, &p.Auth, &p.HideDetails, &p.AlwaysNotify, &p.Filters, &p.CreatedAt, &p.UpdatedAt, &p.Failures)
	return p, err
}

// SavePushSubscription stores or replaces the subscription of an active
// web device of the principal. An endpoint moves to the newest device that
// reports it.
func (s *Store) SavePushSubscription(ctx context.Context, p PushSubscription) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("saving push subscription: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM push_subscriptions WHERE endpoint=$1 AND device_id<>$2`, p.Endpoint, p.DeviceID); err != nil {
		return fmt.Errorf("saving push subscription: %w", err)
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO push_subscriptions
		(device_id, principal, endpoint, p256dh, auth_secret, hide_details, always_notify)
		SELECT $1, $2, $3, $4, $5, $6, $7 FROM user_devices
		WHERE device_id=$1 AND principal=$2 AND kind='web' AND revoked_at IS NULL
		ON CONFLICT (device_id) DO UPDATE SET endpoint=EXCLUDED.endpoint, p256dh=EXCLUDED.p256dh,
			auth_secret=EXCLUDED.auth_secret, hide_details=EXCLUDED.hide_details,
			always_notify=EXCLUDED.always_notify, updated_at=now(), failures=0`,
		p.DeviceID, p.Principal, p.Endpoint, p.P256dh, p.Auth, p.HideDetails, p.AlwaysNotify)
	if err != nil {
		return fmt.Errorf("saving push subscription: %w", err)
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return ErrDeviceNotFound
	}
	return tx.Commit()
}

// SetPushPreferences updates the notification preferences of a device's
// subscription.
func (s *Store) SetPushPreferences(ctx context.Context, principal, deviceID string, hideDetails, alwaysNotify bool) error {
	result, err := s.db.ExecContext(ctx, `UPDATE push_subscriptions SET hide_details=$3, always_notify=$4, updated_at=now()
		WHERE principal=$1 AND device_id=$2`, principal, deviceID, hideDetails, alwaysNotify)
	if err != nil {
		return fmt.Errorf("updating push preferences: %w", err)
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return ErrDeviceNotFound
	}
	return nil
}

// SetPushFilters replaces the push filters (JSON) of a device's
// subscription.
func (s *Store) SetPushFilters(ctx context.Context, principal, deviceID string, filters []byte) error {
	if len(filters) == 0 {
		filters = []byte("{}")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE push_subscriptions SET filters=$3::jsonb, updated_at=now()
		WHERE principal=$1 AND device_id=$2`, principal, deviceID, string(filters))
	if err != nil {
		return fmt.Errorf("updating push filters: %w", err)
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return ErrDeviceNotFound
	}
	return nil
}

// PushSubscriptionForDevice returns the device's subscription.
func (s *Store) PushSubscriptionForDevice(ctx context.Context, principal, deviceID string) (PushSubscription, error) {
	p, err := scanPush(s.db.QueryRowContext(ctx, `SELECT `+pushColumns+` FROM push_subscriptions
		WHERE principal=$1 AND device_id=$2`, principal, deviceID))
	if errors.Is(err, sql.ErrNoRows) {
		return PushSubscription{}, ErrDeviceNotFound
	}
	if err != nil {
		return PushSubscription{}, fmt.Errorf("loading push subscription: %w", err)
	}
	return p, nil
}

// PushSubscriptions lists the principal's subscriptions of active devices.
func (s *Store) PushSubscriptions(ctx context.Context, principal string) ([]PushSubscription, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+pushColumns+` FROM push_subscriptions p
		WHERE p.principal=$1 AND EXISTS (SELECT 1 FROM user_devices d WHERE d.device_id=p.device_id AND d.revoked_at IS NULL)
		ORDER BY p.created_at`, principal)
	if err != nil {
		return nil, fmt.Errorf("listing push subscriptions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []PushSubscription
	for rows.Next() {
		p, err := scanPush(rows)
		if err != nil {
			return nil, fmt.Errorf("listing push subscriptions: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeletePushSubscription removes a device's subscription.
func (s *Store) DeletePushSubscription(ctx context.Context, principal, deviceID string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM push_subscriptions WHERE principal=$1 AND device_id=$2`, principal, deviceID); err != nil {
		return fmt.Errorf("deleting push subscription: %w", err)
	}
	return nil
}

// DeletePushEndpoint removes a subscription the push service reported gone.
func (s *Store) DeletePushEndpoint(ctx context.Context, endpoint string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM push_subscriptions WHERE endpoint=$1`, endpoint); err != nil {
		return fmt.Errorf("deleting push endpoint: %w", err)
	}
	return nil
}

// RecordPushResult resets or increments an endpoint's failure count and
// drops a subscription after maxFailures consecutive failures.
func (s *Store) RecordPushResult(ctx context.Context, endpoint string, ok bool, maxFailures int) error {
	if ok {
		_, err := s.db.ExecContext(ctx, `UPDATE push_subscriptions SET failures=0 WHERE endpoint=$1 AND failures<>0`, endpoint)
		return err
	}
	var failures int
	err := s.db.QueryRowContext(ctx, `UPDATE push_subscriptions SET failures=failures+1 WHERE endpoint=$1 RETURNING failures`, endpoint).Scan(&failures)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("recording push failure: %w", err)
	}
	if failures >= maxFailures {
		return s.DeletePushEndpoint(ctx, endpoint)
	}
	return nil
}
