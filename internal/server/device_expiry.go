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
		if db, _, ok := s.olmDirectory(); ok {
			if ledger, ok := db.(interface {
				ExpireOlmAttempts(context.Context) (int, error)
			}); ok {
				if _, err := ledger.ExpireOlmAttempts(ctx); err != nil {
					s.logger.Warn("Olm attempt sweep failed", zap.Error(err))
				}
			}
			if retention, ok := db.(interface {
				ExpireReceivedOlm(context.Context) (int64, error)
			}); ok {
				// Catch up across bounded transactions. Per-pass row and time
				// budgets keep retention work from monopolizing maintenance.
				retentionCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				for batch := 0; batch < 100; batch++ {
					cleared, err := retention.ExpireReceivedOlm(retentionCtx)
					if err != nil {
						s.logger.Warn("Olm delivered retention sweep failed", zap.Error(err))
						break
					}
					if cleared < 200 {
						break
					}
				}
				cancel()
			}
			if controls, ok := db.(interface {
				ExpirePendingOlmControls(context.Context) (int64, error)
			}); ok {
				if _, err := controls.ExpirePendingOlmControls(ctx); err != nil {
					s.logger.Warn("Olm pending control sweep failed", zap.Error(err))
				}
			}
			if _, err := db.ExpireOlmDirectory(ctx); err != nil {
				s.logger.Warn("Olm directory sweep failed", zap.Error(err))
			}
			s.maintainOlmPending(ctx, db)
		}
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

// maintainOlmPending closes pending copies that can no longer be read (ended
// devices, account epochs abandoned by a reset) and warns when the server-wide
// pending budget is nearly used.
func (s *TCPServer) maintainOlmPending(ctx context.Context, db any) {
	if abandoned, ok := db.(interface {
		ExpireAbandonedOlmDeliveries(context.Context) (int64, error)
	}); ok {
		sweepCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		for batch := 0; batch < 100; batch++ {
			closed, err := abandoned.ExpireAbandonedOlmDeliveries(sweepCtx)
			if err != nil {
				s.logger.Warn("Olm abandoned delivery sweep failed", zap.Error(err))
				break
			}
			if closed < 1000 {
				break
			}
		}
		cancel()
	}
	if usage, ok := db.(interface {
		OlmPendingUsage(context.Context) (int64, int64, error)
	}); ok {
		used, limit, err := usage.OlmPendingUsage(ctx)
		switch {
		case err != nil:
			s.logger.Warn("Olm pending usage check failed", zap.Error(err))
		case limit > 0 && used*10 >= limit*8:
			s.logger.Warn("Olm pending ciphertext budget nearly exhausted: encrypted messages and voice key updates will be refused when it is full",
				zap.Int64("pending_bytes", used), zap.Int64("limit_bytes", limit))
		}
	}
}

// ReportE2EEAvailability logs, at startup, when this server cannot offer
// end-to-end encryption: channel media must be end-to-end encrypted, so such a
// server refuses channel voice as well as secure direct and group messages.
func (s *TCPServer) ReportE2EEAvailability() {
	if s.mediaE2EEAvailable() {
		return
	}
	reason := "the signed device directory is unavailable"
	if s.TLSFingerprint() == "" {
		reason = "tls_enabled is false (keys are bound to the server's TLS certificate)"
	} else if s.deps == nil || s.deps.Devices == nil {
		reason = "the device store (database) is unavailable"
	}
	s.logger.Warn("end-to-end encryption is UNAVAILABLE on this server: channel voice and secure direct/group messages will be refused",
		zap.String("reason", reason))
}
