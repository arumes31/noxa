package authorization

import (
	"slices"
	"testing"
)

func TestRevokedReadScopesIncludesOfflineExceptionsAndSyncedChildren(t *testing.T) {
	p := roleFixture()
	p.Channels[0].Overrides = append(p.Channels[0].Overrides, RoleOverride{UserID: 999, Capability: ViewChannel, Effect: Allow})
	before, err := NewRoleEvaluator(p)
	if err != nil {
		t.Fatal(err)
	}
	p.Channels[0].Overrides = p.Channels[0].Overrides[:3]
	after, err := NewRoleEvaluator(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := RevokedReadScopes(before, after); !slices.Equal(got, []int64{100, 101}) {
		t.Fatalf("revoked scopes: %v", got)
	}
	if got := RevokedReadScopes(after, before); len(got) != 0 {
		t.Fatalf("a grant rotated keys: %v", got)
	}
}

func TestRevokedReadScopesIncludesEveryoneAndGlobalChat(t *testing.T) {
	p := roleFixture()
	before, err := NewRoleEvaluator(p)
	if err != nil {
		t.Fatal(err)
	}
	p.Roles[0].Permissions = nil
	after, err := NewRoleEvaluator(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := RevokedReadScopes(before, after); !slices.Equal(got, []int64{0, 102}) {
		t.Fatalf("revoked scopes: %v", got)
	}
}
