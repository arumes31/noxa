//go:build integration

package store

import (
	"reflect"
	"testing"

	"noxa/internal/authorization"
)

func TestRoleMentionabilityPersistenceRoundTrip(t *testing.T) {
	s, owner, _ := roleTestStore(t)
	p, err := s.PrepareRolePolicy(t.Context(), owner)
	if err != nil {
		t.Fatal(err)
	}
	p, err = s.ChangeRolePolicy(t.Context(), owner, authorization.RoleChange{
		Kind: authorization.RoleCreate, ExpectedRevision: p.Revision,
		Role: authorization.Role{Name: "Raid team", Color: "#12ab34", Icon: "R", Hoist: true, Permissions: []authorization.Capability{authorization.ViewChannel}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var role authorization.Role
	for _, candidate := range p.Roles {
		if candidate.Name == "Raid team" {
			role = candidate
		}
	}
	if role.ID == 0 {
		t.Fatal("created role missing")
	}
	for step, mentionable := range []bool{false, true, false} {
		if step != 0 {
			role.Mentionable = mentionable
			p, err = s.ChangeRolePolicy(t.Context(), owner, authorization.RoleChange{Kind: authorization.RoleUpdate, ExpectedRevision: p.Revision, Role: role})
			if err != nil {
				t.Fatal(err)
			}
		}
		// Read a fresh SQL-backed policy instead of checking the mutation's
		// returned in-memory value: both writing and scanning must persist it.
		loaded, err := s.RolePolicy(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, candidate := range loaded.Roles {
			if candidate.ID == role.ID {
				found = true
				if candidate.Mentionable != mentionable || !reflect.DeepEqual(candidate, role) {
					t.Fatalf("step=%d persisted role=%+v, want=%+v", step, candidate, role)
				}
			}
		}
		if !found || loaded.Revision != p.Revision {
			t.Fatalf("step=%d role found=%t revision=%d want=%d", step, found, loaded.Revision, p.Revision)
		}
	}
}
