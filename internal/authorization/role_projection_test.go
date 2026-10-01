package authorization

import (
	"context"
	"errors"
	"testing"
)

func TestBoundedPolicyIncludesNestedAssignmentsAndOverrides(t *testing.T) {
	p := roleFixture()
	e, err := NewRoleEvaluator(p)
	if err != nil {
		t.Fatal(err)
	}
	top := len(p.Roles) + len(p.Members) + len(p.Channels)
	if _, err := e.BoundedPolicy(t.Context(), top); !errors.Is(err, ErrAuthorizationUnavailable) {
		t.Fatalf("nested entries not counted: %v", err)
	}
	count := top
	for _, r := range p.Roles {
		count += len(r.Permissions)
	}
	for _, m := range p.Members {
		count += len(m.RoleIDs)
	}
	for _, c := range p.Channels {
		count += len(c.Overrides)
	}
	got, err := e.BoundedPolicy(t.Context(), count)
	if err != nil || got.Revision != p.Revision {
		t.Fatalf("exact budget: %+v %v", got, err)
	}
	got.Members[0].RoleIDs[0] = 999
	if e.Policy().Members[0].RoleIDs[0] == 999 {
		t.Fatal("projection aliases evaluator")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := e.BoundedPolicy(ctx, count); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation ignored: %v", err)
	}
}
