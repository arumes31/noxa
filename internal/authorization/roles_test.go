package authorization

import (
	"slices"
	"testing"
)

func roleFixture() RolePolicy {
	return RolePolicy{
		Revision: 1, OwnerID: 1, EveryoneID: 10,
		Roles: []Role{
			{ID: 10, Name: "@everyone", Position: 0, Permissions: []Capability{ViewChannel, Connect, Speak}},
			{ID: 20, Name: "Member", Position: 1, Permissions: []Capability{SendMessages}},
			{ID: 30, Name: "Moderator", Position: 2, Permissions: []Capability{ManageRoles, KickMembers}},
			{ID: 40, Name: "Admin", Position: 3, Permissions: []Capability{Administrator}},
		},
		Members: []RoleMember{{UserID: 2, RoleIDs: []int64{20, 30}}, {UserID: 3, RoleIDs: []int64{20}}, {UserID: 4, RoleIDs: []int64{40}}},
		Channels: []ChannelPolicy{
			{ChannelID: 100, Overrides: []RoleOverride{
				{RoleID: 10, Capability: ViewChannel, Effect: Deny},
				{RoleID: 20, Capability: ViewChannel, Effect: Deny},
				{RoleID: 30, Capability: ViewChannel, Effect: Allow},
			}},
			{ChannelID: 101, ParentID: 100, Synced: true},
			{ChannelID: 102, ParentID: 100},
		},
	}
}

func TestRoleResolution(t *testing.T) {
	e, err := NewRoleEvaluator(roleFixture())
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name          string
		user, channel int64
		cap           Capability
		want          bool
	}{
		{"role allow wins over another role deny", 2, 100, ViewChannel, true},
		{"member denied private channel", 3, 100, ViewChannel, false},
		{"hidden channel also blocks speak", 3, 100, Speak, false},
		{"synced child uses parent policy", 3, 101, ViewChannel, false},
		{"custom child uses own policy", 3, 102, ViewChannel, true},
		{"administrator bypasses channel override", 4, 100, Speak, true},
		{"owner bypasses channel override", 1, 100, ViewChannel, true},
		{"unknown capability denied even for owner", 1, 100, "unknown", false},
		{"server capability rejects channel scope even for owner", 1, 100, ManageRoles, false},
		{"missing channel denied even for owner", 1, 999, ViewChannel, false},
		{"guest only has everyone", 0, 102, SendMessages, false},
		{"server grants compose", 2, 0, ManageRoles, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d := e.Evaluate(tt.user, tt.channel, tt.cap)
			if d.Allowed != tt.want || d.Revision != 1 || d.Reason == "" {
				t.Fatalf("decision = %+v, want allowed %v", d, tt.want)
			}
		})
	}
}

func TestChannelManagementRequiresVisibleScope(t *testing.T) {
	p := roleFixture()
	for _, capability := range []Capability{ManageChannels, ManageChannelAccess, CreateTemporaryChannels} {
		p.Roles[1].Permissions = []Capability{capability}
		e, err := NewRoleEvaluator(p)
		if err != nil {
			t.Fatal(err)
		}
		if e.Evaluate(3, 100, capability).Allowed || e.Evaluate(3, 101, capability).Allowed {
			t.Fatalf("%s bypassed hidden channel prerequisite", capability)
		}
		if !e.Evaluate(3, 102, capability).Allowed {
			t.Fatalf("visible channel lost %s", capability)
		}
	}
}

func TestRoleMemberOverrideAndOrderIndependence(t *testing.T) {
	p := roleFixture()
	p.Channels[0].Overrides = append(p.Channels[0].Overrides, RoleOverride{UserID: 2, Capability: ViewChannel, Effect: Deny})
	for range 2 {
		e, err := NewRoleEvaluator(p)
		if err != nil {
			t.Fatal(err)
		}
		if d := e.Evaluate(2, 101, ViewChannel); d.Allowed || d.Reason != "member_override" {
			t.Fatalf("decision = %+v", d)
		}
		slices.Reverse(p.Roles)
		slices.Reverse(p.Members[0].RoleIDs)
	}
}

func TestRolePolicyValidation(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*RolePolicy)
	}{
		{"missing owner", func(p *RolePolicy) { p.OwnerID = 0 }},
		{"duplicate rank", func(p *RolePolicy) { p.Roles[1].Position = 0 }},
		{"unknown role", func(p *RolePolicy) { p.Members[0].RoleIDs = []int64{999} }},
		{"parent cycle", func(p *RolePolicy) { p.Channels[0].ParentID = 101 }},
		{"unknown parent", func(p *RolePolicy) { p.Channels[0].ParentID = 999 }},
		{"server capability override", func(p *RolePolicy) { p.Channels[0].Overrides[0].Capability = Administrator }},
		{"duplicate override", func(p *RolePolicy) {
			p.Channels[0].Overrides = append(p.Channels[0].Overrides, p.Channels[0].Overrides[0])
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := roleFixture()
			tt.change(&p)
			if _, err := NewRoleEvaluator(p); err == nil {
				t.Fatal("invalid policy accepted")
			}
		})
	}
}

func TestRoleHierarchyAndSnapshotIsolation(t *testing.T) {
	p := roleFixture()
	e, err := NewRoleEvaluator(p)
	if err != nil {
		t.Fatal(err)
	}
	if !e.CanManageMember(2, 3) || e.CanManageMember(2, 2) || e.CanManageMember(2, 1) || e.CanManageMember(2, 4) {
		t.Fatal("member hierarchy violated")
	}
	if !e.CanManageRole(2, 20) || e.CanManageRole(2, 30) || e.CanManageRole(2, 40) {
		t.Fatal("role hierarchy violated")
	}
	p.Roles[2].Permissions[0] = Administrator
	p.Members[0].RoleIDs[0] = 40
	if e.Evaluate(2, 0, Administrator).Allowed {
		t.Fatal("caller mutated evaluator snapshot")
	}
}
