package server

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"noxa/internal/netproto"
	"noxa/internal/permissions"
	"noxa/internal/store"
)

type adminListFunc func(context.Context) ([]store.AdminIdentity, error)

func (f adminListFunc) ListServerAdmins(ctx context.Context) ([]store.AdminIdentity, error) {
	return f(ctx)
}

func TestServerAdminList(t *testing.T) {
	for _, tc := range []struct {
		name    string
		user    string
		backend bool
		fail    bool
		code    uint16
	}{
		{name: "admin includes offline identities", user: "admin-uid", backend: true},
		{name: "member denied despite group management permission", user: "user-uid", backend: true, code: errCodePermissionDenied},
		{name: "unavailable backend", user: "admin-uid", code: errCodeUnavailable},
		{name: "failed query", user: "admin-uid", backend: true, fail: true, code: errCodeUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			tp := tieredWith(boolPerm(permissions.PermissionKeyServerGroupManage, true))
			env := startTestEnvDeps(t, &tp, nil, func(deps *Deps) {
				if tc.backend {
					deps.ServerAdmins = adminListFunc(func(ctx context.Context) ([]store.AdminIdentity, error) {
						calls.Add(1)
						if tc.fail {
							return nil, errors.New("private database detail")
						}
						return []store.AdminIdentity{{UniqueID: "admin-uid", Nickname: "admin"}, {UniqueID: "offline-uid", Nickname: "Offline Admin"}}, ctx.Err()
					})
				}
			})
			defer env.stop()
			conn, _ := dialAuthed(t, env.addr, tc.user)
			defer func() { _ = conn.Close() }()
			send(t, conn, netproto.MsgServerAdminList, netproto.ServerAdminList{})
			if tc.code != 0 {
				e := readError(t, conn)
				if e.Code != tc.code {
					t.Fatalf("error = %+v, want code %d", e, tc.code)
				}
				if tc.user == "user-uid" && calls.Load() != 0 {
					t.Fatal("non-admin reached the roster store")
				}
				return
			}
			var resp netproto.ServerAdmins
			if err := netproto.Decode(readOfType(t, conn, netproto.MsgServerAdmins), &resp); err != nil {
				t.Fatal(err)
			}
			if len(resp.Entries) != 2 || resp.Entries[1].UniqueID != "offline-uid" {
				t.Fatalf("admin roster = %+v", resp)
			}
		})
	}
}
