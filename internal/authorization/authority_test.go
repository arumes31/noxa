package authorization

import (
	"context"
	"errors"
	"sync"
	"testing"
)

type authorityTestBackend struct{ policy RolePolicy }

func (b *authorityTestBackend) RolePolicy(context.Context) (RolePolicy, error) {
	return cloneRolePolicy(b.policy), nil
}
func (b *authorityTestBackend) ChangeRolePolicy(_ context.Context, actor int64, change RoleChange) (RolePolicy, error) {
	p, err := ApplyRoleChange(b.policy, actor, change)
	if err == nil {
		b.policy = p
	}
	return p, err
}

func TestAuthorityRefreshIfChangedPublishesExternalRegistration(t *testing.T) {
	backend := &authorityTestBackend{policy: roleFixture()}
	backend.policy.DefaultMemberRoleID = 20
	reconciliations := 0
	a, err := NewAuthority(t.Context(), backend, func(context.Context, *RoleEvaluator, *RoleEvaluator) error {
		reconciliations++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.RefreshIfChanged(t.Context(), 0, nil); err != nil || reconciliations != 0 {
		t.Fatalf("unchanged admission refresh: reconciliations=%d error=%v", reconciliations, err)
	}
	backend.policy.Members = append(backend.policy.Members, RoleMember{UserID: 5, RoleIDs: []int64{20}})
	if err := a.RefreshIfChanged(t.Context(), 5, func(int64) bool { return true }); err != nil || reconciliations != 0 {
		t.Fatalf("registration admission refresh: reconciliations=%d error=%v", reconciliations, err)
	}
	if err := a.WithAccess(t.Context(), 5, 102, SendMessages, func(*RoleEvaluator) error { return nil }); err != nil {
		t.Fatalf("new member did not receive default role: %v", err)
	}
	if err := a.RefreshIfChanged(t.Context(), 5, func(int64) bool { return true }); err != nil || reconciliations != 0 {
		t.Fatalf("repeated admission refresh: reconciliations=%d error=%v", reconciliations, err)
	}
	backend.policy.Members[1].RoleIDs = []int64{20, 30}
	if err := a.RefreshIfChanged(t.Context(), 5, func(int64) bool { return true }); err != nil || reconciliations != 1 {
		t.Fatalf("other same-revision change skipped reconciliation: reconciliations=%d error=%v", reconciliations, err)
	}
}

func TestAuthorityRefreshReconcilesDefaultRoleRevocation(t *testing.T) {
	backend := &authorityTestBackend{policy: roleFixture()}
	backend.policy.DefaultMemberRoleID = 20
	backend.policy.Channels[2].Overrides = []RoleOverride{{RoleID: 20, Capability: ViewChannel, Effect: Deny}}
	reconciliations := 0
	a, err := NewAuthority(t.Context(), backend, func(context.Context, *RoleEvaluator, *RoleEvaluator) error {
		reconciliations++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	backend.policy.Members = append(backend.policy.Members, RoleMember{UserID: 5, RoleIDs: []int64{20}})
	if err := a.RefreshIfChanged(t.Context(), 5, func(int64) bool { return true }); err != nil || reconciliations != 1 {
		t.Fatalf("revoking default-role override skipped reconciliation: count=%d error=%v", reconciliations, err)
	}
}

func TestAuthorityRefreshReconcilesLiveMember(t *testing.T) {
	backend := &authorityTestBackend{policy: roleFixture()}
	backend.policy.DefaultMemberRoleID = 20
	reconciliations := 0
	a, err := NewAuthority(t.Context(), backend, func(context.Context, *RoleEvaluator, *RoleEvaluator) error {
		reconciliations++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	backend.policy.Members = append(backend.policy.Members, RoleMember{UserID: 5, RoleIDs: []int64{20}})
	if err := a.RefreshIfChanged(t.Context(), 5, func(int64) bool { return false }); err != nil || reconciliations != 1 {
		t.Fatalf("live member skipped reconciliation: count=%d error=%v", reconciliations, err)
	}
}

func TestAuthorityValidatesIntegrationBeforeMutation(t *testing.T) {
	backend := &authorityTestBackend{policy: roleFixture()}
	reconciled := false
	a, err := NewAuthority(t.Context(), backend, func(context.Context, *RoleEvaluator, *RoleEvaluator) error { reconciled = true; return nil })
	if err != nil {
		t.Fatal(err)
	}
	denied := errors.New("integration admission unavailable")
	change := RoleChange{Kind: RoleDelete, RoleID: 20, ExpectedRevision: 1}
	p, err := a.ChangeRolePolicyValidated(t.Context(), 1, change, func(context.Context) error {
		if a.gate.TryRLock() {
			a.gate.RUnlock()
			t.Fatal("admission validation did not hold the mutation barrier")
		}
		return denied
	})
	if !errors.Is(err, denied) || p.Revision != 0 || backend.policy.Revision != 1 || reconciled {
		t.Fatalf("denied validation mutated policy: revision=%d stored=%d reconciled=%v error=%v", p.Revision, backend.policy.Revision, reconciled, err)
	}
	if _, err := a.RolePolicy(t.Context()); err != nil {
		t.Fatalf("pre-commit denial poisoned authority: %v", err)
	}
	p, err = a.ChangeRolePolicyValidated(t.Context(), 1, change, func(context.Context) error { return nil })
	if err != nil || p.Revision != 2 || !reconciled {
		t.Fatalf("valid change: %+v %v", p, err)
	}
}

func TestAuthorityRevocationWaitsForProtectedOperation(t *testing.T) {
	backend := &authorityTestBackend{policy: roleFixture()}
	a, err := NewAuthority(t.Context(), backend, func(context.Context, *RoleEvaluator, *RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	var workers sync.WaitGroup
	workers.Go(func() {
		done <- a.WithAccess(t.Context(), 3, 102, ViewChannel, func(*RoleEvaluator) error {
			// Check the barrier while the protected effect is actually running;
			// launching a writer alone does not establish that it was scheduled.
			if a.gate.TryLock() {
				a.gate.Unlock()
				t.Error("policy writer can enter during a protected effect")
			}
			close(entered)
			select {
			case <-release:
				return nil
			case <-t.Context().Done():
				return t.Context().Err()
			}
		})
	})
	<-entered
	changed := make(chan error, 1)
	workers.Go(func() {
		role := backend.policy.Roles[0]
		role.Permissions = nil
		_, err := a.ChangeRolePolicy(t.Context(), 1, RoleChange{Kind: RoleUpdate, Role: role, ExpectedRevision: 1})
		changed <- err
	})
	close(release)
	workers.Wait()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-changed; err != nil {
		t.Fatal(err)
	}
	called := false
	err = a.WithAccess(t.Context(), 3, 102, ViewChannel, func(*RoleEvaluator) error { called = true; return nil })
	if !errors.Is(err, ErrRoleForbidden) || called {
		t.Fatalf("revocation failed: called=%v err=%v", called, err)
	}
}

func TestAuthorityFailedReconciliationClosesAccessUntilReload(t *testing.T) {
	backend := &authorityTestBackend{policy: roleFixture()}
	fail := true
	a, err := NewAuthority(t.Context(), backend, func(context.Context, *RoleEvaluator, *RoleEvaluator) error {
		if fail {
			return errors.New("key rotation failed")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	committed, err := a.ChangeRolePolicy(t.Context(), 1, RoleChange{Kind: RoleDelete, RoleID: 20, ExpectedRevision: 1})
	if !errors.Is(err, ErrEnforcementPending) || !errors.Is(err, ErrAuthorizationUnavailable) {
		t.Fatal(err)
	}
	if committed.Revision != 2 {
		t.Fatalf("committed change was not acknowledged: revision=%d", committed.Revision)
	}
	if backend.policy.Revision != 2 {
		t.Fatal("test did not commit before reconciliation")
	}
	if err := a.WithAccess(t.Context(), 1, 102, ViewChannel, func(*RoleEvaluator) error { t.Fatal("owner bypassed failed reconciliation"); return nil }); !errors.Is(err, ErrAuthorizationUnavailable) {
		t.Fatal(err)
	}
	fail = false
	committedPolicy := backend.policy
	backend.policy = roleFixture()
	if err := a.Reload(t.Context()); !errors.Is(err, ErrAuthorizationUnavailable) {
		t.Fatalf("recovery accepted a revision older than the saved change: %v", err)
	}
	backend.policy = committedPolicy
	if err := a.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := a.WithAccess(t.Context(), 1, 102, ViewChannel, func(*RoleEvaluator) error { return nil }); err != nil {
		t.Fatal(err)
	}
}
