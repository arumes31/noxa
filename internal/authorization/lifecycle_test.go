package authorization

import (
	"context"
	"errors"
	"testing"
)

func TestLifecycleNoOpDoesNotCommitOrCloseAuthority(t *testing.T) {
	backend := &authorityTestBackend{policy: roleFixture()}
	a, err := NewAuthority(t.Context(), backend, func(context.Context, *RoleEvaluator, *RoleEvaluator) error {
		t.Fatal("no-op invoked reconciliation")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := a.ChangeLifecyclePolicy(t.Context(), 1, func(context.Context) (RolePolicy, error) {
		return RolePolicy{}, ErrLifecycleUnchanged
	})
	if !errors.Is(err, ErrLifecycleUnchanged) || p.Revision != 0 {
		t.Fatalf("no-op acknowledged commit: %+v %v", p, err)
	}
	p, err = a.RolePolicy(t.Context())
	if err != nil || p.Revision != 1 {
		t.Fatalf("no-op changed authority: %+v %v", p, err)
	}
}

func TestLifecycleCommitAndReconciliationExcludeProtectedReaders(t *testing.T) {
	backend := &authorityTestBackend{policy: roleFixture()}
	var authority *Authority
	reconciled := false
	var err error
	authority, err = NewAuthority(t.Context(), backend, func(_ context.Context, before, after *RoleEvaluator) error {
		if authority.gate.TryRLock() {
			authority.gate.RUnlock()
			t.Error("reader entered before lifecycle reconciliation completed")
		}
		if before.Evaluate(3, 102, ViewChannel).Allowed == after.Evaluate(3, 102, ViewChannel).Allowed {
			t.Error("reconciler received wrong policies")
		}
		reconciled = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := authority.ChangeLifecyclePolicy(t.Context(), 1, func(context.Context) (RolePolicy, error) {
		if authority.gate.TryRLock() {
			authority.gate.RUnlock()
			t.Error("reader entered during lifecycle commit")
		}
		next, err := ApplyChannelTreeChange(backend.policy, 1, ChannelTreeChange{Kind: ChannelDelete, ExpectedRevision: 1, ChannelID: 102})
		if err == nil {
			backend.policy = next
		}
		return next, err
	})
	if err != nil || p.Revision != 2 || !reconciled {
		t.Fatalf("lifecycle result %+v %v reconciled=%t", p, err, reconciled)
	}
	if err := authority.WithAccess(t.Context(), 3, 102, ViewChannel, func(*RoleEvaluator) error { t.Fatal("deleted channel remains readable"); return nil }); !errors.Is(err, ErrRoleForbidden) {
		t.Fatal(err)
	}
}

func TestLifecycleKnownCommitFailureRetainsRevisionForRecovery(t *testing.T) {
	backend := &authorityTestBackend{policy: roleFixture()}
	stagedResource := false
	resourceMirrored := false
	authority, err := NewAuthority(t.Context(), backend, func(context.Context, *RoleEvaluator, *RoleEvaluator) error {
		if !stagedResource {
			t.Fatal("recovery lost committed resource")
		}
		resourceMirrored = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := authority.ChangeLifecyclePolicy(t.Context(), 1, func(context.Context) (RolePolicy, error) {
		next, err := ApplyChannelTreeChange(backend.policy, 1, ChannelTreeChange{Kind: ChannelDelete, ExpectedRevision: 1, ChannelID: 102})
		if err != nil {
			return RolePolicy{}, err
		}
		backend.policy = next
		stagedResource = true
		return next, errors.New("state mirroring failed after commit")
	})
	if saved.Revision != 2 || !errors.Is(err, ErrEnforcementPending) || resourceMirrored {
		t.Fatalf("lost pending commit: %+v %v", saved, err)
	}
	if err := authority.WithPolicy(t.Context(), func(*RoleEvaluator) error { t.Fatal("access reopened early"); return nil }); !errors.Is(err, ErrAuthorizationUnavailable) {
		t.Fatal(err)
	}
	committed := backend.policy
	backend.policy = roleFixture()
	if err := authority.Reload(t.Context()); !errors.Is(err, ErrAuthorizationUnavailable) {
		t.Fatalf("accepted old revision: %v", err)
	}
	backend.policy = committed
	if err := authority.Reload(t.Context()); err != nil || !resourceMirrored {
		t.Fatalf("recovery did not repair resources: %v", err)
	}
}

func TestLifecycleStaleRevisionDoesNotInvokeCommit(t *testing.T) {
	backend := &authorityTestBackend{policy: roleFixture()}
	authority, err := NewAuthority(t.Context(), backend, func(context.Context, *RoleEvaluator, *RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.ChangeLifecyclePolicy(t.Context(), 0, func(context.Context) (RolePolicy, error) {
		t.Fatal("stale mutation reached resource writer")
		return RolePolicy{}, nil
	}); !errors.Is(err, ErrRoleConflict) {
		t.Fatal(err)
	}
}

type ambiguousRoleBackend struct{ authorityTestBackend }

func (b *ambiguousRoleBackend) ChangeRolePolicy(_ context.Context, actorID int64, change RoleChange) (RolePolicy, error) {
	candidate, err := ApplyRoleChange(b.policy, actorID, change)
	if err != nil {
		return RolePolicy{}, err
	}
	return candidate, errors.New("ambiguous commit")
}

func TestOrdinaryRoleCommitCandidateIsNotAcknowledgedAsDurable(t *testing.T) {
	backend := &ambiguousRoleBackend{authorityTestBackend{policy: roleFixture()}}
	authority, err := NewAuthority(t.Context(), backend, func(context.Context, *RoleEvaluator, *RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	saved, err := authority.ChangeRolePolicy(t.Context(), 1, RoleChange{Kind: RoleDelete, RoleID: 20, ExpectedRevision: 1})
	if err == nil || errors.Is(err, ErrEnforcementPending) || saved.Revision != 0 {
		t.Fatalf("acknowledged unconfirmed candidate: %+v %v", saved, err)
	}
	if err := authority.Reload(t.Context()); err != nil {
		t.Fatalf("could not recover unchanged database: %v", err)
	}
}
