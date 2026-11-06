package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// rebuildUnpartitionedAuditLog replaces the migrated audit_log with the
// pre-068 shape (BIGSERIAL id, old indexes) so the 068 conversion can run on
// a populated table.
func rebuildUnpartitionedAuditLog(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	for _, stmt := range []string{
		`DROP TABLE public.audit_log`,
		`CREATE TABLE public.audit_log (
			id BIGSERIAL PRIMARY KEY,
			actor_unique_id TEXT NOT NULL DEFAULT '',
			action TEXT NOT NULL,
			target TEXT NOT NULL DEFAULT '',
			detail TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			scope_channel_ids BIGINT[])`,
		`CREATE INDEX idx_audit_log_created ON public.audit_log (id DESC)`,
		`CREATE INDEX idx_audit_log_retention ON public.audit_log (created_at, id)`,
	} {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
}

func runAuditPartitionMigration(t *testing.T, s *Store) {
	t.Helper()
	content, err := migrationsFS.ReadFile("migrations/068_audit_log_partitions.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(context.Background(), string(content)); err != nil {
		t.Fatalf("applying 068: %v", err)
	}
}

func auditCount(t *testing.T, s *Store, table string) int64 {
	t.Helper()
	var n int64
	if err := s.db.QueryRowContext(context.Background(), `SELECT count(*) FROM public.` + auditQuoteIdent(table)).Scan(&n); err != nil {
		t.Fatalf("counting %s: %v", table, err)
	}
	return n
}

func TestAuditLogPartitionMigrationKeepsRowsIDsAndGrants(t *testing.T) {
	s := testDedicatedDBStore(t)
	ctx := context.Background()
	rebuildUnpartitionedAuditLog(t, s)
	role := fmt.Sprintf("noxa_audit_test_%d", time.Now().UnixNano())
	if _, err := s.db.ExecContext(ctx, `CREATE ROLE `+role+` NOLOGIN`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.db.ExecContext(context.Background(), `DROP OWNED BY ` + role)
		_, _ = s.db.ExecContext(context.Background(), `DROP ROLE IF EXISTS ` + role)
	})
	if _, err := s.db.ExecContext(ctx, `GRANT SELECT, INSERT ON public.audit_log TO `+role); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	old := now.AddDate(-2, 0, 0)
	farFuture := now.AddDate(3, 0, 0) // beyond the 12 prepared months: DEFAULT
	for i, at := range []time.Time{old, old.Add(time.Hour), now.AddDate(0, -1, 0), now, farFuture} {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO public.audit_log (actor_unique_id, action, target, detail, created_at, scope_channel_ids)
			VALUES ($1, 'test.action', 'target', 'detail', $2, '{}')`, fmt.Sprintf("actor-%d", i), at); err != nil {
			t.Fatal(err)
		}
	}
	var maxBefore int64
	if err := s.db.QueryRowContext(context.Background(), `SELECT max(id) FROM public.audit_log`).Scan(&maxBefore); err != nil {
		t.Fatal(err)
	}
	idsBefore := auditIDs(t, s)

	runAuditPartitionMigration(t, s)

	var partitioned bool
	if err := s.db.QueryRowContext(context.Background(), `SELECT EXISTS (SELECT 1 FROM pg_partitioned_table WHERE partrelid='public.audit_log'::regclass)`).Scan(&partitioned); err != nil || !partitioned {
		t.Fatalf("audit_log partitioned = %v, %v", partitioned, err)
	}
	if got := auditIDs(t, s); got != idsBefore {
		t.Fatalf("ids changed: %s -> %s", idsBefore, got)
	}
	if n := auditCount(t, s, "audit_log_default"); n != 1 {
		t.Fatalf("default partition holds %d rows, want the far-future one", n)
	}
	oldPart := auditPartitionFor(monthStart(old))
	if n := auditCount(t, s, oldPart); n != 2 {
		t.Fatalf("%s holds %d rows, want 2", oldPart, n)
	}
	// The sequence continues after the highest copied id.
	s.Audit(ctx, "actor-new", "test.after", "", "")
	var maxAfter int64
	if err := s.db.QueryRowContext(context.Background(), `SELECT max(id) FROM public.audit_log`).Scan(&maxAfter); err != nil || maxAfter <= maxBefore {
		t.Fatalf("new id %d not after %d (%v)", maxAfter, maxBefore, err)
	}
	// Grants of the old table reach the parent and every partition.
	for _, table := range []string{"audit_log", "audit_log_default", oldPart, auditPartitionFor(monthStart(now))} {
		var sel, ins, upd bool
		if err := s.db.QueryRowContext(context.Background(), `SELECT has_table_privilege($1, 'public.'||quote_ident($2), 'SELECT'),
			has_table_privilege($1, 'public.'||quote_ident($2), 'INSERT'),
			has_table_privilege($1, 'public.'||quote_ident($2), 'UPDATE')`, role, table).Scan(&sel, &ins, &upd); err != nil {
			t.Fatal(err)
		}
		if !sel || !ins || upd {
			t.Fatalf("%s grants for test role: select=%v insert=%v update=%v", table, sel, ins, upd)
		}
	}
	// Both old indexes exist on the partitioned parent.
	var indexes string
	if err := s.db.QueryRowContext(context.Background(), `SELECT string_agg(indexname, ',' ORDER BY indexname) FROM pg_indexes WHERE tablename='audit_log'`).Scan(&indexes); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"audit_log_pkey", "idx_audit_log_created", "idx_audit_log_retention"} {
		if !strings.Contains(indexes, want) {
			t.Fatalf("index %s missing from %s", want, indexes)
		}
	}
	// Running 068 again changes nothing.
	runAuditPartitionMigration(t, s)
	if got := auditCount(t, s, "audit_log"); got != 6 {
		t.Fatalf("rows after re-run = %d, want 6", got)
	}
	// Listing still works through the parent.
	entries, err := s.AuditList(ctx, 0, 10)
	if err != nil || len(entries) != 6 {
		t.Fatalf("ListAuditLog = %d entries, %v", len(entries), err)
	}
}

func auditIDs(t *testing.T, s *Store) string {
	t.Helper()
	var ids string
	if err := s.db.QueryRowContext(context.Background(), `SELECT coalesce(string_agg(id::text || '@' || to_char(created_at AT TIME ZONE 'UTC', 'YYYYMMDDHH24MISS'), ',' ORDER BY id), '') FROM public.audit_log`).Scan(&ids); err != nil {
		t.Fatal(err)
	}
	return ids
}

func TestMaintainAuditPartitionsCreatesMovesAndDropsOnlyOld(t *testing.T) {
	s := testDedicatedDBStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	// A future month, as if the migration ran long ago: noxa-migrate later
	// must move it out of DEFAULT. Remove that month's partition first.
	future := monthStart(now).AddDate(0, 14, 0)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO public.audit_log (action, created_at) VALUES ('test.future', $1)`, future.Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n := auditCount(t, s, "audit_log_default"); n != 1 {
		t.Fatalf("default rows = %d", n)
	}
	report, err := s.MaintainAuditPartitions(ctx, now.AddDate(0, 2, 0), 0)
	if err != nil {
		t.Fatal(err)
	}
	if report.MovedRows != 1 || auditCount(t, s, "audit_log_default") != 0 {
		t.Fatalf("report %+v, default rows %d", report, auditCount(t, s, "audit_log_default"))
	}
	if n := auditCount(t, s, auditPartitionFor(future)); n != 1 {
		t.Fatalf("%s holds %d rows", auditPartitionFor(future), n)
	}
	ready, defaultRows, ok, err := s.AuditPartitionCoverage(ctx, now)
	if err != nil || !ok || !ready || defaultRows != 0 {
		t.Fatalf("coverage = %v %d %v %v", ready, defaultRows, ok, err)
	}
	// A second run creates nothing.
	again, err := s.MaintainAuditPartitions(ctx, now.AddDate(0, 2, 0), 0)
	if err != nil || len(again.Created) != 0 || len(again.Dropped) != 0 {
		t.Fatalf("second run %+v, %v", again, err)
	}

	// Retention: an old partition with rows, and the current month.
	old := monthStart(now).AddDate(0, -10, 0)
	if _, err := s.MaintainAuditPartitions(ctx, old, 0); err != nil { // creates the old months
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO public.audit_log (action, created_at) VALUES ('test.old', $1), ('test.old', $1), ('test.now', $2)`, old.Add(time.Hour), now); err != nil {
		t.Fatal(err)
	}
	dropped, err := s.MaintainAuditPartitions(ctx, now, 6)
	if err != nil {
		t.Fatal(err)
	}
	cutoff := monthStart(now).AddDate(0, -6, 0)
	for _, name := range dropped.Dropped {
		month, ok := parseAuditPartition(name)
		if !ok || month.AddDate(0, 1, 0).After(cutoff) {
			t.Fatalf("dropped %s, newer than the cutoff %s", name, cutoff)
		}
	}
	if !containsString(dropped.Dropped, auditPartitionFor(old)) || dropped.DroppedRows != 2 {
		t.Fatalf("retention report %+v", dropped)
	}
	if n := auditCount(t, s, auditPartitionFor(monthStart(now))); n < 1 {
		t.Fatal("current month lost rows")
	}
	var audited int
	if err := s.db.QueryRowContext(context.Background(), `SELECT count(*) FROM public.audit_log WHERE action='audit.partition_dropped' AND target=$1`, auditPartitionFor(old)).Scan(&audited); err != nil || audited != 1 {
		t.Fatalf("drop audited %d times (%v)", audited, err)
	}
	if _, err := s.MaintainAuditPartitions(ctx, now, -1); err == nil {
		t.Fatal("negative retention accepted")
	}
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func TestParseAuditPartition(t *testing.T) {
	if m, ok := parseAuditPartition("audit_log_202611"); !ok || m != time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("parsed %v %v", m, ok)
	}
	for _, bad := range []string{"audit_log_default", "audit_log_202613", "audit_log_2026", "x_202611"} {
		if _, ok := parseAuditPartition(bad); ok {
			t.Fatalf("%s parsed", bad)
		}
	}
}
