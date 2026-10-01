package server

import (
	"context"
	"errors"
	"testing"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

func TestRoleChatReadsRejectLegacyAdminAndUnknownScopes(t *testing.T) {
	store := serverRoleFixture()
	authority, err := authorization.NewAuthority(t.Context(), store, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority })
	defer env.stop()
	for _, identity := range []string{"admin-uid", "user-uid"} {
		conn, clientID := dialAuthed(t, env.addr, identity)
		defer func() { _ = conn.Close() }()
		client, _ := env.srv.clientByID(clientID)
		scopes := []int64{999, -1}
		if identity == "admin-uid" {
			scopes = append(scopes, 0, 1)
		}
		for _, scope := range scopes {
			for _, request := range []struct {
				kind netproto.MessageType
				body any
			}{
				{netproto.MsgChatHistory, netproto.ChatHistory{ChannelID: scope}},
				{netproto.MsgChatPins, netproto.ChatPins{ChannelID: scope}},
				{netproto.MsgChatKeyRequest, netproto.ChatKeyRequest{ChannelID: scope, KeyIDs: []uint32{1}}},
			} {
				send(t, conn, request.kind, request.body)
				if got := readError(t, conn); got.Code != errCodePermissionDenied {
					t.Fatalf("%s scope %d %v: %+v", identity, scope, request.kind, got)
				}
			}
			if err := env.srv.deliverScopeKey(t.Context(), client, scope); !errors.Is(err, authorization.ErrRoleForbidden) {
				t.Fatalf("key delivery scope %d: %v", scope, err)
			}
		}
	}
}

func TestRoleLeaseChecksNestedTargetWithoutReacquiring(t *testing.T) {
	store := serverRoleFixture()
	store.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel}
	store.policy.Channels = append(store.policy.Channels, authorization.ChannelPolicy{ChannelID: 2, Overrides: []authorization.RoleOverride{{RoleID: 10, Capability: authorization.ViewChannel, Effect: authorization.Deny}}})
	authority, err := authorization.NewAuthority(t.Context(), store, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority })
	defer env.stop()
	conn, id := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = conn.Close() }()
	client, _ := env.srv.clientByID(id)
	err = env.srv.withRoleAccess(t.Context(), client, 1, authorization.ViewChannel, func(ctx context.Context) error {
		if !env.srv.scopeReadable(ctx, client, 1) {
			t.Error("explicit target grant denied without membership")
		}
		if env.srv.scopeReadable(ctx, client, 2) {
			t.Error("nested check inherited another channel's grant")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRoleChatMessageMutationsUseStoredScope(t *testing.T) {
	backend := serverRoleFixture()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.SendMessages}
	backend.policy.Channels = append(backend.policy.Channels, authorization.ChannelPolicy{ChannelID: 2, Overrides: []authorization.RoleOverride{{RoleID: 10, Capability: authorization.ViewChannel, Effect: authorization.Deny}}})
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority })
	defer env.stop()
	conn, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = conn.Close() }()
	id, _, err := env.chat.StoreChatMessage(t.Context(), 2, "admin-uid", "admin", "ciphertext", 1, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, messageID := range []int64{id, 99999} {
		for _, request := range []struct {
			kind netproto.MessageType
			body any
		}{
			{netproto.MsgChatEdit, netproto.ChatEdit{MessageID: messageID, NewText: "edited"}},
			{netproto.MsgChatDelete, netproto.ChatDelete{MessageID: messageID}},
			{netproto.MsgChatReact, netproto.ChatReact{MessageID: messageID, Emoji: "ok"}},
		} {
			send(t, conn, request.kind, request.body)
			if got := readError(t, conn); got.Code != errCodeNotFound || got.Message != "message not found" {
				t.Fatalf("hidden/absent message responses differ: %+v", got)
			}
		}
	}
	message, err := env.chat.GetChatMessage(t.Context(), id)
	if err != nil || message.DeletedAt != nil || message.BodyEnc != "ciphertext" {
		t.Fatalf("denied mutation changed content: %+v %v", message, err)
	}
	send(t, conn, netproto.MsgChatPin, netproto.ChatPin{ChannelID: 1, MessageID: id, Pinned: true})
	if got := readError(t, conn); got.Code != errCodePermissionDenied {
		t.Fatalf("legacy admin pinned without ManageMessages: %+v", got)
	}
}
