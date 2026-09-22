package server

import (
	"context"
	"testing"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

func TestRoleServerManagementCapabilities(t *testing.T) {
	words := "updated-filter"
	for _, command := range []struct {
		name        string
		capability  authorization.Capability
		kind, reply netproto.MessageType
		body        any
		asset       string
	}{
		{"icon", authorization.ManageServer, netproto.MsgServerIconSet, 0, netproto.ServerIconSet{DataBase64: b64(tinyPNG)}, "server_icon"},
		{"banner", authorization.ManageServer, netproto.MsgServerBannerSet, 0, netproto.ServerBannerSet{DataBase64: b64(tinyPNG)}, "server_banner"},
		{"filter-read", authorization.ManageChatFilters, netproto.MsgChatFilterGet, netproto.MsgChatFilterResponse, netproto.ChatFilterGet{}, ""},
		{"filter-write", authorization.ManageChatFilters, netproto.MsgChatFilterSet, netproto.MsgChatFilterResponse, netproto.ChatFilterSet{WordFilter: &words}, ""},
		{"complaint-read", authorization.BanMembers, netproto.MsgComplaintList, netproto.MsgComplaints, netproto.ComplaintList{}, ""},
		{"complaint-clear", authorization.BanMembers, netproto.MsgComplaintClear, netproto.MsgComplaints, netproto.ComplaintClear{TargetUniqueID: "reported"}, ""},
		{"audit", authorization.ViewAuditLog, netproto.MsgAuditLog, netproto.MsgAuditLogResponse, netproto.AuditLog{}, ""},
	} {
		for _, mode := range []string{"denied-legacy-admin", "owner", "delegated", "revoked"} {
			t.Run(command.name+"/"+mode, func(t *testing.T) {
				backend := serverRoleFixture()
				if mode == "delegated" || mode == "revoked" {
					backend.policy.Roles[0].Permissions = []authorization.Capability{command.capability}
				}
				authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
				if err != nil {
					t.Fatal(err)
				}
				env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority })
				defer env.stop()
				if err := env.complaints.AddComplaint(t.Context(), "reporter", "reported", "private evidence"); err != nil {
					t.Fatal(err)
				}
				env.groups.Audit(t.Context(), "actor", "private-action", "target", "detail")
				identity := "admin-uid"
				if mode == "owner" {
					identity = "user-uid"
				}
				conn, _ := dialAuthed(t, env.addr, identity)
				defer func() { _ = conn.Close() }()
				if mode == "revoked" {
					_, err := authority.ChangeRolePolicy(t.Context(), 2, authorization.RoleChange{Kind: authorization.RoleUpdate, ExpectedRevision: 1, Role: authorization.Role{ID: 10, Name: "@everyone"}})
					if err != nil {
						t.Fatal(err)
					}
				}
				allowed := mode == "owner" || mode == "delegated"
				send(t, conn, command.kind, command.body)
				send(t, conn, netproto.MsgPing, netproto.Ping{})
				var denied, replied bool
				for {
					frame := readFrame(t, conn)
					kind := netproto.MessageType(frame.Type)
					if kind == netproto.MsgPong {
						break
					}
					switch kind {
					case netproto.MsgError:
						var failure netproto.Error
						if err := netproto.Decode(frame, &failure); err != nil {
							t.Fatal(err)
						}
						if failure.Code != errCodePermissionDenied || failure.OriginType != uint16(command.kind) {
							t.Fatalf("unexpected denial: %+v", failure)
						}
						denied = true
					case command.reply:
						replied = true
					}
				}
				if denied == allowed || (command.reply != 0 && replied != allowed) {
					t.Fatalf("allowed=%v denied=%v replied=%v", allowed, denied, replied)
				}
				if command.asset != "" {
					raw, _, err := env.srv.assets().readImage("", command.asset)
					if allowed && (err != nil || string(raw) != string(tinyPNG)) {
						t.Fatalf("granted branding not saved: %v", err)
					}
					if !allowed && err == nil {
						t.Fatal("denied branding was saved")
					}
				}
				if command.name == "filter-write" {
					filters, _ := env.srv.effectiveFilters(t.Context())
					if (filters.WordFilter == words) != allowed {
						t.Fatalf("filter mutation: %+v", filters)
					}
				}
				if command.name == "complaint-clear" {
					rows, err := env.complaints.ListComplaints(t.Context())
					if err != nil || (len(rows) == 0) != allowed {
						t.Fatalf("complaint mutation: %v %v", rows, err)
					}
				}
			})
		}
	}
}
