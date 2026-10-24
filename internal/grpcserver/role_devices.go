package grpcserver

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"noxa/internal/auth"
	"noxa/internal/netproto"
	"noxa/internal/query"
	noxav1 "noxa/v1"
)

func (c *controlService) ListDevices(ctx context.Context, req *noxav1.ListDevicesRequest) (*noxav1.ListDevicesResponse, error) {
	return deviceRequest(ctx, c, netproto.IntegrationDeviceRequest{Action: "list", UniqueID: req.GetUniqueId()},
		func(uid string, devices, _ []*noxav1.DeviceRecord) *noxav1.ListDevicesResponse {
			return &noxav1.ListDevicesResponse{UniqueId: uid, Devices: devices}
		})
}

func (c *controlService) RevokeDevice(ctx context.Context, req *noxav1.RevokeDeviceRequest) (*noxav1.RevokeDeviceResponse, error) {
	return deviceRequest(ctx, c, netproto.IntegrationDeviceRequest{Action: "revoke", UniqueID: req.GetUniqueId(), DeviceID: req.GetDeviceId()},
		func(uid string, devices, revoked []*noxav1.DeviceRecord) *noxav1.RevokeDeviceResponse {
			return &noxav1.RevokeDeviceResponse{UniqueId: uid, Devices: devices, Revoked: revoked}
		})
}

func (c *controlService) ResetDevices(ctx context.Context, req *noxav1.ResetDevicesRequest) (*noxav1.ResetDevicesResponse, error) {
	return deviceRequest(ctx, c, netproto.IntegrationDeviceRequest{Action: "reset", UniqueID: req.GetUniqueId(), Keep: req.GetKeepDeviceId()},
		func(uid string, devices, revoked []*noxav1.DeviceRecord) *noxav1.ResetDevicesResponse {
			return &noxav1.ResetDevicesResponse{UniqueId: uid, Devices: devices, Revoked: revoked}
		})
}

func deviceRequest[T proto.Message](ctx context.Context, c *controlService, request netproto.IntegrationDeviceRequest, build func(uid string, devices, revoked []*noxav1.DeviceRecord) T) (T, error) {
	var zero T
	p, authenticated := ctx.Value(integrationPrincipalKey{}).(auth.IntegrationPrincipal)
	b, supported := c.backend.(query.RoleDeviceBackend)
	if !authenticated || !supported {
		return zero, status.Error(codes.FailedPrecondition, "role integration required")
	}
	if !request.Valid() {
		return zero, status.Error(codes.InvalidArgument, "unique_id is required; revoke also requires device_id")
	}
	return protectedUnaryRead(ctx, c.logger, func(ctx context.Context, deliver func(T) error) error {
		return b.WithIntegrationDevices(ctx, p, request, func(_ context.Context, result netproto.IntegrationDeviceResult) error {
			var devices, revoked []*noxav1.DeviceRecord
			for _, d := range result.Devices {
				devices = append(devices, deviceRecord(d))
			}
			for _, d := range result.Revoked {
				revoked = append(revoked, deviceRecord(d))
			}
			return deliver(build(result.UniqueID, devices, revoked))
		})
	})
}

func deviceRecord(d netproto.DeviceInfo) *noxav1.DeviceRecord {
	return &noxav1.DeviceRecord{
		DeviceId: d.DeviceID, Kind: d.Kind, Name: d.Name, Fingerprint: d.Fingerprint, FirstDevice: d.FirstDevice,
		ApprovedBy: d.ApprovedBy, CreatedAt: d.CreatedAt, LastSeenAt: d.LastSeenAt, RevokedAt: d.RevokedAt,
	}
}
