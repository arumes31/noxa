package authorization

import (
	"errors"
	"testing"
)

func TestRoleDeletionProtection(t *testing.T) {
	p := roleFixture()
	for i := range p.Roles {
		if p.Roles[i].ID == 20 {
			p.Roles[i].DeletionProtected = true
		}
	}
	for _, actor := range []int64{1, 2, 4} {
		_, err := ApplyRoleChange(p, actor, RoleChange{Kind: RoleDelete, RoleID: 20, ExpectedRevision: p.Revision})
		if !errors.Is(err, ErrRoleForbidden) {
			t.Fatalf("actor %d deleted protected role: %v", actor, err)
		}
	}
	var role Role
	for _, r := range p.Roles {
		if r.ID == 20 {
			role = r
		}
	}
	role.DeletionProtected = false
	_, err := ApplyRoleChange(p, 2, RoleChange{Kind: RoleUpdate, Role: role, ExpectedRevision: p.Revision})
	if !errors.Is(err, ErrRoleForbidden) {
		t.Fatalf("manager removed protection: %v", err)
	}
	next, err := ApplyRoleChange(p, 1, RoleChange{Kind: RoleUpdate, Role: role, ExpectedRevision: p.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ApplyRoleChange(next, 1, RoleChange{Kind: RoleDelete, RoleID: 20, ExpectedRevision: next.Revision}); err != nil {
		t.Fatal(err)
	}
}

func TestNewAdministratorRoleStartsProtected(t *testing.T) {
	p := roleFixture()
	next, err := ApplyRoleChange(p, 1, RoleChange{Kind: RoleCreate, ExpectedRevision: p.Revision, Role: Role{ID: 50, Name: "Admin", Permissions: []Capability{Administrator}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range next.Roles {
		if r.ID == 50 && !r.DeletionProtected {
			t.Fatal("new admin is not protected")
		}
	}
}

// A transport without the flag (gRPC) must not clear protection on a rename,
// and a manager below the owner must still be able to edit the role.
func TestRoleUpdateKeepsDeletionProtection(t *testing.T) {
	p := roleFixture()
	var role Role
	for i := range p.Roles {
		if p.Roles[i].ID == 20 {
			p.Roles[i].DeletionProtected = true
			role = p.Roles[i]
		}
	}
	role.DeletionProtected, role.Name = false, "Renamed"
	for _, actor := range []int64{1, 2} {
		next, err := ApplyRoleChange(p, actor, RoleChange{Kind: RoleUpdate, Role: role, ExpectedRevision: p.Revision, KeepDeletionProtection: true})
		if err != nil {
			t.Fatalf("actor %d: %v", actor, err)
		}
		for _, r := range next.Roles {
			if r.ID == 20 && (!r.DeletionProtected || r.Name != "Renamed") {
				t.Fatalf("actor %d: role after update = %+v", actor, r)
			}
		}
	}
}
