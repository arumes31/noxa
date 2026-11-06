package auth

import (
	"errors"
	"slices"
	"testing"
)

func TestNormalizeIntegrationScopes(t *testing.T) {
	got, err := NormalizeIntegrationScopes([]string{ScopeMessagesManage, ScopeServerRead})
	if err != nil || !slices.Equal(got, []string{ScopeServerRead, ScopeMessagesManage}) {
		t.Fatalf("normalized = %v, %v", got, err)
	}
	if got, err := NormalizeIntegrationScopes(nil); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("empty list = %#v, %v; want an empty, non-nil list", got, err)
	}
	for _, bad := range [][]string{{"server:*"}, {ScopeServerRead, ScopeServerRead}, {""}} {
		if _, err := NormalizeIntegrationScopes(bad); !errors.Is(err, ErrIntegrationScopeInvalid) {
			t.Fatalf("%v accepted: %v", bad, err)
		}
	}
	seen := map[string]bool{}
	for _, scope := range IntegrationScopes {
		if seen[scope] {
			t.Fatalf("duplicate scope constant %q", scope)
		}
		seen[scope] = true
	}
}

func TestPrincipalScopeCeiling(t *testing.T) {
	unscoped := IntegrationPrincipal{}
	if !unscoped.AllowsScope(ScopeMembersBan) {
		t.Fatal("an account without a ceiling must keep full access")
	}
	scoped := IntegrationPrincipal{scoped: true, scopes: []string{ScopeServerRead}}
	if !scoped.AllowsScope(ScopeServerRead) || scoped.AllowsScope(ScopeServerManage) {
		t.Fatal("ceiling not applied")
	}
	none := IntegrationPrincipal{scoped: true}
	if none.AllowsScope(ScopeServerRead) {
		t.Fatal("an empty ceiling must allow nothing")
	}
	list, isScoped := scoped.Scopes()
	list[0] = "changed"
	if again, _ := scoped.Scopes(); !isScoped || again[0] != ScopeServerRead {
		t.Fatal("Scopes must return a copy")
	}
}
