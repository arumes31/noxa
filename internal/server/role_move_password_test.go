package server

import (
	"context"
	"errors"
	"testing"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

// A forced move delivers the destination's keys, so it must not get around a
// channel password unless the mover or the member may bypass it.
func TestRoleMoveIntoPasswordChannelRequiresBypass(t *testing.T) {
	for _, tt := range []struct {
		name   string
		bypass bool
	}{{"without bypass", false}, {"with bypass", true}} {
		t.Run(tt.name, func(t *testing.T) {
			backend := serverRoleFixture()
			backend.policy.OwnerID = 3
			backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.Connect}
			backend.policy.Channels = append(backend.policy.Channels, authorization.ChannelPolicy{ChannelID: 2})
			permissions := []authorization.Capability{authorization.MoveMembers}
			if tt.bypass {
				permissions = append(permissions, authorization.BypassChannelPassword)
			}
			backend.policy.Roles = append(backend.policy.Roles, authorization.Role{ID: 20, Name: "Mover", Position: 1, Permissions: permissions})
			backend.policy.Members = []authorization.RoleMember{{UserID: 2, RoleIDs: []int64{20}}}
			authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority })
			defer env.stop()
			env.state.AddChannel(testChannel(1))
			locked := testChannel(2)
			locked.PasswordHash = "$argon2id$v=19$m=65536,t=1,p=1$c2FsdA$aGFzaA"
			env.state.AddChannel(locked)
			memberConn, memberID := dialAuthed(t, env.addr, "admin-uid")
			defer func() { _ = memberConn.Close() }()
			if err := env.state.MoveClient(memberID, 1); err != nil {
				t.Fatal(err)
			}
			evaluator, err := authorization.NewRoleEvaluator(backend.policy)
			if err != nil {
				t.Fatal(err)
			}
			err = env.srv.moveRoleMember(t.Context(), evaluator, 2, "user-uid", "", netproto.MoveClient{ClientID: memberID, ChannelID: 2})
			after, _ := env.state.GetClient(memberID)
			if tt.bypass {
				if err != nil || after.ChannelID != 2 {
					t.Fatalf("move with bypass: err=%v channel=%d", err, after.ChannelID)
				}
				return
			}
			if !errors.Is(err, authorization.ErrRoleForbidden) || after.ChannelID != 1 {
				t.Fatalf("move without bypass: err=%v channel=%d, want ErrRoleForbidden and no move", err, after.ChannelID)
			}
		})
	}
}
