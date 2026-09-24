//go:build integration

package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"noxa/internal/authorization"
)

func roleTestStore(t *testing.T) (*Store, int64, int64) {
	t.Helper()
	s := testScratchStore(t)
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	var owner, member int64
	if err := s.db.QueryRowContext(t.Context(), `INSERT INTO users(unique_id,nickname) VALUES('role-owner','Role Owner') RETURNING id`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(t.Context(), `INSERT INTO users(unique_id,nickname) VALUES('role-member','Role Member') RETURNING id`).Scan(&member); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), `INSERT INTO channels(name,channel_type) VALUES('Private',2)`); err != nil {
		t.Fatal(err)
	}
	return s, owner, member
}

func TestRoleStorePreparationAndConstraints(t *testing.T) {
	s, owner, _ := roleTestStore(t)
	if _, err := s.RolePolicy(t.Context()); !errors.Is(err, authorization.ErrRolesNotConfigured) {
		t.Fatal(err)
	}
	p, err := s.PrepareRolePolicy(t.Context(), owner)
	if err != nil {
		t.Fatal(err)
	}
	e, err := authorization.NewRoleEvaluator(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Channels) != 1 || e.Evaluate(0, p.Channels[0].ChannelID, authorization.ViewChannel).Allowed {
		t.Fatal("existing channel is not closed")
	}
	if !e.Evaluate(owner, p.Channels[0].ChannelID, authorization.ViewChannel).Allowed {
		t.Fatal("owner locked out")
	}
	if len(p.Roles) != 4 || len(p.Members) != 0 || p.DefaultMemberRoleID != 0 {
		t.Fatalf("expected four unassigned roles and no automatic assignment: %+v", p)
	}
	wantNames := []string{"@everyone", "Member", "Moderator", "Administrator"}
	for i, role := range p.Roles {
		if role.Name != wantNames[i] || role.Position != i {
			t.Fatalf("template order: %+v", role)
		}
	}
	if len(p.Roles[0].Permissions) != 0 {
		t.Fatal("everyone gained default privileges")
	}
	for _, template := range p.Roles[1:] {
		fixture := p
		fixture.Members = []authorization.RoleMember{{UserID: owner + 1000, RoleIDs: []int64{template.ID}}}
		view, err := authorization.NewRoleEvaluator(fixture)
		if err != nil {
			t.Fatal(err)
		}
		if !view.Evaluate(owner+1000, 0, authorization.SendMessages).Allowed {
			t.Fatalf("%s cannot send messages", template.Name)
		}
		if view.Evaluate(owner+1000, p.Channels[0].ChannelID, authorization.ViewChannel).Allowed != (template.Name == "Administrator") {
			t.Fatalf("%s unexpectedly opened reset channel", template.Name)
		}
		if view.Evaluate(owner+1000, 0, authorization.ManageRoles).Allowed != (template.Name == "Administrator") {
			t.Fatalf("%s role management grants", template.Name)
		}
	}
	if _, err := s.PrepareRolePolicy(t.Context(), owner); !errors.Is(err, authorization.ErrRoleConflict) {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), `DELETE FROM users WHERE id=$1`, owner); err == nil {
		t.Fatal("owner deletion accepted")
	}
	if _, err := s.db.ExecContext(t.Context(), `DELETE FROM auth_roles WHERE id=$1`, p.EveryoneID); err == nil {
		t.Fatal("everyone deletion accepted")
	}
	var active bool
	if err := s.db.QueryRowContext(t.Context(), `SELECT active FROM authorization_config`).Scan(&active); err != nil || active {
		t.Fatalf("unexpected activation: %v %v", active, err)
	}
}

func TestRoleStorePreparationRejectsOrphanStaging(t *testing.T) {
	for _, orphan := range []string{"assigned role", "channel policy"} {
		t.Run(orphan, func(t *testing.T) {
			s, owner, member := roleTestStore(t)
			if orphan == "assigned role" {
				var roleID int64
				if err := s.db.QueryRowContext(t.Context(), `INSERT INTO auth_roles(name,position) VALUES('Old administrator',10) RETURNING id`).Scan(&roleID); err != nil {
					t.Fatal(err)
				}
				if _, err := s.db.ExecContext(t.Context(), `INSERT INTO auth_role_grants(role_id,capability) VALUES($1,'administrator')`, roleID); err != nil {
					t.Fatal(err)
				}
				if _, err := s.db.ExecContext(t.Context(), `INSERT INTO auth_member_roles(user_id,role_id) VALUES($1,$2)`, member, roleID); err != nil {
					t.Fatal(err)
				}
			} else if _, err := s.db.ExecContext(t.Context(), `INSERT INTO auth_channel_access(channel_id) SELECT id FROM channels`); err != nil {
				t.Fatal(err)
			}
			if _, err := s.PrepareRolePolicy(t.Context(), owner); !errors.Is(err, authorization.ErrRoleConflict) {
				t.Fatalf("orphan staging must fail with conflict, got %v", err)
			}
			var configured, audits int
			if err := s.db.QueryRowContext(t.Context(), `SELECT (SELECT count(*) FROM authorization_config),(SELECT count(*) FROM audit_log WHERE action='roles.prepare')`).Scan(&configured, &audits); err != nil {
				t.Fatal(err)
			}
			if configured != 0 || audits != 0 {
				t.Fatalf("failed preparation changed state: config=%d audits=%d", configured, audits)
			}
			report, err := s.InspectRoleSetup(t.Context(), "role-owner")
			if err != nil || report.State != "inconsistent" || len(report.Issues) != 1 || report.Issues[0] != "orphan_staging" {
				t.Fatalf("orphan staging was hidden: %+v %v", report, err)
			}
			if orphan == "assigned role" && (report.Counts["auth_roles"] != 1 || report.Counts["auth_member_roles"] != 1 || report.Counts["auth_role_grants"] != 1) {
				t.Fatal("refused preparation modified existing role authority")
			}
			if orphan == "channel policy" && report.Counts["auth_channel_access"] != 1 {
				t.Fatal("refused preparation modified existing channel policy")
			}
		})
	}
}

func TestRoleStorePreparationWaitsForConcurrentStaging(t *testing.T) {
	s, owner, _ := roleTestStore(t)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(t.Context(), `INSERT INTO auth_roles(name,position) VALUES('Concurrent staging',10)`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := s.PrepareRolePolicy(ctx, owner); result <- err }()
	select {
	case err := <-result:
		t.Fatalf("preparation did not wait for staged writes: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, authorization.ErrRoleConflict) {
		t.Fatalf("concurrent staging was adopted: %v", err)
	}
}

func TestRoleStoreConcurrentPreparationAndAuditRollback(t *testing.T) {
	s, owner, _ := roleTestStore(t)
	// A failed audit insert must roll back the entire initial policy.
	if _, err := s.db.ExecContext(t.Context(), `ALTER TABLE audit_log ADD CONSTRAINT reject_prepare_audit CHECK (action<>'roles.prepare')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PrepareRolePolicy(t.Context(), owner); err == nil {
		t.Fatal("preparation succeeded without its audit")
	}
	r, err := s.InspectRoleSetup(t.Context(), "role-owner")
	if err != nil || r.State != "unprepared" || r.Counts["auth_roles"] != 0 || r.Counts["auth_channel_access"] != 0 {
		t.Fatalf("preparation left partial state after rollback: %+v %v", r, err)
	}
	if _, err := s.db.ExecContext(t.Context(), `ALTER TABLE audit_log DROP CONSTRAINT reject_prepare_audit`); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() { _, err := s.PrepareRolePolicy(t.Context(), owner); results <- err })
	}
	wg.Wait()
	close(results)
	var successes, conflicts int
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, authorization.ErrRoleConflict):
			conflicts++
		default:
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
}

func TestRoleStoreOwnershipTransferIsAtomic(t *testing.T) {
	s, owner, member := roleTestStore(t)
	p, err := s.PrepareRolePolicy(t.Context(), owner)
	if err != nil {
		t.Fatal(err)
	}
	change := authorization.RoleChange{Kind: authorization.OwnerTransfer, UserID: member + 1000, ExpectedRevision: p.Revision}
	if _, err := s.ChangeRolePolicy(t.Context(), owner, change); !errors.Is(err, authorization.ErrRoleInvalid) {
		t.Fatalf("missing account accepted: %v", err)
	}
	unchanged, err := s.RolePolicy(t.Context())
	if err != nil || unchanged.OwnerID != owner || unchanged.Revision != p.Revision {
		t.Fatalf("failed transfer changed policy: %+v %v", unchanged, err)
	}
	change.UserID = member
	next, err := s.ChangeRolePolicy(t.Context(), owner, change)
	if err != nil {
		t.Fatal(err)
	}
	if next.OwnerID != member || next.Revision != p.Revision+1 {
		t.Fatalf("transfer: %+v", next)
	}
	var auditCount int
	if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM audit_log WHERE action='roles.owner_transfer'`).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("audit count %d: %v", auditCount, err)
	}
	change.ExpectedRevision, change.UserID = next.Revision, owner
	if _, err := s.ChangeRolePolicy(t.Context(), owner, change); !errors.Is(err, authorization.ErrRoleForbidden) {
		t.Fatalf("former owner retained authority: %v", err)
	}
	if _, err := s.db.ExecContext(t.Context(), `DELETE FROM users WHERE id=$1`, member); err == nil {
		t.Fatal("new owner deletion accepted")
	}
	if _, err := s.ChangeRolePolicy(t.Context(), member, change); err != nil {
		t.Fatalf("new owner cannot transfer: %v", err)
	}
}

func TestRoleStoreConcurrentMutationAndAtomicAudit(t *testing.T) {
	s, owner, member := roleTestStore(t)
	p, err := s.PrepareRolePolicy(t.Context(), owner)
	if err != nil {
		t.Fatal(err)
	}
	change := authorization.RoleChange{Kind: authorization.RoleCreate, ExpectedRevision: p.Revision, Role: authorization.Role{Name: "Moderator", Permissions: []authorization.Capability{authorization.ManageRoles}}}
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() { _, err := s.ChangeRolePolicy(t.Context(), owner, change); results <- err })
	}
	wg.Wait()
	close(results)
	var successes, conflicts int
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, authorization.ErrRoleConflict):
			conflicts++
		default:
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("success=%d conflict=%d", successes, conflicts)
	}
	p, err = s.RolePolicy(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if p.Revision != 2 || len(p.Roles) != 5 {
		t.Fatalf("unexpected snapshot: %+v", p)
	}
	roleID := p.Roles[1].ID
	assign := authorization.RoleChange{Kind: authorization.MemberRolesSet, ExpectedRevision: 2, UserID: member, RoleIDs: []int64{roleID}}
	p, err = s.ChangeRolePolicy(t.Context(), owner, assign)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Members) != 1 {
		t.Fatalf("assignment lost: %+v", p.Members)
	}
	// A foreign-key failure must leave both revision and audit unchanged.
	assign.ExpectedRevision, assign.UserID = 3, 999999
	if _, err := s.ChangeRolePolicy(t.Context(), owner, assign); err == nil {
		t.Fatal("missing user accepted")
	}
	p, err = s.RolePolicy(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if p.Revision != 3 {
		t.Fatal("failed change advanced revision")
	}
	var count int
	if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM audit_log WHERE action LIKE 'roles.%'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("audit count = %d, want prepare + create + assign", count)
	}
	// The owner record and all content survive ordinary policy writes.
	if _, err := s.ChangeRolePolicy(t.Context(), owner, authorization.RoleChange{Kind: authorization.RoleDelete, RoleID: roleID, ExpectedRevision: 3}); err != nil {
		t.Fatal(err)
	}
	p, err = s.RolePolicy(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Members) != 0 || len(p.Roles) != 4 || len(p.Channels) != 1 {
		t.Fatalf("bad deletion cleanup: %+v", p)
	}
}

func TestRoleStoreChannelAccessAndMemberSearch(t *testing.T) {
	s, owner, member := roleTestStore(t)
	p, err := s.PrepareRolePolicy(t.Context(), owner)
	if err != nil {
		t.Fatal(err)
	}
	query := authorization.MemberQuery{ExpectedRevision: 1, Search: "Role Member"}
	page, err := s.RoleMembers(t.Context(), owner, query)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 1 || page.Entries[0].UserID != member || !page.Entries[0].Manageable {
		t.Fatalf("unexpected roster: %+v", page)
	}
	if _, err := s.RoleMembers(t.Context(), member, query); !errors.Is(err, authorization.ErrRoleForbidden) {
		t.Fatal(err)
	}
	query.Search = "%"
	page, err = s.RoleMembers(t.Context(), owner, query)
	if err != nil || len(page.Entries) != 0 {
		t.Fatalf("wildcard was not treated literally: %+v %v", page, err)
	}
	ch := p.Channels[0]
	ch.Overrides = append(ch.Overrides, authorization.RoleOverride{UserID: member, Capability: authorization.ViewChannel, Effect: authorization.Allow})
	p, err = s.ChangeRolePolicy(t.Context(), owner, authorization.RoleChange{Kind: authorization.ChannelAccessSet, ExpectedRevision: 1, Channel: ch})
	if err != nil {
		t.Fatal(err)
	}
	p, err = s.RolePolicy(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	e, err := authorization.NewRoleEvaluator(p)
	if err != nil {
		t.Fatal(err)
	}
	if !e.Evaluate(member, ch.ChannelID, authorization.ViewChannel).Allowed || e.Evaluate(0, ch.ChannelID, authorization.ViewChannel).Allowed {
		t.Fatal("member exception did not survive storage")
	}
	if _, err := s.RoleMembers(t.Context(), owner, query); !errors.Is(err, authorization.ErrRoleConflict) {
		t.Fatal(err)
	}
}
