//go:build integration

package server

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"go.uber.org/zap"
	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/config"
	"noxa/internal/netproto"
	"noxa/internal/state"
)

type metadataTestConn struct{ *blockingTCPConn }

func (*metadataTestConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("192.0.2.81"), Port: 1234}
}

func TestIntegrationMetadataFiltersCurrentScopeAndSensitiveFields(t *testing.T) {
	db := integrationManagementStore(t)
	a := auth.New(db, zap.NewNop())
	uid, err := a.RegisterUser(t.Context(), "metadata-reader", "integration-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET integration_enabled=TRUE WHERE unique_id=$1", uid); err != nil {
		t.Fatal(err)
	}
	p, err := a.AuthenticateIntegration(t.Context(), uid, "integration-password", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	backend := serverRoleFixture()
	backend.policy.OwnerID = p.UserID() + 100
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel}
	backend.policy.Channels = []authorization.ChannelPolicy{
		{ChannelID: 1},
		{ChannelID: 2, Overrides: []authorization.RoleOverride{{RoleID: 10, Capability: authorization.ViewChannel, Effect: authorization.Deny}}},
	}
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	sm := state.New(zap.NewNop())
	sm.AddChannel(testChannel(1))
	sm.AddChannel(testChannel(2))
	if err := db.SetServerSetting(t.Context(), "motd", "Visible welcome", 0); err != nil {
		t.Fatal(err)
	}
	srv := New(&config.Config{ServerName: "Private server", ServerInfoMOTD: true, MaxClients: 25}, zap.NewNop(), &Deps{Authority: authority, Auth: a, State: sm, Chat: db})
	add := func(id, uniqueID string, channelID int64) {
		userID := p.UserID() + 10
		if uniqueID == uid {
			userID = p.UserID()
		}
		c := &Client{ID: id, Conn: &metadataTestConn{newBlockingTCPConn()}, lastActive: time.Now(), bytesIn: 123, rttKnown: true, rttNs: int64(time.Millisecond)}
		c.setIdentity(uniqueID, id, userID, false)
		srv.register(c)
		sm.AddClient(&state.Client{ClientID: id, UserID: userID, UniqueID: uniqueID, Nickname: id, ConnectedAt: time.Now()})
		if err := sm.MoveClient(id, channelID); err != nil {
			t.Fatal(err)
		}
	}
	add("visible", "other-uid", 1)
	add("hidden", "hidden-uid", 2)
	add("own-account-session", uid, 1)
	clientInfo := func(target string) (netproto.ClientInfoResponse, error) {
		var info netproto.ClientInfoResponse
		err := srv.WithIntegrationClientInfo(t.Context(), p, target, func(_ context.Context, result netproto.ClientInfoResponse) error { info = result; return nil })
		return info, err
	}
	for _, target := range []string{"visible", "own-account-session"} {
		info, err := clientInfo(target)
		if err != nil || info.ClientID != target || info.IP != "" || info.BytesIn != 0 || info.ConnectedAt != 0 || info.PingMs != -1 {
			t.Fatalf("ordinary member or same-account bypass: %+v %v", info, err)
		}
	}
	for _, target := range []string{"hidden", "missing"} {
		if _, err := clientInfo(target); !errors.Is(err, authorization.ErrRoleForbidden) {
			t.Fatalf("inaccessible target %q: %v", target, err)
		}
	}
	serverInfo := func() (netproto.ServerInfoResponse, error) {
		var info netproto.ServerInfoResponse
		err := srv.WithIntegrationServerInfo(t.Context(), p, func(_ context.Context, result netproto.ServerInfoResponse) error { info = result; return nil })
		return info, err
	}
	info, err := serverInfo()
	if err != nil || info.ClientsOnline != 2 || info.ChannelsOnline != 1 || info.MOTD != "Visible welcome" || info.MaxClients != 25 {
		t.Fatalf("filtered server counts: %+v %v", info, err)
	}
	sm.SetStatus("visible", "invisible", "")
	if _, err := clientInfo("visible"); !errors.Is(err, authorization.ErrRoleForbidden) {
		t.Fatalf("invisible member leaked: %v", err)
	}
	setPermissions := func(caps ...authorization.Capability) {
		t.Helper()
		backend.mu.Lock()
		backend.policy.Roles[0].Permissions = caps
		backend.policy.Revision++
		backend.mu.Unlock()
		if err := authority.Reload(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	configRead := func() error {
		return srv.WithIntegrationServerConfig(t.Context(), p, func(_ context.Context, result netproto.ServerConfig) error {
			if result.MaxClients != 25 {
				return errors.New("configuration changed")
			}
			return nil
		})
	}
	if err := configRead(); !errors.Is(err, authorization.ErrRoleForbidden) {
		t.Fatalf("ordinary member could read configuration: %v", err)
	}
	setPermissions(authorization.ViewChannel, authorization.ViewConnectionInfo)
	stats, err := clientInfo("visible")
	if err != nil || stats.IP != "" || stats.BytesIn != 123 || stats.PingMs != 1 || stats.ConnectedAt == 0 {
		t.Fatalf("statistics/address gates conflated: %+v %v", stats, err)
	}
	setPermissions(authorization.ViewChannel, authorization.ViewRemoteAddresses, authorization.ManageServer)
	sm.SetStatus("visible", "online", "")
	address, err := clientInfo("visible")
	if err != nil || address.IP != "192.0.2.81" || address.Port != 1234 || address.BytesIn != 0 || address.ConnectedAt != 0 {
		t.Fatalf("address/statistics gates conflated: %+v %v", address, err)
	}
	if err := configRead(); err != nil {
		t.Fatal(err)
	}
	setPermissions()
	info, err = serverInfo()
	if err != nil || info.ChannelsOnline != 0 || info.ClientsOnline != 0 || info.MOTD != "" {
		t.Fatalf("same principal retained counts or MOTD: %+v %v", info, err)
	}
	if _, err := clientInfo("own-account-session"); !errors.Is(err, authorization.ErrRoleForbidden) {
		t.Fatalf("account match bypassed channel visibility: %v", err)
	}
	if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET integration_enabled=FALSE WHERE id=$1", p.UserID()); err != nil {
		t.Fatal(err)
	}
	if _, err := serverInfo(); !errors.Is(err, auth.ErrIntegrationDenied) {
		t.Fatalf("disabled account read metadata: %v", err)
	}
}
