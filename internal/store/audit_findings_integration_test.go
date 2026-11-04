//go:build integration

package store

import (
	"errors"
	"testing"

	"noxa/internal/authorization"
)

// A nonexistent account in a role change is invalid input, so Authority keeps
// serving the current evaluator instead of treating it as an ambiguous commit.
func TestRoleChangeRejectsMissingAccountsAsInvalid(t *testing.T) {
	s, owner, member := roleTestStore(t)
	p, err := s.PrepareRolePolicy(t.Context(), owner)
	if err != nil {
		t.Fatal(err)
	}
	p, err = s.ChangeRolePolicy(t.Context(), owner, authorization.RoleChange{Kind: authorization.RoleCreate, ExpectedRevision: p.Revision,
		Role: authorization.Role{Name: "Helper"}})
	if err != nil {
		t.Fatal(err)
	}
	var roleID int64
	for _, r := range p.Roles {
		if r.Name == "Helper" {
			roleID = r.ID
		}
	}
	const missing = int64(1 << 40)
	_, err = s.ChangeRolePolicy(t.Context(), owner, authorization.RoleChange{Kind: authorization.MemberRolesSet, ExpectedRevision: p.Revision,
		UserID: missing, RoleIDs: []int64{roleID}})
	if !errors.Is(err, authorization.ErrRoleInvalid) {
		t.Fatalf("missing member: err = %v, want ErrRoleInvalid", err)
	}
	channel := p.Channels[0]
	channel.Overrides = append(channel.Overrides, authorization.RoleOverride{UserID: missing, Capability: authorization.ViewChannel, Effect: authorization.Allow})
	_, err = s.ChangeRolePolicy(t.Context(), owner, authorization.RoleChange{Kind: authorization.ChannelAccessSet, ExpectedRevision: p.Revision, Channel: channel})
	if !errors.Is(err, authorization.ErrRoleInvalid) {
		t.Fatalf("missing override subject: err = %v, want ErrRoleInvalid", err)
	}
	if _, err := s.ChangeRolePolicy(t.Context(), owner, authorization.RoleChange{Kind: authorization.MemberRolesSet, ExpectedRevision: p.Revision,
		UserID: member, RoleIDs: []int64{roleID}}); err != nil {
		t.Fatalf("existing member: %v", err)
	}
}
