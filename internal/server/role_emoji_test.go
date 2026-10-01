package server

import (
	"context"
	"testing"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

func TestEmojiDenialPreventsMutation(t *testing.T) {
	for _, roleMode := range []bool{false, true} {
		name, identity := "legacy", "user-uid"
		if roleMode {
			name, identity = "roles", "admin-uid"
		}
		t.Run(name, func(t *testing.T) {
			backend := serverRoleFixture()
			authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			env := startTestEnvDeps(t, nil, nil, func(d *Deps) {
				if roleMode {
					d.Authority = authority
				}
			})
			defer env.stop()
			conn, _ := dialAuthed(t, env.addr, identity)
			defer func() { _ = conn.Close() }()
			if _, err := env.srv.assets().writeImage("emojis", "kept", ".png", tinyPNG); err != nil {
				t.Fatal(err)
			}
			for _, request := range []struct {
				kind netproto.MessageType
				body any
			}{
				{netproto.MsgEmojiRename, netproto.EmojiRename{Name: "kept", NewName: "stolen"}},
				{netproto.MsgEmojiDelete, netproto.EmojiDelete{Name: "kept"}},
				{netproto.MsgEmojiUpload, netproto.EmojiUpload{Name: "created", DataBase64: b64(tinyPNG)}},
			} {
				send(t, conn, request.kind, request.body)
				if got := readError(t, conn); got.Code != errCodePermissionDenied {
					t.Fatalf("%v denial: %+v", request.kind, got)
				}
				// Drain through the next request: the denial alone does not prove
				// that the denied handler stopped before touching storage.
				send(t, conn, netproto.MsgPing, netproto.Ping{})
				readOfType(t, conn, netproto.MsgPong)
				if raw, _, err := env.srv.assets().readImage("emojis", "kept"); err != nil || string(raw) != string(tinyPNG) {
					t.Fatalf("denied mutation changed original: %v", err)
				}
				for _, key := range []string{"stolen", "created"} {
					if _, _, err := env.srv.assets().readImage("emojis", key); err == nil {
						t.Fatalf("denied mutation created %s", key)
					}
				}
			}
		})
	}
}

func TestRoleEmojiManagementUsesCapability(t *testing.T) {
	backend := serverRoleFixture()
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority })
	defer env.stop()
	owner, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = owner.Close() }()
	send(t, owner, netproto.MsgEmojiUpload, netproto.EmojiUpload{Name: "hello", DataBase64: b64(tinyPNG)})
	readEventOfType(t, owner, eventEmojiAdded)
	send(t, owner, netproto.MsgEmojiRename, netproto.EmojiRename{Name: "hello", NewName: "wave"})
	readEventOfType(t, owner, eventEmojiRenamed)
	_, err = authority.ChangeRolePolicy(t.Context(), 2, authorization.RoleChange{Kind: authorization.RoleUpdate, ExpectedRevision: 1,
		Role: authorization.Role{ID: 10, Name: "@everyone", Permissions: []authorization.Capability{authorization.ManageEmoji}}})
	if err != nil {
		t.Fatal(err)
	}
	member, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = member.Close() }()
	send(t, member, netproto.MsgEmojiDelete, netproto.EmojiDelete{Name: "wave"})
	readEventOfType(t, member, eventEmojiRemoved)
	if _, _, err := env.srv.assets().readImage("emojis", "wave"); err == nil {
		t.Fatal("granted delete left emoji behind")
	}
}
