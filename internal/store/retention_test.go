package store

import (
	"context"
	"testing"
	"time"
)

func TestRetentionBoundariesBatchesAndPendingDelivery(t *testing.T) {
	s := pollTestStore(t)
	ctx := t.Context()
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	dmCutoff, auditCutoff := now.Add(-30*24*time.Hour), now.Add(-180*24*time.Hour)
	var user int64
	if err := s.db.QueryRowContext(ctx, `INSERT INTO users(unique_id,nickname) VALUES('retention-user','User') RETURNING id`).Scan(&user); err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO offline_messages(from_user_id,to_user_id,message,delivered_at) SELECT $1,$1,'',$2 FROM generate_series(1,501)`, user, dmCutoff.Add(-time.Second))
	exec(`INSERT INTO offline_messages(from_user_id,to_user_id,message,delivered_at) VALUES($1,$1,'',$2),($1,$1,'',$3),($1,$1,'',NULL)`, user, dmCutoff, now)
	exec(`INSERT INTO audit_log(action,created_at) SELECT 'old',$1 FROM generate_series(1,501)`, auditCutoff.Add(-time.Second))
	exec(`INSERT INTO audit_log(action,created_at) VALUES('boundary',$1),('recent',$2)`, auditCutoff, now)
	day := auditCutoff.Truncate(24 * time.Hour)
	exec(`INSERT INTO audit_log_daily(day,action,target,event_count) SELECT $1,'old',i::text,1 FROM generate_series(1,501) AS i`, day.Add(-24*time.Hour))
	exec(`INSERT INTO audit_log_daily(day,action,event_count) VALUES($1,'boundary',1),($2,'recent',1)`, day, now)
	count := func(table string, want int) {
		t.Helper()
		var n int
		if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil || n != want {
			t.Fatalf("%s count=%d want=%d err=%v", table, n, want, err)
		}
	}
	if err := s.PruneExpiredData(ctx, now); err != nil {
		t.Fatal(err)
	}
	count("offline_messages", 4)
	count("audit_log", 3)
	count("audit_log_daily", 3)
	if err := s.PruneExpiredData(ctx, now); err != nil {
		t.Fatal(err)
	}
	count("offline_messages", 3)
	count("audit_log", 2)
	count("audit_log_daily", 2)
	// An idempotent repeat and cancellation must preserve recent and undelivered data.
	if err := s.PruneExpiredData(ctx, now); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.PruneExpiredData(canceled, now); err == nil {
		t.Fatal("ignored cancellation")
	}
	count("offline_messages", 3)
	count("audit_log", 2)
	count("audit_log_daily", 2)
}

func TestRetentionSkipsLockedRows(t *testing.T) {
	s := pollTestStore(t)
	ctx := t.Context()
	now := time.Now().UTC()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO audit_log(action,created_at) VALUES('locked',$1),('unlocked',$1)`, now.Add(-181*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `SELECT id FROM audit_log WHERE action='locked' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	batch, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err = s.PruneExpiredData(batch, now); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log`).Scan(&n); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = s.PruneExpiredData(ctx, now); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log`).Scan(&n); err != nil || n != 0 {
		t.Fatal(n, err)
	}
}
