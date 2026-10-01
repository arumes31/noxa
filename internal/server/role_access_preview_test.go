package server

import (
	"context"
	"testing"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

func TestRoleAccessPreviewSeparatesSubjectGrantAndActorHierarchy(t *testing.T) {
	backend := serverRoleFixture()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.Connect, authorization.Speak}
	backend.policy.Channels[0].Overrides = []authorization.RoleOverride{{RoleID: 10, Capability: authorization.ViewChannel, Effect: authorization.Deny}}
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority })
	defer env.stop()
	owner, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = owner.Close() }()
	for _, subject := range []int64{0, 1, 2} {
		send(t, owner, netproto.MsgAccessCheck, netproto.AccessCheck{UserID: subject, ChannelID: 1, Capability: authorization.Speak, ExpectedRevision: 1})
		var response netproto.AccessCheckResult
		if err := netproto.Decode(readOfType(t, owner, netproto.MsgAccessCheckResult), &response); err != nil {
			t.Fatal(err)
		}
		if response.Decision.Revision != 1 || response.Decision.Allowed != (subject == 2) || response.CanManageMember != (subject != 2) {
			t.Fatalf("subject %d preview: %+v", subject, response)
		}
		if subject != 2 && (response.Decision.Reason != "requires" || response.Decision.Requirement != authorization.Connect) {
			t.Fatalf("missing connect prerequisite: %+v", response.Decision)
		}
	}
	for _, request := range []netproto.AccessCheck{
		{UserID: 1, ChannelID: 1, Capability: authorization.Speak, ExpectedRevision: 2},
		{UserID: 1, ChannelID: 99, Capability: authorization.Speak, ExpectedRevision: 1},
	} {
		send(t, owner, netproto.MsgAccessCheck, request)
		want := uint16(errCodeConflict)
		if request.ChannelID == 99 {
			want = errCodePermissionDenied
		}
		if got := readError(t, owner); got.Code != want {
			t.Fatalf("invalid preview accepted: %+v", got)
		}
	}
	member, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = member.Close() }()
	send(t, member, netproto.MsgAccessCheck, netproto.AccessCheck{UserID: 2, ChannelID: 1, Capability: authorization.Speak, ExpectedRevision: 1})
	if got := readError(t, member); got.Code != errCodePermissionDenied {
		t.Fatalf("legacy admin read protected preview: %+v", got)
	}
	policy, err := authority.RolePolicy(t.Context())
	if err != nil || policy.Revision != 1 {
		t.Fatalf("preview mutated policy: %+v, %v", policy, err)
	}
}
