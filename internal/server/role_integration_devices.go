package server

import (
	"context"
	"errors"

	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"noxa/internal/store"
)

// WithIntegrationDevices lists, revokes or resets an account's linked
// devices for administrators. Device keys are never part of the result.
func (s *TCPServer) WithIntegrationDevices(ctx context.Context, principal auth.IntegrationPrincipal, request netproto.IntegrationDeviceRequest, deliver func(context.Context, netproto.IntegrationDeviceResult) error) error {
	if deliver == nil || !request.Valid() {
		return authorization.ErrRoleInvalid
	}
	if s.deps == nil || s.deps.Devices == nil {
		return authorization.ErrAuthorizationUnavailable
	}
	return s.withIntegrationPolicy(ctx, principal, func(ctx context.Context, e *authorization.RoleEvaluator) error {
		if !e.Evaluate(principal.UserID(), 0, authorization.Administrator).Allowed {
			return authorization.ErrRoleForbidden
		}
		actor := "integration:" + principal.UniqueID()
		out := netproto.IntegrationDeviceResult{Action: request.Action, UniqueID: request.UniqueID}
		switch request.Action {
		case "revoke":
			d, err := s.deps.Devices.RevokeDevice(ctx, request.UniqueID, request.DeviceID, store.DeviceRevokedByIntegration)
			if errors.Is(err, store.ErrDeviceNotFound) {
				return authorization.ErrRoleInvalid
			}
			if err != nil {
				return err
			}
			s.afterDevicesRevoked(ctx, request.UniqueID, []store.Device{d}, store.DeviceRevokedByIntegration, actor)
			out.Revoked = append(out.Revoked, deviceInfo(d, ""))
		case "reset":
			revoked, err := s.ResetDevices(ctx, request.UniqueID, request.Keep, actor)
			if err != nil {
				return err
			}
			for _, d := range revoked {
				out.Revoked = append(out.Revoked, deviceInfo(d, ""))
			}
		}
		devices, err := s.deps.Devices.ListDevices(ctx, request.UniqueID, false)
		if err != nil {
			return err
		}
		out.Devices = make([]netproto.DeviceInfo, 0, len(devices))
		for _, d := range devices {
			out.Devices = append(out.Devices, deviceInfo(d, ""))
		}
		return deliver(ctx, out)
	})
}
