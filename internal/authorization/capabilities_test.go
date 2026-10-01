package authorization

import "testing"

func TestUnimplementedInvitationsAreNotGrantable(t *testing.T) {
	for _, key := range []Capability{"manage_invitations", "use_invitations"} {
		p := roleFixture()
		p.Roles[0].Permissions = append(p.Roles[0].Permissions, key)
		if _, err := NewRoleEvaluator(p); err == nil {
			t.Fatalf("unimplemented capability accepted: %s", key)
		}
		for _, info := range Capabilities() {
			if info.Key == key {
				t.Fatalf("unimplemented capability advertised: %s", key)
			}
		}
	}
}
