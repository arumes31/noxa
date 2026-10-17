package authorization

import (
	"context"
	"errors"
	"testing"
	"time"
)

type blockingPolicyBackend struct {
	authorityTestBackend
	entered, release chan struct{}
}

func (b *blockingPolicyBackend) RolePolicy(ctx context.Context) (RolePolicy, error) {
	if b.entered != nil {
		b.entered <- struct{}{}
		<-b.release
	}
	return b.authorityTestBackend.RolePolicy(ctx)
}

// Realtime voice uses TryWithPolicy. A policy writer that is only queued
// behind a long shared lease (e.g. a renegotiation offer written to a slow
// client) must not make packets fail; one that holds the gate must.
func TestPacketPolicyLeaseIgnoresQueuedWriterButNotActiveWriter(t *testing.T) {
	backend := &authorityTestBackend{policy: roleFixture()}
	reconcileEntered, reconcileRelease := make(chan struct{}), make(chan struct{})
	reconciling := false
	a, err := NewAuthority(t.Context(), backend, func(context.Context, *RoleEvaluator, *RoleEvaluator) error {
		if reconciling {
			close(reconcileEntered)
			<-reconcileRelease
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	leaseHeld, leaseRelease, leaseDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(leaseDone)
		_ = a.WithPolicy(t.Context(), func(*RoleEvaluator) error { close(leaseHeld); <-leaseRelease; return nil })
	}()
	<-leaseHeld
	reconciling = true
	backend.policy.Members[1].RoleIDs = []int64{20, 30}
	writerDone := make(chan error, 1)
	go func() { writerDone <- a.Reload(context.Background()) }()
	time.Sleep(20 * time.Millisecond) // Let Reload queue on the gate.
	if err := a.TryWithPolicy(func(*RoleEvaluator) error { return nil }); err != nil {
		t.Fatalf("queued policy writer dropped a voice packet: %v", err)
	}
	close(leaseRelease)
	<-leaseDone
	select {
	case <-reconcileEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("writer did not acquire the gate")
	}
	if err := a.TryWithPolicy(func(*RoleEvaluator) error { t.Error("packet ran during reconciliation"); return nil }); err == nil {
		t.Fatal("packet lease bypassed an active policy writer")
	}
	close(reconcileRelease)
	if err := <-writerDone; err != nil {
		t.Fatal(err)
	}
	if err := a.TryWithPolicy(func(*RoleEvaluator) error { return nil }); err != nil {
		t.Fatalf("packet lease stayed closed after reload: %v", err)
	}
}

// Every login calls RefreshIfChanged. With no policy change it must not take
// the exclusive gate, even while its database read is slow.
func TestUnchangedLoginRefreshKeepsPacketGateOpen(t *testing.T) {
	backend := &blockingPolicyBackend{authorityTestBackend: authorityTestBackend{policy: roleFixture()}}
	a, err := NewAuthority(t.Context(), backend, func(context.Context, *RoleEvaluator, *RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	backend.entered, backend.release = make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- a.RefreshIfChanged(context.Background(), 0, nil) }()
	<-backend.entered
	if err := a.TryWithPolicy(func(*RoleEvaluator) error { return nil }); err != nil {
		t.Fatalf("login refresh closed the packet gate during its database read: %v", err)
	}
	close(backend.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// Realtime callers must be able to tell a momentarily busy gate from a
// missing policy: only the former may reuse a previous decision.
func TestTryWithPolicyDistinguishesBusyFromUnavailable(t *testing.T) {
	a, err := NewAuthority(t.Context(), &authorityTestBackend{policy: roleFixture()}, func(context.Context, *RoleEvaluator, *RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	unlock := a.lockExclusive()
	err = a.TryWithPolicy(func(*RoleEvaluator) error { t.Error("packet ran under a policy writer"); return nil })
	unlock()
	if !errors.Is(err, ErrAuthorizationBusy) || !errors.Is(err, ErrAuthorizationUnavailable) {
		t.Fatalf("held gate: got %v, want busy (wrapping unavailable)", err)
	}
	empty := &Authority{}
	err = empty.TryWithPolicy(func(*RoleEvaluator) error { t.Error("packet ran without a policy"); return nil })
	if !errors.Is(err, ErrAuthorizationUnavailable) || errors.Is(err, ErrAuthorizationBusy) {
		t.Fatalf("no evaluator: got %v, want unavailable but not busy", err)
	}
}
