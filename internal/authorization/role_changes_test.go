package authorization

import (
	"errors"
	"reflect"
	"testing"
)

func TestRoleChangesUsePreChangeAuthority(t *testing.T) {
	for _, tt := range []struct {
		name   string
		actor  int64
		change RoleChange
		want   error
	}{
		{"owner creates admin", 1, RoleChange{Kind: RoleCreate, Role: Role{ID: 50, Name: "Admin 2", Permissions: []Capability{Administrator}}}, nil},
		{"manager cannot create admin", 2, RoleChange{Kind: RoleCreate, Role: Role{ID: 50, Name: "Admin 2", Permissions: []Capability{Administrator}}}, ErrRoleForbidden},
		{"admin cannot delegate admin", 4, RoleChange{Kind: MemberRolesSet, UserID: 3, RoleIDs: []int64{40}}, ErrRoleForbidden},
		{"manager cannot grant absent capability", 2, RoleChange{Kind: RoleCreate, Role: Role{ID: 50, Name: "Ban", Permissions: []Capability{BanMembers}}}, ErrRoleForbidden},
		{"manager can create lower role", 2, RoleChange{Kind: RoleCreate, Role: Role{ID: 50, Name: "Helper", Permissions: []Capability{KickMembers}}}, nil},
		{"manager cannot edit own highest role", 2, RoleChange{Kind: RoleUpdate, Role: Role{ID: 30, Name: "Manager", Position: 2}}, ErrRoleForbidden},
		{"cannot assign own role", 2, RoleChange{Kind: MemberRolesSet, UserID: 3, RoleIDs: []int64{30}}, ErrRoleForbidden},
		{"cannot edit own memberships", 2, RoleChange{Kind: MemberRolesSet, UserID: 2, RoleIDs: []int64{20}}, ErrRoleForbidden},
		{"owner may assign admin", 1, RoleChange{Kind: MemberRolesSet, UserID: 3, RoleIDs: []int64{40}}, nil},
		{"everyone is permanent", 1, RoleChange{Kind: RoleDelete, RoleID: 10}, ErrRoleInvalid},
		{"guest cannot change policy", 0, RoleChange{Kind: RoleDelete, RoleID: 20}, ErrRoleForbidden},
		{"manager cannot reorder above admin", 2, RoleChange{Kind: RolesReorder, RoleIDs: []int64{10, 40, 20, 30}}, ErrRoleForbidden},
		{"owner can reorder", 1, RoleChange{Kind: RolesReorder, RoleIDs: []int64{10, 40, 20, 30}}, nil},
		{"duplicate reorder rejected", 1, RoleChange{Kind: RolesReorder, RoleIDs: []int64{10, 20, 20, 30}}, ErrRoleInvalid},
		{"unknown operation rejected", 1, RoleChange{Kind: "unknown"}, ErrRoleInvalid},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := roleFixture()
			before := cloneRolePolicy(p)
			tt.change.ExpectedRevision = p.Revision
			got, err := ApplyRoleChange(p, tt.actor, tt.change)
			if !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
			if !reflect.DeepEqual(p, before) {
				t.Fatal("mutated source snapshot")
			}
			if err == nil {
				if got.Revision != p.Revision+1 {
					t.Fatal("revision did not advance")
				}
				if _, err := NewRoleEvaluator(got); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestRoleChangeConflictAndCleanup(t *testing.T) {
	p := roleFixture()
	if _, err := ApplyRoleChange(p, 1, RoleChange{Kind: RoleDelete, RoleID: 20, ExpectedRevision: 2}); !errors.Is(err, ErrRoleConflict) {
		t.Fatal(err)
	}
	p.DefaultMemberRoleID = 20
	next, err := ApplyRoleChange(p, 1, RoleChange{Kind: RoleDelete, RoleID: 20, ExpectedRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	if next.DefaultMemberRoleID != 0 {
		t.Fatal("dangling default role")
	}
	e, err := NewRoleEvaluator(next)
	if err != nil {
		t.Fatal(err)
	}
	if e.Evaluate(3, 102, SendMessages).Allowed {
		t.Fatal("deleted role still grants")
	}
}

func TestOwnershipTransfer(t *testing.T) {
	p := roleFixture()
	for _, actor := range []int64{0, 2, 4} {
		if _, err := ApplyRoleChange(p, actor, RoleChange{Kind: "owner_transfer", UserID: 3, ExpectedRevision: p.Revision}); !errors.Is(err, ErrRoleForbidden) {
			t.Fatalf("actor %d transferred ownership: %v", actor, err)
		}
	}
	for _, target := range []int64{0, -1, p.OwnerID} {
		if _, err := ApplyRoleChange(p, p.OwnerID, RoleChange{Kind: "owner_transfer", UserID: target, ExpectedRevision: p.Revision}); !errors.Is(err, ErrRoleInvalid) {
			t.Fatalf("invalid target %d accepted: %v", target, err)
		}
	}
	next, err := ApplyRoleChange(p, p.OwnerID, RoleChange{Kind: "owner_transfer", UserID: 3, ExpectedRevision: p.Revision})
	if err != nil {
		t.Fatal(err)
	}
	e, err := NewRoleEvaluator(next)
	if err != nil {
		t.Fatal(err)
	}
	if next.OwnerID != 3 || next.Revision != p.Revision+1 || p.OwnerID != 1 {
		t.Fatal("transfer did not produce an independent committed revision")
	}
	if !e.Evaluate(3, 0, Administrator).Allowed || e.Evaluate(1, 0, ManageRoles).Allowed || e.CanManageMember(4, 3) {
		t.Fatal("ownership protection did not follow the new owner")
	}
	if _, err := ApplyRoleChange(next, p.OwnerID, RoleChange{Kind: "owner_transfer", UserID: 1, ExpectedRevision: next.Revision}); !errors.Is(err, ErrRoleForbidden) {
		t.Fatalf("former owner retained transfer authority: %v", err)
	}
}

func TestRoleChannelChangeHierarchyAndSync(t *testing.T) {
	p := roleFixture()
	p.Roles[2].Permissions = append(p.Roles[2].Permissions, ManageChannelAccess)
	// A manager cannot rewrite an override on a higher role.
	ch := p.Channels[2]
	ch.Overrides = []RoleOverride{{RoleID: 40, Capability: ViewChannel, Effect: Deny}}
	if _, err := ApplyRoleChange(p, 2, RoleChange{Kind: ChannelAccessSet, Channel: ch, ExpectedRevision: 1}); !errors.Is(err, ErrRoleForbidden) {
		t.Fatal(err)
	}
	// Syncing discards the custom policy and follows the parent immediately.
	ch.Synced, ch.Overrides = true, nil
	next, err := ApplyRoleChange(p, 1, RoleChange{Kind: ChannelAccessSet, Channel: ch, ExpectedRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	e, err := NewRoleEvaluator(next)
	if err != nil {
		t.Fatal(err)
	}
	if e.Evaluate(3, 102, ViewChannel).Allowed {
		t.Fatal("sync ignored parent deny")
	}
	// Channel tree mutations belong to the channel lifecycle transaction.
	ch.ParentID = 101
	if _, err := ApplyRoleChange(p, 1, RoleChange{Kind: ChannelAccessSet, Channel: ch, ExpectedRevision: 1}); !errors.Is(err, ErrRoleInvalid) {
		t.Fatal(err)
	}
}

func TestChannelAccessDelegationDoesNotGrantRoleManagement(t *testing.T) {
	p := roleFixture()
	p.Roles[2].Permissions = []Capability{ManageChannelAccess}
	e, err := NewRoleEvaluator(p)
	if err != nil {
		t.Fatal(err)
	}
	if e.CanManageRole(2, 20) {
		t.Fatal("channel manager gained server role management")
	}
	if !e.CanManageChannelRole(2, 102, 20) {
		t.Fatal("channel manager cannot manage a lower role override")
	}
	if e.CanManageChannelRole(2, 102, 30) || e.CanManageChannelRole(2, 102, 40) {
		t.Fatal("channel role hierarchy bypassed")
	}
	if e.CanManageChannelRole(3, 102, 10) {
		t.Fatal("member without channel management may edit everyone")
	}
	ch := p.Channels[2]
	ch.Overrides = []RoleOverride{{RoleID: 20, Capability: ViewChannel, Effect: Allow}}
	if _, err := ApplyRoleChange(p, 2, RoleChange{Kind: ChannelAccessSet, Channel: ch, ExpectedRevision: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyRoleChange(p, 2, RoleChange{Kind: RoleDelete, RoleID: 20, ExpectedRevision: 1}); !errors.Is(err, ErrRoleForbidden) {
		t.Fatal("channel manager may delete server roles", err)
	}
	ch.Overrides[0].RoleID = 30
	if _, err := ApplyRoleChange(p, 2, RoleChange{Kind: ChannelAccessSet, Channel: ch, ExpectedRevision: 1}); !errors.Is(err, ErrRoleForbidden) {
		t.Fatal("equal-role override permitted", err)
	}
}
