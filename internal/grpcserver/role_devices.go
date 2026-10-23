package grpcserver

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"noxa/internal/auth"
	"noxa/internal/netproto"
	"noxa/internal/query"
	noxav1 "noxa/v1"
)

func (c *controlService) ListDevices(ctx context.Context, req *noxav1.ListDevicesRequest) (*noxav1.DevicesResponse, error) {
	return c.deviceRequest(ctx, netproto.IntegrationDeviceRequest{Action: "list", UniqueID: req.GetUniqueId()})
}

func (c *controlService) RevokeDevice(ctx context.Context, req *noxav1.RevokeDeviceRequest) (*noxav1.DevicesResponse, error) {
	return c.deviceRequest(ctx, netproto.IntegrationDeviceRequest{Action: "revoke", UniqueID: req.GetUniqueId(), DeviceID: req.GetDeviceId()})
}

func (c *controlService) ResetDevices(ctx context.Context, req *noxav1.ResetDevicesRequest) (*noxav1.DevicesResponse, error) {
	return c.deviceRequest(ctx, netproto.IntegrationDeviceRequest{Action: "reset", UniqueID: req.GetUniqueId(), Keep: req.GetKeepDeviceId()})
}

func (c *controlService) deviceRequest(ctx context.Context, request netproto.IntegrationDeviceRequest) (*noxav1.DevicesResponse, error) {
	p, authenticated := ctx.Value(integrationPrincipalKey{}).(auth.IntegrationPrincipal)
	b, supported := c.backend.(query.RoleDeviceBackend)
	if !authenticated || !supported {
		return nil, status.Error(codes.FailedPrecondition, "role integration required")
	}
	if !request.Valid() {
		return nil, status.Error(codes.InvalidArgument, "unique_id is required; revoke also requires device_id")
	}
	return protectedUnaryRead(ctx, c.logger, func(ctx context.Context, deliver func(*noxav1.DevicesResponse) error) error {
		return b.WithIntegrationDevices(ctx, p, request, func(_ context.Context, result netproto.IntegrationDeviceResult) error {
			out := &noxav1.DevicesResponse{UniqueId: result.UniqueID}
			for _, d := range result.Devices {
				out.Devices = append(out.Devices, deviceRecord(d))
			}
			for _, d := range result.Revoked {
				out.Revoked = append(out.Revoked, deviceRecord(d))
			}
			return deliver(out)
		})
	})
}

func deviceRecord(d netproto.DeviceInfo) *noxav1.DeviceRecord {
	return &noxav1.DeviceRecord{
		DeviceId: d.DeviceID, Kind: d.Kind, Name: d.Name, Fingerprint: d.Fingerprint, FirstDevice: d.FirstDevice,
		ApprovedBy: d.ApprovedBy, CreatedAt: d.CreatedAt, LastSeenAt: d.LastSeenAt, RevokedAt: d.RevokedAt,
	}
}
