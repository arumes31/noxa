package authorization

import (
	"errors"
	"slices"
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

// An override on ViewChannel can make an existing ReadHistory grant effective;
// the manager must hold every capability that becomes effective.
func TestMemberRolesSetChecksDependentChannelGains(t *testing.T) {
	p := roleFixture()
	p.Roles[0].Permissions = append(p.Roles[0].Permissions, ReadHistory) // @everyone
	p.Roles[1].Permissions = nil
	p.Members = []RoleMember{{UserID: 2, RoleIDs: []int64{30}}, {UserID: 5, RoleIDs: []int64{}}}
	p.Channels = append(p.Channels, ChannelPolicy{ChannelID: 103, Overrides: []RoleOverride{
		{RoleID: 10, Capability: ViewChannel, Effect: Deny},
		{RoleID: 20, Capability: ViewChannel, Effect: Allow},
		{RoleID: 30, Capability: ViewChannel, Effect: Allow},
		{RoleID: 30, Capability: ReadHistory, Effect: Deny},
	}})
	_, err := ApplyRoleChange(p, 2, RoleChange{Kind: MemberRolesSet, UserID: 5, RoleIDs: []int64{20}, ExpectedRevision: p.Revision})
	if !errors.Is(err, ErrRoleForbidden) {
		t.Fatalf("dependent ReadHistory gain: err = %v, want ErrRoleForbidden", err)
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

// Deleting a lower role removes its denying channel overrides; that gain is
// checked like removing the role from each of its members.
func TestRoleDeleteChecksChannelOverrideGains(t *testing.T) {
	p := roleFixture()
	p.Channels = append(p.Channels, ChannelPolicy{ChannelID: 103, Overrides: []RoleOverride{
		{RoleID: 20, Capability: ViewChannel, Effect: Deny},
	}})
	change := RoleChange{Kind: RoleDelete, RoleID: 20, ExpectedRevision: p.Revision}
	if _, err := ApplyRoleChange(p, 2, change); !errors.Is(err, ErrRoleForbidden) {
		t.Fatalf("deleting own denying role: err = %v, want ErrRoleForbidden", err)
	}
	if _, err := ApplyRoleChange(p, 1, change); err != nil {
		t.Fatalf("owner deletion: %v", err)
	}
	// Without a resulting gain the manager may still delete the role.
	if _, err := ApplyRoleChange(roleFixture(), 2, change); err != nil {
		t.Fatalf("deletion without channel gain: %v", err)
	}
}

// A channel access edit can make dependent grants effective for others (an
// allowed ViewChannel unlocks an existing ReadHistory or Connect); the editor
// must hold every capability that becomes effective.
func TestChannelAccessChecksDependentGains(t *testing.T) {
	denied := []RoleOverride{{RoleID: 30, Capability: ReadHistory, Effect: Deny}, {RoleID: 30, Capability: Connect, Effect: Deny}}
	base := []RoleOverride{
		{RoleID: 10, Capability: ViewChannel, Effect: Deny},
		{RoleID: 20, Capability: ViewChannel, Effect: Allow},
		{RoleID: 30, Capability: ViewChannel, Effect: Allow},
	}
	// User 2 manages access in private channel 103; when editorDenied, its
	// role is denied ReadHistory and Connect there.
	fixture := func(editorDenied bool) RolePolicy {
		overrides := slices.Clone(base)
		if editorDenied {
			overrides = append(overrides, denied...)
		}
		return RolePolicy{
			Revision: 1, OwnerID: 1, EveryoneID: 10,
			Roles: []Role{
				{ID: 10, Name: "@everyone", Position: 0, Permissions: []Capability{ViewChannel, ReadHistory, Connect, Speak}},
				{ID: 20, Name: "Member", Position: 1},
				{ID: 30, Name: "ChannelMod", Position: 2, Permissions: []Capability{ManageChannels, ManageChannelAccess}},
			},
			Members:  []RoleMember{{UserID: 2, RoleIDs: []int64{30}}},
			Channels: []ChannelPolicy{{ChannelID: 103, Overrides: overrides}},
		}
	}
	set := func(p RolePolicy, actor int64, overrides []RoleOverride) error {
		_, err := ApplyRoleChange(p, actor, RoleChange{Kind: ChannelAccessSet, ExpectedRevision: 1, Channel: ChannelPolicy{ChannelID: 103, Overrides: overrides}})
		return err
	}
	open := append(slices.Clone(base[1:]), denied...) // without the @everyone ViewChannel deny
	if err := set(fixture(true), 2, open); !errors.Is(err, ErrRoleForbidden) {
		t.Fatalf("opening the channel to everyone: err = %v, want ErrRoleForbidden", err)
	}
	member := append(append(slices.Clone(base), denied...), RoleOverride{UserID: 5, Capability: ViewChannel, Effect: Allow})
	if err := set(fixture(true), 2, member); !errors.Is(err, ErrRoleForbidden) {
		t.Fatalf("admitting one member: err = %v, want ErrRoleForbidden", err)
	}
	if err := set(fixture(true), 1, open); err != nil {
		t.Fatalf("owner: %v", err)
	}
	// Members with the same roles gain alike, except one an override names.
	sameRoles := fixture(true)
	sameRoles.Members = append(sameRoles.Members, RoleMember{UserID: 6, RoleIDs: []int64{40}}, RoleMember{UserID: 7, RoleIDs: []int64{40}})
	sameRoles.Roles = append(sameRoles.Roles[:2:2], Role{ID: 40, Name: "Team", Position: 2}, Role{ID: 30, Name: "ChannelMod", Position: 3, Permissions: sameRoles.Roles[2].Permissions})
	named := append(slices.Clone(sameRoles.Channels[0].Overrides), RoleOverride{UserID: 6, Capability: ViewChannel, Effect: Deny})
	sameRoles.Channels[0].Overrides = named
	for range 20 { // whichever member the evaluation picks for the role set
		if err := set(sameRoles, 2, append(slices.Clone(named), RoleOverride{RoleID: 40, Capability: ViewChannel, Effect: Allow})); !errors.Is(err, ErrRoleForbidden) {
			t.Fatalf("admitting a role whose members gain more: err = %v, want ErrRoleForbidden", err)
		}
	}
	if err := set(fixture(false), 2, slices.Clone(base[1:])); err != nil {
		t.Fatalf("editor holding every gained capability: %v", err)
	}
	// Restricting access gives nobody anything.
	if err := set(fixture(true), 2, append(append(slices.Clone(base), denied...), RoleOverride{RoleID: 20, Capability: Speak, Effect: Deny})); !errors.Is(err, ErrRoleForbidden) {
		// Speak depends on Connect, which the editor is denied here.
		t.Fatalf("override on a capability the editor lacks: err = %v, want ErrRoleForbidden", err)
	}
	if err := set(fixture(true), 2, append(slices.Clone(base[:1]), append(slices.Clone(base[2:]), denied...)...)); err != nil {
		t.Fatalf("removing a role's ViewChannel allow: %v", err)
	}

	// A new channel with custom access is compared with its parent the same way.
	create := func(p RolePolicy, overrides []RoleOverride) error {
		_, err := ApplyChannelTreeChange(p, 2, ChannelTreeChange{Kind: ChannelCreate, ExpectedRevision: 1, ChannelID: 104, ParentID: 103,
			Access: ChannelPolicy{ChannelID: 104, ParentID: 103, Overrides: overrides}})
		return err
	}
	if err := create(fixture(true), open); !errors.Is(err, ErrRoleForbidden) {
		t.Fatalf("creating an open child channel: err = %v, want ErrRoleForbidden", err)
	}
	if err := create(fixture(false), slices.Clone(base[1:])); err != nil {
		t.Fatalf("creating an open child channel while holding every gained capability: %v", err)
	}
}
