//go:build integration

package server

import (
	"context"
	"errors"
	"testing"

	"go.uber.org/zap"

	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/config"
	"noxa/internal/netproto"
	"noxa/internal/store"
)

func TestIntegrationDevicesRequireAdministrator(t *testing.T) {
	db := integrationManagementStore(t)
	a := auth.New(db, zap.NewNop())
	uid, err := a.RegisterUser(t.Context(), "device-admin", "integration-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET integration_enabled=TRUE WHERE unique_id=$1", uid); err != nil {
		t.Fatal(err)
	}
	principal, err := a.AuthenticateIntegration(t.Context(), uid, "integration-password", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	newServer := func(owner int64) *TCPServer {
		backend := serverRoleFixture()
		backend.policy.OwnerID = owner
		authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
		if err != nil {
			t.Fatal(err)
		}
		return New(&config.Config{}, zap.NewNop(), &Deps{Authority: authority, Auth: a, Devices: db})
	}
	call := func(srv *TCPServer, request netproto.IntegrationDeviceRequest) (netproto.IntegrationDeviceResult, error) {
		var out netproto.IntegrationDeviceResult
		err := srv.WithIntegrationDevices(t.Context(), principal, request, func(_ context.Context, r netproto.IntegrationDeviceResult) error {
			out = r
			return nil
		})
		return out, err
	}

	target := "device-target-" + uid
	var ids []string
	for i := 0; i < 3; i++ {
		keys := newLinkTestKeys(t)
		d, err := db.AddDevice(t.Context(), store.NewDevice{Principal: target, Kind: store.DeviceKindWeb, Name: "Phone", Ed25519Public: keys.public}, 10)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, d.DeviceID)
	}

	if _, err := call(newServer(principal.UserID()+100), netproto.IntegrationDeviceRequest{Action: "list", UniqueID: target}); !errors.Is(err, authorization.ErrRoleForbidden) {
		t.Fatalf("non-administrator list err = %v", err)
	}
	admin := newServer(principal.UserID())
	listed, err := call(admin, netproto.IntegrationDeviceRequest{Action: "list", UniqueID: target})
	if err != nil || len(listed.Devices) != 3 {
		t.Fatalf("list = %+v, %v", listed, err)
	}
	revoked, err := call(admin, netproto.IntegrationDeviceRequest{Action: "revoke", UniqueID: target, DeviceID: ids[2]})
	if err != nil || len(revoked.Revoked) != 1 || len(revoked.Devices) != 2 {
		t.Fatalf("revoke = %+v, %v", revoked, err)
	}
	reset, err := call(admin, netproto.IntegrationDeviceRequest{Action: "reset", UniqueID: target, Keep: ids[0]})
	if err != nil || len(reset.Revoked) != 1 || len(reset.Devices) != 1 || reset.Devices[0].DeviceID != ids[0] {
		t.Fatalf("reset = %+v, %v", reset, err)
	}
	if _, err := call(admin, netproto.IntegrationDeviceRequest{Action: "revoke", UniqueID: target, DeviceID: ids[2]}); !errors.Is(err, authorization.ErrRoleInvalid) {
		t.Fatalf("second revoke err = %v", err)
	}
}
