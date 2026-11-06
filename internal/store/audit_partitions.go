package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// AuditPartitionMonthsAhead is how many months beyond the current one
// noxa-migrate keeps partitioned in advance (301).
const AuditPartitionMonthsAhead = 12

var auditPartitionName = regexp.MustCompile(`^audit_log_([0-9]{4})([0-9]{2})$`)

// AuditPartitionReport lists what MaintainAuditPartitions changed.
type AuditPartitionReport struct {
	Created     []string // new monthly partitions
	MovedRows   int64    // rows moved out of the DEFAULT partition
	Dropped     []string // partitions removed by retention
	DroppedRows int64    // rows those partitions held
}

func monthStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

func auditPartitionFor(month time.Time) string {
	return fmt.Sprintf("audit_log_%04d%02d", month.Year(), int(month.Month()))
}

// MaintainAuditPartitions creates monthly audit_log partitions from the
// current month through AuditPartitionMonthsAhead months ahead, plus a month
// for every row that landed in the DEFAULT partition (those rows move into
// it). With retentionMonths > 0 it drops partitions that ended more than
// retentionMonths whole months before the current month, recording each drop
// in the audit log first. It needs the migration owner's DDL privileges, so
// only noxa-migrate calls it; the serving process cannot.
func (s *Store) MaintainAuditPartitions(ctx context.Context, now time.Time, retentionMonths int) (AuditPartitionReport, error) {
	var report AuditPartitionReport
	if s == nil || s.db == nil {
		return report, errors.New("audit partitions: no database")
	}
	if retentionMonths < 0 {
		return report, fmt.Errorf("audit partitions: retention %d months must not be negative", retentionMonths)
	}
	partitioned, err := s.auditLogPartitioned(ctx)
	if err != nil || !partitioned {
		return report, err
	}
	current := monthStart(now)
	wanted := map[time.Time]bool{}
	for i := 0; i <= AuditPartitionMonthsAhead; i++ {
		wanted[current.AddDate(0, i, 0)] = true
	}
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT date_trunc('month', created_at AT TIME ZONE 'UTC') FROM public.audit_log_default`)
	if err != nil {
		return report, fmt.Errorf("audit partitions: reading default partition: %w", err)
	}
	for rows.Next() {
		var month time.Time
		if err := rows.Scan(&month); err != nil {
			_ = rows.Close()
			return report, err
		}
		wanted[monthStart(month)] = true
	}
	if err := rows.Close(); err != nil {
		return report, err
	}
	if err := rows.Err(); err != nil {
		return report, err
	}
	months := make([]time.Time, 0, len(wanted))
	for month := range wanted {
		months = append(months, month)
	}
	sort.Slice(months, func(i, j int) bool { return months[i].Before(months[j]) })
	for _, month := range months {
		created, moved, err := s.ensureAuditPartition(ctx, month)
		if err != nil {
			return report, err
		}
		if created {
			report.Created = append(report.Created, auditPartitionFor(month))
			report.MovedRows += moved
		}
	}
	if retentionMonths > 0 {
		cutoff := current.AddDate(0, -retentionMonths, 0)
		dropped, droppedRows, err := s.dropAuditPartitionsBefore(ctx, cutoff)
		if err != nil {
			return report, err
		}
		report.Dropped, report.DroppedRows = dropped, droppedRows
	}
	return report, nil
}

func (s *Store) auditLogPartitioned(ctx context.Context) (bool, error) {
	var partitioned bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_partitioned_table
		WHERE partrelid = to_regclass('public.audit_log'))`).Scan(&partitioned)
	if err != nil {
		return false, fmt.Errorf("audit partitions: checking audit_log: %w", err)
	}
	return partitioned, nil
}

// ensureAuditPartition creates the partition for month when it is missing.
// Rows of that month already in the DEFAULT partition move into it within
// the same transaction, because attaching a range the DEFAULT partition still
// holds rows for would fail.
func (s *Store) ensureAuditPartition(ctx context.Context, month time.Time) (created bool, moved int64, err error) {
	name := auditPartitionFor(month)
	from, to := month, month.AddDate(0, 1, 0)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, 0, err
	}
	defer func() { _ = tx.Rollback() }()
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT to_regclass('public.' || quote_ident($1)) IS NOT NULL`, name).Scan(&exists); err != nil {
		return false, 0, err
	}
	if exists {
		return false, 0, nil
	}
	// The default partition is locked first so no row of this month can
	// arrive between the move and the attach.
	if _, err := tx.ExecContext(ctx, `LOCK TABLE public.audit_log_default IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return false, 0, fmt.Errorf("audit partitions: locking default partition: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM public.audit_log_default WHERE created_at >= $1 AND created_at < $2`, from, to).Scan(&moved); err != nil {
		return false, 0, err
	}
	ident := auditQuoteIdent(name)
	bounds := fmt.Sprintf("FOR VALUES FROM (%s) TO (%s)", auditQuoteLiteral(from.Format(time.RFC3339)), auditQuoteLiteral(to.Format(time.RFC3339)))
	if moved == 0 {
		if _, err := tx.ExecContext(ctx, `CREATE TABLE public.`+ident+` PARTITION OF public.audit_log `+bounds); err != nil {
			return false, 0, fmt.Errorf("audit partitions: creating %s: %w", name, err)
		}
	} else {
		stmts := []string{
			`CREATE TABLE public.` + ident + ` (LIKE public.audit_log INCLUDING DEFAULTS INCLUDING CONSTRAINTS)`,
			`INSERT INTO public.` + ident + ` (id, actor_unique_id, action, target, detail, created_at, scope_channel_ids)
				SELECT id, actor_unique_id, action, target, detail, created_at, scope_channel_ids FROM public.audit_log_default
				WHERE created_at >= ` + auditQuoteLiteral(from.Format(time.RFC3339)) + ` AND created_at < ` + auditQuoteLiteral(to.Format(time.RFC3339)),
			`DELETE FROM public.audit_log_default WHERE created_at >= ` + auditQuoteLiteral(from.Format(time.RFC3339)) + ` AND created_at < ` + auditQuoteLiteral(to.Format(time.RFC3339)),
			`ALTER TABLE public.audit_log ATTACH PARTITION public.` + ident + ` ` + bounds,
		}
		for _, stmt := range stmts {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return false, 0, fmt.Errorf("audit partitions: moving default rows into %s: %w", name, err)
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `SELECT public.noxa_copy_table_acl('public.audit_log'::regclass, ('public.' || quote_ident($1))::regclass)`, name); err != nil {
		return false, 0, fmt.Errorf("audit partitions: granting %s: %w", name, err)
	}
	if err := tx.Commit(); err != nil {
		return false, 0, err
	}
	return true, moved, nil
}

// dropAuditPartitionsBefore drops monthly partitions whose whole range ends
// on or before cutoff. Each drop is audited in the same transaction.
func (s *Store) dropAuditPartitionsBefore(ctx context.Context, cutoff time.Time) ([]string, int64, error) {
	names, err := s.auditPartitionNames(ctx)
	if err != nil {
		return nil, 0, err
	}
	var dropped []string
	var total int64
	for _, name := range names {
		month, ok := parseAuditPartition(name)
		if !ok || month.AddDate(0, 1, 0).After(cutoff) {
			continue
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return dropped, total, err
		}
		var count int64
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM public.`+auditQuoteIdent(name)).Scan(&count); err != nil {
			_ = tx.Rollback()
			return dropped, total, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO public.audit_log (actor_unique_id, action, target, detail, scope_channel_ids)
			VALUES ('system:noxa-migrate', 'audit.partition_dropped', $1, $2, '{}')`,
			name, fmt.Sprintf("retention removed %d audit rows from %s", count, month.Format("2006-01"))); err != nil {
			_ = tx.Rollback()
			return dropped, total, fmt.Errorf("audit partitions: recording drop of %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, `DROP TABLE public.`+auditQuoteIdent(name)); err != nil {
			_ = tx.Rollback()
			return dropped, total, fmt.Errorf("audit partitions: dropping %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return dropped, total, err
		}
		dropped = append(dropped, name)
		total += count
	}
	return dropped, total, nil
}

func (s *Store) auditPartitionNames(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT c.relname FROM pg_catalog.pg_inherits i
		JOIN pg_catalog.pg_class c ON c.oid = i.inhrelid
		WHERE i.inhparent = to_regclass('public.audit_log') ORDER BY c.relname`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

func parseAuditPartition(name string) (time.Time, bool) {
	m := auditPartitionName.FindStringSubmatch(name)
	if m == nil {
		return time.Time{}, false
	}
	year, _ := strconv.Atoi(m[1])
	month, _ := strconv.Atoi(m[2])
	if month < 1 || month > 12 {
		return time.Time{}, false
	}
	return time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC), true
}

// AuditPartitionCoverage reports, for the serving process, whether next
// month's audit partition exists and how many rows sit in the DEFAULT
// partition. Both mean noxa-migrate should run soon; neither breaks inserts.
// ok is false when audit_log is not partitioned.
func (s *Store) AuditPartitionCoverage(ctx context.Context, now time.Time) (nextMonthReady bool, defaultRows int64, ok bool, err error) {
	if s == nil || s.db == nil {
		return false, 0, false, nil
	}
	partitioned, err := s.auditLogPartitioned(ctx)
	if err != nil || !partitioned {
		return false, 0, false, err
	}
	next := auditPartitionFor(monthStart(now).AddDate(0, 1, 0))
	err = s.db.QueryRowContext(ctx, `SELECT to_regclass('public.' || quote_ident($1)) IS NOT NULL,
		(SELECT count(*) FROM public.audit_log_default)`, next).Scan(&nextMonthReady, &defaultRows)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return nextMonthReady, defaultRows, err == nil, err
}

func auditQuoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func auditQuoteLiteral(value string) string {
	return `'` + strings.ReplaceAll(value, `'`, `''`) + `'`
}
