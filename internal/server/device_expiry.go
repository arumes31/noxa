// device_expiry.go revokes linked devices that stayed idle too long.
package server

import (
	"context"
	"time"

	"go.uber.org/zap"

	"noxa/internal/netproto"
	"noxa/internal/store"
)

const (
	// deviceIdleRevoke revokes a linked device unused this long. The first
	// device of an account never expires.
	deviceIdleRevoke = 180 * 24 * time.Hour
	// deviceIdleWarn is when every session of the account is warned.
	deviceIdleWarn = deviceIdleRevoke - 14*24*time.Hour
	// deviceExpiryInterval is how often the expiry sweep runs.
	deviceExpiryInterval = time.Hour
)

// deviceExpiryStore is the store side of the idle sweep.
type deviceExpiryStore interface {
	ExpireIdleDevices(ctx context.Context, warnBefore, revokeBefore time.Time) (warned, revoked []store.Device, err error)
}

// MaintainDeviceExpiry runs the idle-device sweep until ctx ends.
func (s *TCPServer) MaintainDeviceExpiry(ctx context.Context) {
	ticker := time.NewTicker(deviceExpiryInterval)
	defer ticker.Stop()
	for {
		s.expireIdleDevices(ctx, time.Now())
		s.expireHistoryCopies(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *TCPServer) expireIdleDevices(ctx context.Context, now time.Time) {
	if s.deps == nil || s.deps.Devices == nil {
		return
	}
	expiry, ok := s.deps.Devices.(deviceExpiryStore)
	if !ok {
		return
	}
	warned, revoked, err := expiry.ExpireIdleDevices(ctx, now.Add(-deviceIdleWarn), now.Add(-deviceIdleRevoke))
	if err != nil {
		s.logger.Warn("idle device sweep failed", zap.Error(err))
	}
	for _, d := range warned {
		info := deviceInfo(d, "")
		s.notifyDevices(d.Principal, netproto.DeviceEvent{
			Event:     netproto.DeviceEventExpiryWarning,
			Device:    &info,
			ExpiresAt: d.LastSeenAt.Add(deviceIdleRevoke).Unix(),
		})
	}
	byPrincipal := make(map[string][]store.Device)
	for _, d := range revoked {
		byPrincipal[d.Principal] = append(byPrincipal[d.Principal], d)
	}
	for principal, devices := range byPrincipal {
		s.afterDevicesRevoked(ctx, principal, devices, store.DeviceRevokedIdle, "system")
	}
}
