package server

import (
	"context"
	"slices"
	"testing"

	"noxa/internal/authorization"
	"noxa/internal/state"
)

func customRoleMentionFixture(t *testing.T, change func(*authorization.RolePolicy)) (*TCPServer, *Client, *authorization.Authority) {
	t.Helper()
	backend := serverRoleFixture()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.SendMessages}
	backend.policy.Roles = append(backend.policy.Roles,
		authorization.Role{ID: 20, Name: "Raid team", Position: 1, Mentionable: true})
	for _, id := range []int64{1, 3, 4, 5, 6, 7, 8} {
		backend.policy.Members = append(backend.policy.Members, authorization.RoleMember{UserID: id, RoleIDs: []int64{20}})
	}
	backend.policy.Channels[0].Overrides = []authorization.RoleOverride{{UserID: 7, Capability: authorization.ViewChannel, Effect: authorization.Deny}}
	backend.policy.Channels = append(backend.policy.Channels,
		authorization.ChannelPolicy{ChannelID: 2},
		authorization.ChannelPolicy{ChannelID: 3, Overrides: []authorization.RoleOverride{{RoleID: 10, Capability: authorization.ViewChannel, Effect: authorization.Deny}}})
	if change != nil {
		change(&backend.policy)
	}
	a, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	sm := state.New(testLogger())
	for _, id := range []int64{1, 2, 3} {
		sm.AddChannel(testChannel(id))
	}
	for _, member := range []*state.Client{
		{ClientID: "sender", UserID: 1, UniqueID: "sender-uid"},
		{ClientID: "connected", UserID: 3, UniqueID: "connected-uid"},
		{ClientID: "connected-again", UserID: 3, UniqueID: "connected-uid"},
		{ClientID: "other-voice", UserID: 4, UniqueID: "other-voice-uid", ChannelID: 2},
		{ClientID: "invisible", UserID: 5, UniqueID: "invisible-uid", Status: "invisible"},
		{ClientID: "hidden-voice", UserID: 6, UniqueID: "hidden-voice-uid", ChannelID: 3},
		{ClientID: "cannot-read", UserID: 7, UniqueID: "cannot-read-uid", ChannelID: 2},
		{ClientID: "no-identity", UserID: 8},
		{ClientID: "non-member", UserID: 9, UniqueID: "non-member-uid"},
		{ClientID: "guest", UniqueID: "guest-uid"},
	} {
		sm.AddClient(member)
	}
	return &TCPServer{deps: &Deps{Authority: a, State: sm}}, &Client{ID: "sender", UserID: 1, UniqueID: "sender-uid"}, a
}

func assertCustomRoleMentions(t *testing.T, srv *TCPServer, sender *Client, channelID int64, body string, want []string) {
	t.Helper()
	if err := srv.withRolePolicy(t.Context(), func(ctx context.Context) error {
		got := srv.parseRoleMentions(ctx, sender, channelID, body)
		slices.Sort(got)
		want = slices.Clone(want)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("scope=%d body=%q mentions=%v, want %v", channelID, body, got, want)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCustomRoleMentionsResolveMembershipAndVisibility(t *testing.T) {
	srv, sender, _ := customRoleMentionFixture(t, nil)
	// Reading the chat does not require joining its voice channel. Repeated
	// tokens and multiple sessions for one identity must still notify once.
	assertCustomRoleMentions(t, srv, sender, 1, "Ready <@&20>? <@&20>", []string{"connected-uid", "other-voice-uid"})
	assertCustomRoleMentions(t, srv, sender, 0, "<@&20>", []string{"connected-uid", "other-voice-uid", "cannot-read-uid"})
}

func TestCustomRoleMentionsRequireExactStableTokens(t *testing.T) {
	srv, sender, _ := customRoleMentionFixture(t, nil)
	for _, body := range []string{
		"@Raid team", "@member", "<@&2>", "<@&200>", "<@&20", "@&20>",
		"<@&20suffix>", "<@&20.0>", "<@&-20>", "<@&+20>", "<@& 20>",
		"<@&0>", "<@&9223372036854775808>", "<@&999>", "<@&10>",
	} {
		t.Run(body, func(t *testing.T) {
			assertCustomRoleMentions(t, srv, sender, 1, body, nil)
		})
	}
}

func TestCustomRoleMentionsRespectMentionabilityAndScopedMassGrant(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		mentionable, mass, denied bool
		want                      bool
	}{
		{name: "mentionable role", mentionable: true, want: true},
		{name: "restricted role"},
		{name: "mass grant overrides restriction", mass: true, want: true},
		{name: "channel denial defeats server mass grant", mass: true, denied: true},
		{name: "mentionable independent of mass grant", mentionable: true, mass: true, denied: true, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, sender, _ := customRoleMentionFixture(t, func(p *authorization.RolePolicy) {
				p.Roles[1].Mentionable = tc.mentionable
				if tc.mass {
					p.Roles[0].Permissions = append(p.Roles[0].Permissions, authorization.MentionEveryone)
				}
				if tc.denied {
					p.Channels[0].Overrides = append(p.Channels[0].Overrides, authorization.RoleOverride{RoleID: 10, Capability: authorization.MentionEveryone, Effect: authorization.Deny})
				}
			})
			var want []string
			if tc.want {
				want = []string{"connected-uid", "other-voice-uid"}
			}
			assertCustomRoleMentions(t, srv, sender, 1, "<@&20>", want)
		})
	}
}

func TestCustomRoleMentionsRespectInvisibleVisibilityGrant(t *testing.T) {
	srv, sender, _ := customRoleMentionFixture(t, func(p *authorization.RolePolicy) {
		p.Roles[0].Permissions = append(p.Roles[0].Permissions, authorization.ViewConnectionInfo)
	})
	assertCustomRoleMentions(t, srv, sender, 1, "<@&20>", []string{"connected-uid", "other-voice-uid", "invisible-uid"})
}

func TestCustomRoleMentionsObserveMembershipRevocationAndDeletion(t *testing.T) {
	srv, sender, authority := customRoleMentionFixture(t, nil)
	assertCustomRoleMentions(t, srv, sender, 1, "<@&20>", []string{"connected-uid", "other-voice-uid"})
	if _, err := authority.ChangeRolePolicy(t.Context(), 2, authorization.RoleChange{
		Kind: authorization.MemberRolesSet, ExpectedRevision: 1, UserID: 3,
	}); err != nil {
		t.Fatal(err)
	}
	assertCustomRoleMentions(t, srv, sender, 1, "<@&20>", []string{"other-voice-uid"})
	if _, err := authority.ChangeRolePolicy(t.Context(), 2, authorization.RoleChange{
		Kind: authorization.RoleDelete, ExpectedRevision: 2, RoleID: 20,
	}); err != nil {
		t.Fatal(err)
	}
	assertCustomRoleMentions(t, srv, sender, 1, "<@&20>", nil)
}

func TestCustomRoleMentionsFailClosedWithoutLease(t *testing.T) {
	srv, sender, _ := customRoleMentionFixture(t, nil)
	if got := srv.parseRoleMentions(t.Context(), sender, 1, "<@&20>"); len(got) != 0 {
		t.Fatalf("role mentions without an authority lease: %v", got)
	}
}
