package server

import (
	"context"
	"errors"
	"testing"

	"noxa/internal/authorization"
	"noxa/internal/config"
	"noxa/internal/netproto"
)

func TestRoleChatRevocationRotatesBeforeAcknowledgement(t *testing.T) {
	backend := serverRoleFixture()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.ReadHistory, authorization.SendMessages}
	var env *testEnv
	authority, err := authorization.NewAuthority(t.Context(), backend, func(ctx context.Context, before, after *authorization.RoleEvaluator) error {
		return env.srv.reconcileRoleChat(ctx, before, after)
	})
	if err != nil {
		t.Fatal(err)
	}
	env = startTestEnvDeps(t, nil, func(c *config.Config) { c.ChatKeyRotateMinSecs = 3600 }, func(d *Deps) { d.Authority = authority; d.Roles = backend })
	defer env.stop()
	env.state.AddChannel(testChannel(1))
	pub, _ := testX25519(t)
	reader, readerInfo := dialSubscriptionClient(t, env.addr, "admin-uid", pub)
	defer func() { _ = reader.Close() }()
	send(t, reader, netproto.MsgChannelSubscribe, netproto.ChannelSubscribe{Subscribe: true, ChannelIDs: []int64{1}})
	reply, keys, errs := readSubscriptionReply(t, reader)
	if len(reply.ChannelIDs) != 1 || len(keys) != 1 || len(errs) != 0 {
		t.Fatalf("subscribe: %+v %+v %+v", reply, keys, errs)
	}
	oldGeneration := keys[0].KeyID
	owner, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = owner.Close() }()
	send(t, owner, netproto.MsgRoleChange, authorization.RoleChange{Kind: authorization.ChannelAccessSet, ExpectedRevision: 1, Channel: authorization.ChannelPolicy{ChannelID: 1, Overrides: []authorization.RoleOverride{{RoleID: 10, Capability: authorization.ViewChannel, Effect: authorization.Deny}}}})
	var ack netproto.RoleChangeResult
	if err := netproto.Decode(readOfType(t, owner, netproto.MsgRoleChangeResult), &ack); err != nil {
		t.Fatal(err)
	}
	if ack.EnforcementPending || ack.Revision != 2 {
		t.Fatalf("unexpected ack: %+v", ack)
	}
	if env.state.IsSubscribed(readerInfo.ClientID, 1) {
		t.Fatal("revoked subscription survived acknowledgement")
	}
	newGeneration, _, err := env.srv.chatKeys.current(t.Context(), 1)
	if err != nil || newGeneration == oldGeneration {
		t.Fatalf("revoked generation was not replaced: %d %v", newGeneration, err)
	}
	send(t, reader, netproto.MsgChatKeyRequest, netproto.ChatKeyRequest{ChannelID: 1, KeyIDs: []uint32{oldGeneration, newGeneration}})
	if got := readError(t, reader); got.Code != errCodePermissionDenied {
		t.Fatalf("revoked reader got keys: %+v", got)
	}
	send(t, reader, netproto.MsgChatSend, netproto.ChatSend{ChannelID: "1", Text: "after revocation"})
	if got := readError(t, reader); got.Code != errCodePermissionDenied {
		t.Fatalf("revoked reader sent content: %+v", got)
	}
}

type failingRoleRotationStore struct{ ScopeKeyStore }

func (s failingRoleRotationStore) RotateScopeKey(context.Context, int64, uint32, []byte, uint16) error {
	return errors.New("rotation unavailable")
}

func TestRoleChatRotationFailureClosesAccess(t *testing.T) {
	backend := serverRoleFixture()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel}
	var env *testEnv
	authority, err := authorization.NewAuthority(t.Context(), backend, func(ctx context.Context, before, after *authorization.RoleEvaluator) error {
		return env.srv.reconcileRoleChat(ctx, before, after)
	})
	if err != nil {
		t.Fatal(err)
	}
	env = startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority; d.ScopeKeys = failingRoleRotationStore{d.ScopeKeys} })
	defer env.stop()
	// Global chat already has a persisted generation. Removing its grant
	// requires rotation even if no affected identity is online.
	p, err := authority.ChangeRolePolicy(t.Context(), 2, authorization.RoleChange{Kind: authorization.RoleUpdate, ExpectedRevision: 1, Role: authorization.Role{ID: 10, Name: "@everyone"}})
	if p.Revision != 2 || !errors.Is(err, authorization.ErrEnforcementPending) {
		t.Fatalf("commit status: %d %v", p.Revision, err)
	}
	if err := authority.WithAccess(t.Context(), 2, 0, authorization.ViewChannel, func(*authorization.RoleEvaluator) error { t.Fatal("owner bypassed failed rotation"); return nil }); !errors.Is(err, authorization.ErrAuthorizationUnavailable) {
		t.Fatal(err)
	}
}
