//go:build integration

package store

import (
	"context"
	"errors"
	"net/url"
	"testing"

	"noxa/internal/authorization"
)

func TestRoleProcessLeaseExcludesOtherProcesses(t *testing.T) {
	s := testScratchStore(t)
	dsn := scratchStoreDSN(t, s)

	serving, err := AcquireRoleProcessLease(t.Context(), dsn)
	if err != nil {
		t.Fatalf("acquiring serving lease: %v", err)
	}
	t.Cleanup(func() { _ = serving.Close() })
	if err := serving.Check(t.Context()); err != nil {
		t.Fatalf("checking serving lease: %v", err)
	}

	if competing, err := AcquireRoleProcessLease(t.Context(), dsn); !errors.Is(err, ErrRoleProcessBusy) {
		if competing != nil {
			_ = competing.Close()
		}
		t.Fatalf("competing lease error = %v, want ErrRoleProcessBusy", err)
	}
	if err := serving.Check(t.Context()); err != nil {
		t.Fatalf("serving lease lost after competing attempt: %v", err)
	}
	if err := serving.Close(); err != nil {
		t.Fatalf("releasing serving lease: %v", err)
	}
	if err := serving.Check(t.Context()); err == nil {
		t.Fatal("closed serving lease still passes Check")
	}

	offline, err := AcquireRoleProcessLease(t.Context(), dsn)
	if err != nil {
		t.Fatalf("acquiring offline lease after release: %v", err)
	}
	defer func() {
		if err := offline.Close(); err != nil {
			t.Errorf("releasing offline lease: %v", err)
		}
	}()
	if err := offline.Check(t.Context()); err != nil {
		t.Fatalf("checking offline lease: %v", err)
	}
}

func TestRoleProcessLeaseDetectsLostLock(t *testing.T) {
	s := testScratchStore(t)
	lease, err := AcquireRoleProcessLease(t.Context(), scratchStoreDSN(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.Close() }()
	var released bool
	if err := lease.conn.QueryRowContext(t.Context(), `SELECT pg_advisory_unlock($1)`, roleProcessLockID).Scan(&released); err != nil || !released {
		t.Fatalf("releasing held lock for loss test: released=%v error=%v", released, err)
	}
	if err := lease.Check(t.Context()); !errors.Is(err, errRoleProcessLeaseLost) {
		t.Fatalf("checking released lock = %v, want lease loss", err)
	}
}

func TestOfflineRegistrationRefreshesAuthoritySnapshot(t *testing.T) {
	s := testScratchStore(t)
	if err := s.MigrateContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	dsn := scratchStoreDSN(t, s)
	var ownerID int64
	if err := s.db.QueryRowContext(t.Context(), `INSERT INTO users(unique_id,nickname) VALUES('lease-owner','Lease Owner') RETURNING id`).Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PrepareRolePolicy(t.Context(), ownerID); err != nil {
		t.Fatal(err)
	}
	activated, err := s.ActivatePreparedRolePolicy(t.Context(), "lease-owner", func() (uint16, []byte, error) {
		return 1, []byte{1, 2, 3}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var memberRoleID int64
	for _, role := range activated.Policy.Roles {
		if role.Name == "Member" {
			memberRoleID = role.ID
			break
		}
	}
	if memberRoleID == 0 {
		t.Fatal("starter Member role missing")
	}
	if _, err := s.ChangeRolePolicy(t.Context(), ownerID, authorization.RoleChange{
		Kind: authorization.DefaultMemberRoleSet, ExpectedRevision: activated.Policy.Revision, RoleID: memberRoleID,
	}); err != nil {
		t.Fatal(err)
	}
	backend := activeLeasePolicyBackend{s}
	authority, err := authorization.NewAuthority(t.Context(), backend,
		func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}

	offline, err := AcquireRoleProcessLease(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	userID, err := s.RegisterUserWithDefaultRole(t.Context(), "lease-member", "Lease Member", "hash", "key")
	if err != nil {
		_ = offline.Close()
		t.Fatal(err)
	}
	if err := offline.Close(); err != nil {
		t.Fatal(err)
	}

	if err := authority.RefreshIfChanged(t.Context(), 0, nil); err != nil {
		t.Fatal(err)
	}
	policy, err := authority.RolePolicy(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range policy.Members {
		if member.UserID != userID {
			continue
		}
		if len(member.RoleIDs) != 1 || member.RoleIDs[0] != memberRoleID {
			t.Fatalf("registered member roles = %v, want [%d]", member.RoleIDs, memberRoleID)
		}
		return
	}
	t.Fatalf("registered user %d missing from Authority snapshot", userID)
}

type activeLeasePolicyBackend struct{ *Store }

func (b activeLeasePolicyBackend) RolePolicy(ctx context.Context) (authorization.RolePolicy, error) {
	return b.Store.ActiveRolePolicy(ctx)
}

func scratchStoreDSN(t *testing.T, s *Store) string {
	t.Helper()
	var databaseName string
	if err := s.db.QueryRowContext(t.Context(), `SELECT current_database()`).Scan(&databaseName); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(testDBURL())
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + databaseName
	u.RawPath = ""
	return u.String()
}
