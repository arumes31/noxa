//go:build integration

package server

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/config"
	"noxa/internal/netproto"
	"noxa/internal/store"
)

func TestIntegrationComplaintsPaginationScopeAndRevocation(t *testing.T) {
	db := integrationManagementStore(t)
	a := auth.New(db, zap.NewNop())
	create := func(name string) auth.IntegrationPrincipal {
		t.Helper()
		uid, err := a.RegisterUser(t.Context(), name, "complaint-password")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET integration_enabled=TRUE WHERE unique_id=$1", uid); err != nil {
			t.Fatal(err)
		}
		p, err := a.AuthenticateIntegration(t.Context(), uid, "complaint-password", "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	member, owner := create("complaint-reader"), create("complaint-owner")
	backend := serverRoleFixture()
	backend.policy.OwnerID = owner.UserID()
	backend.policy.Roles[0].Permissions = nil
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	srv := New(&config.Config{}, zap.NewNop(), &Deps{Auth: a, Authority: authority, Complaints: db, Groups: db})
	add := func(reporter, target, reason string) {
		t.Helper()
		if err := db.AddComplaint(t.Context(), reporter, target, reason); err != nil {
			t.Fatal(err)
		}
	}
	add(member.UniqueID(), owner.UniqueID(), "First | report\nreason")
	add(owner.UniqueID(), owner.UniqueID(), "Second")
	add(member.UniqueID(), member.UniqueID(), "Unrelated")
	read := func(p auth.IntegrationPrincipal, q netproto.ComplaintQuery) (netproto.ComplaintPage, error) {
		var page netproto.ComplaintPage
		err := srv.WithIntegrationComplaints(t.Context(), p, q, func(_ context.Context, got netproto.ComplaintPage) error { page = got; return nil })
		return page, err
	}
	request := netproto.ComplaintClear{TargetUniqueID: owner.UniqueID(), FromUniqueID: member.UniqueID()}
	if _, err := read(member, netproto.ComplaintQuery{}); !errors.Is(err, authorization.ErrRoleForbidden) {
		t.Fatalf("ordinary member read: %v", err)
	}
	if _, err := srv.ClearIntegrationComplaints(t.Context(), member, request); !errors.Is(err, authorization.ErrRoleForbidden) {
		t.Fatalf("ordinary member delete: %v", err)
	}
	first, err := read(owner, netproto.ComplaintQuery{Limit: 1})
	if err != nil || len(first.Entries) != 1 || first.NextAfterID != first.Entries[0].ID {
		t.Fatalf("first page: %+v %v", first, err)
	}
	entry := first.Entries[0]
	if entry.TargetNickname != "complaint-owner" || entry.FromNickname != "complaint-reader" || entry.Reason != "First | report\nreason" || entry.CreatedAt <= 0 {
		t.Fatalf("native projection lost fields: %+v", entry)
	}
	if _, err := srv.ClearIntegrationComplaints(t.Context(), owner, request); err != nil {
		t.Fatal(err)
	}
	add(owner.UniqueID(), member.UniqueID(), "Inserted after cursor")
	page, err := read(owner, netproto.ComplaintQuery{AfterID: first.NextAfterID, Limit: 100})
	if err != nil || len(page.Entries) != 3 || page.NextAfterID != 0 {
		t.Fatalf("page after deletion/insertion: %+v %v", page, err)
	}
	previous := first.NextAfterID
	for _, entry := range page.Entries {
		if entry.ID <= previous {
			t.Fatal("ascending cursor repeated a row")
		}
		previous = entry.ID
	}
	for _, q := range []netproto.ComplaintQuery{{AfterID: -1}, {Limit: -1}, {Limit: 101}} {
		if _, err := read(owner, q); !errors.Is(err, authorization.ErrRoleInvalid) {
			t.Fatalf("invalid request: %v", err)
		}
		if _, err := db.ListComplaintPage(t.Context(), q.AfterID, q.Limit); err == nil {
			t.Fatal("store accepted invalid page")
		}
	}
	if _, err := srv.ClearIntegrationComplaints(t.Context(), owner, netproto.ComplaintClear{}); !errors.Is(err, authorization.ErrRoleInvalid) {
		t.Fatal(err)
	}
	backend.mu.Lock()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.BanMembers}
	backend.policy.Revision++
	backend.mu.Unlock()
	if err := authority.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	result, err := srv.ClearIntegrationComplaints(t.Context(), member, request)
	if err != nil || result.Deleted != 0 {
		t.Fatalf("repeat was not harmless: %+v %v", result, err)
	}
	result, err = srv.ClearIntegrationComplaints(t.Context(), member, netproto.ComplaintClear{TargetUniqueID: owner.UniqueID()})
	if err != nil || result.Deleted != 1 {
		t.Fatalf("target clear: %+v %v", result, err)
	}
	page, err = read(member, netproto.ComplaintQuery{})
	if err != nil || len(page.Entries) != 2 {
		t.Fatalf("unrelated target deleted: %+v %v", page, err)
	}
	for _, entry := range page.Entries {
		if entry.TargetUniqueID != member.UniqueID() {
			t.Fatal("wrong deletion scope")
		}
	}
	var actor, target, detail string
	if err := db.DB().QueryRowContext(t.Context(), "SELECT actor_unique_id,target,detail FROM audit_log WHERE action='complaint_clear' ORDER BY id DESC LIMIT 1").Scan(&actor, &target, &detail); err != nil {
		t.Fatal(err)
	}
	var auditDetail store.AuditDetail
	if err := json.Unmarshal([]byte(detail), &auditDetail); err != nil {
		t.Fatal(err)
	}
	if actor != member.UniqueID() || target != owner.UniqueID() || auditDetail.Text != "count=1" || auditDetail.Version != store.AuditDetailVersion || auditDetail.Channels == nil || len(auditDetail.Channels) != 0 {
		t.Fatalf("noncanonical audit: %q %q %q", actor, target, detail)
	}
	if _, err := db.DB().ExecContext(t.Context(), "INSERT INTO complaints (reporter,target,reason) SELECT 'page-reporter-' || n, 'page-target', 'Bounded page' FROM generate_series(1,101) n"); err != nil {
		t.Fatal(err)
	}
	page, err = read(member, netproto.ComplaintQuery{})
	if err != nil || len(page.Entries) != 50 || page.NextAfterID != page.Entries[49].ID {
		t.Fatalf("unbounded default page: %+v %v", page, err)
	}
	page, err = read(member, netproto.ComplaintQuery{Limit: 100})
	if err != nil || len(page.Entries) != 100 || page.NextAfterID != page.Entries[99].ID {
		t.Fatalf("unbounded maximum page: %+v %v", page, err)
	}
	page, err = read(member, netproto.ComplaintQuery{AfterID: page.NextAfterID, Limit: 100})
	if err != nil || len(page.Entries) != 3 || page.NextAfterID != 0 {
		t.Fatalf("final bounded page: %+v %v", page, err)
	}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	var once sync.Once
	defer once.Do(func() { close(release) })
	go func() {
		done <- srv.WithIntegrationComplaints(t.Context(), member, netproto.ComplaintQuery{}, func(context.Context, netproto.ComplaintPage) error {
			if srv.roleMetadataMu.TryLock() {
				srv.roleMetadataMu.Unlock()
				return errors.New("metadata released before delivery")
			}
			close(entered)
			<-release
			return nil
		})
	}()
	select {
	case <-entered:
	case err := <-done:
		t.Fatal(err)
	case <-time.After(3 * time.Second):
		t.Fatal("no delivery")
	}
	backend.mu.Lock()
	backend.policy.Roles[0].Permissions = nil
	backend.policy.Revision++
	backend.mu.Unlock()
	reloaded := make(chan error, 1)
	go func() { reloaded <- authority.Reload(t.Context()) }()
	select {
	case err := <-reloaded:
		t.Fatalf("revocation passed protected delivery: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	once.Do(func() { close(release) })
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-reloaded; err != nil {
		t.Fatal(err)
	}
	if _, err := read(member, netproto.ComplaintQuery{}); !errors.Is(err, authorization.ErrRoleForbidden) {
		t.Fatalf("revoked read: %v", err)
	}
	if _, err := srv.ClearIntegrationComplaints(t.Context(), member, request); !errors.Is(err, authorization.ErrRoleForbidden) {
		t.Fatalf("revoked clear: %v", err)
	}
	if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET integration_enabled=FALSE WHERE id=$1", owner.UserID()); err != nil {
		t.Fatal(err)
	}
	if _, err := read(owner, netproto.ComplaintQuery{}); !errors.Is(err, auth.ErrIntegrationDenied) {
		t.Fatalf("disabled owner read: %v", err)
	}
	if _, err := srv.ClearIntegrationComplaints(t.Context(), owner, request); !errors.Is(err, auth.ErrIntegrationDenied) {
		t.Fatalf("disabled owner clear: %v", err)
	}
}
