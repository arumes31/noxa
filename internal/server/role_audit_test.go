package server

import (
	"context"
	"encoding/json"
	"testing"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"noxa/internal/store"
)

func TestRoleAuditRedactsEveryProtectedField(t *testing.T) {
	backend := serverRoleFixture()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewAuditLog, authorization.ViewChannel}
	backend.policy.Channels = append(backend.policy.Channels, authorization.ChannelPolicy{ChannelID: 2, Overrides: []authorization.RoleOverride{{RoleID: 10, Capability: authorization.ViewChannel, Effect: authorization.Deny}}})
	a, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = a })
	defer env.stop()
	for _, tc := range []struct {
		target   string
		channels []int64
	}{
		{"server", []int64{}}, {"visible", []int64{1}}, {"hidden", []int64{2}},
		{"mixed", []int64{1, 2}}, {"deleted", []int64{99}}, {"unspecified", nil},
	} {
		detail, err := json.Marshal(store.AuditDetail{Version: store.AuditDetailVersion, Channels: tc.channels, Text: "private details"})
		if err != nil {
			t.Fatal(err)
		}
		env.groups.AuditScoped(t.Context(), "private-actor", "private-action", tc.target, string(detail), tc.channels)
	}
	env.groups.Audit(t.Context(), "legacy-actor", "legacy-action", "legacy-private-target", "unscoped legacy text")
	env.groups.Audit(t.Context(), "legacy-actor", "channel_create", "legacy-private-id", `{"version":1,"channel_ids":[]}`)
	for _, identity := range []string{"admin-uid", "user-uid"} {
		conn, _ := dialAuthed(t, env.addr, identity)
		defer func() { _ = conn.Close() }()
		send(t, conn, netproto.MsgAuditLog, netproto.AuditLog{})
		var page netproto.AuditLogResponse
		if err := netproto.Decode(readOfType(t, conn, netproto.MsgAuditLogResponse), &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Entries) != 8 {
			t.Fatalf("pagination lost rows: %d", len(page.Entries))
		}
		var restricted int
		for _, entry := range page.Entries {
			if identity == "user-uid" {
				wantStructured := entry.Actor != "legacy-actor" && entry.Target != "unspecified"
				if entry.Structured != wantStructured {
					t.Fatalf("incorrect audit provenance: %+v", entry)
				}
				if entry.Restricted || entry.Target == "" {
					t.Fatal("owner lost audit history")
				}
				continue
			}
			if entry.Target == "server" || entry.Target == "visible" {
				if entry.Restricted || entry.Detail == "" {
					t.Fatal("visible audit was redacted")
				}
				continue
			}
			if !entry.Restricted || entry.Actor != "" || entry.Action != "" || entry.Target != "" || entry.Detail != "" {
				t.Fatalf("protected audit leaked: %+v", entry)
			}
			restricted++
		}
		if identity == "admin-uid" && restricted != 6 {
			t.Fatalf("redacted %d rows, want 6", restricted)
		}
		if identity == "admin-uid" {
			_, err := a.ChangeRolePolicy(t.Context(), 2, authorization.RoleChange{Kind: authorization.RoleUpdate, ExpectedRevision: 1,
				Role: authorization.Role{ID: 10, Name: "@everyone", Permissions: []authorization.Capability{authorization.ViewAuditLog}}})
			if err != nil {
				t.Fatal(err)
			}
			send(t, conn, netproto.MsgAuditLog, netproto.AuditLog{})
			if err := netproto.Decode(readOfType(t, conn, netproto.MsgAuditLogResponse), &page); err != nil {
				t.Fatal(err)
			}
			for _, entry := range page.Entries {
				if entry.Target != "server" && !entry.Restricted {
					t.Fatalf("revoked audit remained visible: %+v", entry)
				}
			}
		}
	}
}

func TestRoleChannelKickAuditRetainsOldScope(t *testing.T) {
	a, err := authorization.NewAuthority(t.Context(), serverRoleFixture(), func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = a })
	defer env.stop()
	env.state.AddChannel(testChannel(1))
	owner, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = owner.Close() }()
	member, memberID := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = member.Close() }()
	if err := env.state.MoveClient(memberID, 1); err != nil {
		t.Fatal(err)
	}
	send(t, owner, netproto.MsgKickClient, netproto.KickClient{
		ClientID: memberID, ExpectedChannelID: 1, Reason: "private reason", AckRequested: true,
	})
	readOfType(t, owner, netproto.MsgClientRemoved)
	entries, err := env.groups.AuditList(t.Context(), 0, 50)
	if err != nil || len(entries) != 1 || len(entries[0].ChannelIDs) != 1 || entries[0].ChannelIDs[0] != 1 {
		t.Fatalf("kick audit scope: %+v, %v", entries, err)
	}
}
