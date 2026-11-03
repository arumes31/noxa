package authorization

import (
	"errors"
	"testing"
)

// A manager must not delegate private-channel access through a role's channel
// overrides when the manager cannot use that access.
func TestMemberRolesSetChecksChannelOverrideGains(t *testing.T) {
	fixture := func(managerSeesChannel bool) RolePolicy {
		p := roleFixture()
		p.Roles[1].Permissions = nil // Member: grants only through channel overrides
		p.Members = []RoleMember{{UserID: 2, RoleIDs: []int64{30}}, {UserID: 5, RoleIDs: []int64{}}}
		overrides := []RoleOverride{
			{RoleID: 10, Capability: ViewChannel, Effect: Deny},
			{RoleID: 20, Capability: ViewChannel, Effect: Allow},
		}
		if managerSeesChannel {
			overrides = append(overrides, RoleOverride{RoleID: 30, Capability: ViewChannel, Effect: Allow})
		}
		p.Channels = append(p.Channels, ChannelPolicy{ChannelID: 103, Overrides: overrides})
		return p
	}
	change := RoleChange{Kind: MemberRolesSet, UserID: 5, RoleIDs: []int64{20}, ExpectedRevision: 1}
	if _, err := ApplyRoleChange(fixture(false), 2, change); !errors.Is(err, ErrRoleForbidden) {
		t.Fatalf("delegating hidden channel access: err = %v, want ErrRoleForbidden", err)
	}
	if _, err := ApplyRoleChange(fixture(true), 2, change); err != nil {
		t.Fatalf("delegating access the manager holds: %v", err)
	}
	if _, err := ApplyRoleChange(fixture(false), 1, change); err != nil {
		t.Fatalf("owner assignment: %v", err)
	}

	// Removing a denying role is a gain as well.
	p := fixture(false)
	p.Members[1].RoleIDs = []int64{20}
	p.Channels[len(p.Channels)-1].Overrides = []RoleOverride{{RoleID: 20, Capability: ViewChannel, Effect: Deny}, {RoleID: 30, Capability: ViewChannel, Effect: Deny}}
	if _, err := ApplyRoleChange(p, 2, RoleChange{Kind: MemberRolesSet, UserID: 5, RoleIDs: []int64{}, ExpectedRevision: 1}); !errors.Is(err, ErrRoleForbidden) {
		t.Fatalf("removing denying role: err = %v, want ErrRoleForbidden", err)
	}
}

func TestMemberRolesSetRejectsOversizedAndDuplicateLists(t *testing.T) {
	p := roleFixture()
	for name, ids := range map[string][]int64{
		"duplicate": {20, 20},
		"oversized": make([]int64, len(p.Roles)+1),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ApplyRoleChange(p, 1, RoleChange{Kind: MemberRolesSet, UserID: 3, RoleIDs: ids, ExpectedRevision: p.Revision})
			if !errors.Is(err, ErrRoleInvalid) {
				t.Fatalf("err = %v, want ErrRoleInvalid", err)
			}
		})
	}
}

func TestChannelAncestryValidationIsLinear(t *testing.T) {
	p := roleFixture()
	p.Channels = nil
	const depth = 20000
	for id := int64(1); id <= depth; id++ {
		p.Channels = append(p.Channels, ChannelPolicy{ChannelID: id, ParentID: id - 1})
	}
	if _, err := NewRoleEvaluator(p); err != nil {
		t.Fatalf("deep valid chain: %v", err)
	}
	p.Channels[0].ParentID = depth // close a cycle
	if _, err := NewRoleEvaluator(p); err == nil {
		t.Fatal("cycle accepted")
	}
	p.Channels[0].ParentID = depth + 1 // unknown parent
	if _, err := NewRoleEvaluator(p); err == nil {
		t.Fatal("unknown parent accepted")
	}
}
