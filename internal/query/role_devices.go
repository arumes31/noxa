package query

import (
	"context"
	"time"

	"noxa/internal/auth"
	"noxa/internal/netproto"
)

// RoleDeviceBackend administers linked devices of an account.
type RoleDeviceBackend interface {
	WithIntegrationDevices(context.Context, auth.IntegrationPrincipal, netproto.IntegrationDeviceRequest, func(context.Context, netproto.IntegrationDeviceResult) error) error
}

// executeRoleDevices serves devicelist, devicerevoke and devicereset.
func (s *Server) executeRoleDevices(ctx context.Context, sess *session, backend RoleIntegrationBackend, cmd command) bool {
	b, ok := backend.(RoleDeviceBackend)
	if !ok {
		return s.write(sess, errorLine(errInsufficientPermissions, "device administration unavailable"))
	}
	request := netproto.IntegrationDeviceRequest{UniqueID: cmd.args["uid"], DeviceID: cmd.args["did"], Keep: cmd.args["keep"]}
	switch cmd.name {
	case "devicelist":
		request.Action = "list"
	case "devicerevoke":
		request.Action = "revoke"
	case "devicereset":
		request.Action = "reset"
	}
	if !request.Valid() {
		return s.write(sess, errorLine(errInvalidParameter, "devicelist uid=<unique_id>; devicerevoke uid=<unique_id> did=<device_id>; devicereset uid=<unique_id> [keep=<device_id>]"))
	}
	return s.executeIntegrationJSON(ctx, sess, 10*time.Second, func(ctx context.Context, deliver func(context.Context, any) error) error {
		return b.WithIntegrationDevices(ctx, sess.principal, request, func(ctx context.Context, result netproto.IntegrationDeviceResult) error {
			return deliver(ctx, result)
		})
	})
}
