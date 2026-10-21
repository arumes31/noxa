package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"sync"
	"testing"
)

type memoryChannelInvites struct {
	*fakeChat
	mu     sync.Mutex
	hash   []byte
	access string
}

func (m *memoryChannelInvites) ChannelInvite(_ context.Context, r netproto.ChannelInviteRequest, hash []byte) (netproto.ChannelInviteResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := netproto.ChannelInviteResult{Action: r.Action, ChannelID: r.ChannelID}
	switch r.Action {
	case "regenerate":
		m.hash = append([]byte(nil), hash...)
		m.access = r.Access
	case "revoke":
		m.hash = nil
	case "resolve":
		if len(m.hash) == 0 || subtle.ConstantTimeCompare(hash, m.hash) != 1 {
			return out, errors.New("invalid")
		}
	}
	out.Active = len(m.hash) > 0
	out.Access = m.access
	return out, nil
}
func TestChannelInviteRotationAndPermissionEnforcement(t *testing.T) {
	roles := serverRoleFixture()
	roles.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.Connect}
	authority, err := authorization.NewAuthority(t.Context(), roles, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	invites := &memoryChannelInvites{fakeChat: newFakeChat()}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority; d.Chat = invites })
	defer env.stop()
	owner, _ := dialAuthed(t, env.addr, "user-uid")
	defer owner.Close()
	other, otherID := dialAuthed(t, env.addr, "admin-uid")
	defer other.Close()
	send(t, other, netproto.MsgChannelInviteRequest, netproto.ChannelInviteRequest{Action: "regenerate", ChannelID: 1, Access: "guest"})
	if got := readError(t, other); got.Code != errCodePermissionDenied {
		t.Fatal(got)
	}
	create := func() string {
		send(t, owner, netproto.MsgChannelInviteRequest, netproto.ChannelInviteRequest{Action: "regenerate", ChannelID: 1, Access: "guest"})
		var out netproto.ChannelInviteResult
		if err := netproto.Decode(readOfType(t, owner, netproto.MsgChannelInviteResult), &out); err != nil {
			t.Fatal(err)
		}
		if !netproto.ValidInviteToken(out.Token) {
			t.Fatal("missing secret")
		}
		return out.Token
	}
	first := create()
	sum := sha256.Sum256([]byte(first))
	invites.mu.Lock()
	if subtle.ConstantTimeCompare(invites.hash, sum[:]) != 1 {
		t.Error("store did not receive digest")
	}
	invites.mu.Unlock()
	send(t, owner, netproto.MsgChannelInviteRequest, netproto.ChannelInviteRequest{Action: "get", ChannelID: 1})
	var listed netproto.ChannelInviteResult
	if err := netproto.Decode(readOfType(t, owner, netproto.MsgChannelInviteResult), &listed); err != nil {
		t.Fatal(err)
	}
	if listed.Token != "" {
		t.Fatal("get disclosed secret")
	}
	send(t, owner, netproto.MsgChannelInviteRequest, netproto.ChannelInviteRequest{Action: "regenerate", ChannelID: 1, Access: "member"})
	var memberInvite netproto.ChannelInviteResult
	if err := netproto.Decode(readOfType(t, owner, netproto.MsgChannelInviteResult), &memberInvite); err != nil {
		t.Fatal(err)
	}
	guest, _ := env.srv.clientByID(otherID)
	guest.mu.Lock()
	originalID := guest.UserID
	guest.UserID = 0
	guest.mu.Unlock()
	send(t, other, netproto.MsgChannelInviteRequest, netproto.ChannelInviteRequest{Action: "resolve", ChannelID: 1, Token: memberInvite.Token})
	if got := readError(t, other); got.Code != errCodePermissionDenied {
		t.Fatal("guest redeemed member-only link", got)
	}
	guest.mu.Lock()
	guest.UserID = originalID
	guest.mu.Unlock()
	second := create()
	if first == second {
		t.Fatal("token not rotated")
	}
	send(t, other, netproto.MsgChannelInviteRequest, netproto.ChannelInviteRequest{Action: "resolve", ChannelID: 1, Token: first})
	readError(t, other)
	send(t, other, netproto.MsgChannelInviteRequest, netproto.ChannelInviteRequest{Action: "resolve", ChannelID: 1, Token: second})
	readOfType(t, other, netproto.MsgChannelInviteResult)
	send(t, other, netproto.MsgChannelInviteRequest, netproto.ChannelInviteRequest{Action: "resolve", ChannelID: 999, Token: second})
	if got := readError(t, other); got.Code != errCodePermissionDenied {
		t.Fatal(got)
	}
	send(t, owner, netproto.MsgChannelInviteRequest, netproto.ChannelInviteRequest{Action: "revoke", ChannelID: 1})
	readOfType(t, owner, netproto.MsgChannelInviteResult)
	send(t, other, netproto.MsgChannelInviteRequest, netproto.ChannelInviteRequest{Action: "resolve", ChannelID: 1, Token: second})
	readError(t, other)
}
