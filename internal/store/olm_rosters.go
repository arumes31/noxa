package store

import (
	"context"
	"database/sql"
	"github.com/lib/pq"
	"noxa/internal/e2ee/admission"
	"sort"
)

func lockOlmRosters(ctx context.Context, tx *sql.Tx, principals []string) (map[string]int64, error) {
	ps := append([]string(nil), principals...)
	sort.Strings(ps)
	versions := map[string]int64{}
	for _, p := range ps {
		if _, ok := versions[p]; ok {
			continue
		}
		if _, e := tx.ExecContext(ctx, `INSERT INTO olm_rosters(principal) VALUES($1) ON CONFLICT DO NOTHING`, p); e != nil {
			return nil, e
		}
		var v int64
		if e := tx.QueryRowContext(ctx, `SELECT version FROM olm_rosters WHERE principal=$1 FOR UPDATE`, p).Scan(&v); e != nil {
			return nil, e
		}
		versions[p] = v
	}
	return versions, nil
}
func bumpOlmRoster(ctx context.Context, tx *sql.Tx, p string) error {
	_, e := tx.ExecContext(ctx, `UPDATE olm_rosters SET version=version+1 WHERE principal=$1`, p)
	return e
}
func lockMutationPrincipals(ctx context.Context, tx *sql.Tx, predicate string, args []any) ([]string, error) {
	// #nosec G202 -- predicate is a fixed SQL fragment from internal callers; values are bound parameters.
	rows, e := tx.QueryContext(ctx, `SELECT DISTINCT principal FROM user_devices WHERE `+predicate, args...)
	if e != nil {
		return nil, e
	}
	var ps []string
	for rows.Next() {
		var p string
		if e = rows.Scan(&p); e != nil {
			_ = rows.Close()
			return nil, e
		}
		ps = append(ps, p)
	}
	e = rows.Err()
	_ = rows.Close()
	if e != nil {
		return nil, e
	}
	_, e = lockOlmRosters(ctx, tx, ps)
	return ps, e
}
func bumpMutationPrincipals(ctx context.Context, tx *sql.Tx, ids []string) error {
	_, e := tx.ExecContext(ctx, `UPDATE olm_rosters SET version=version+1 WHERE principal IN (SELECT principal FROM user_devices WHERE device_id=ANY($1))`, pq.Array(ids))
	return e
}

type OlmRosterSnapshot struct {
	Revision int64              `json:"revision"`
	Epoch    int64              `json:"epoch"`
	Rosters  []admission.Roster `json:"rosters"`
	Targets  []admission.Target `json:"targets"`
}

func readOlmRoster(ctx context.Context, tx *sql.Tx, ps []string, source string) (OlmRosterSnapshot, error) {
	var out OlmRosterSnapshot
	versions, e := lockOlmRosters(ctx, tx, ps)
	if e != nil {
		return out, e
	}
	sort.Strings(ps)
	for _, p := range ps {
		out.Rosters = append(out.Rosters, admission.Roster{Principal: p, Version: versions[p]})
	}
	rows, e := tx.QueryContext(ctx, `SELECT d.principal,d.device_id,COALESCE(a.account_epoch,0),COALESCE(a.identity,'') FROM user_devices d LEFT JOIN olm_directory_state a USING(device_id) WHERE d.principal=ANY($1) AND d.revoked_at IS NULL AND (d.first_device OR octet_length(d.approval_signature)>0) ORDER BY d.device_id COLLATE "C" FOR UPDATE OF d`, pq.Array(ps))
	if e != nil {
		return out, e
	}
	defer func() { _ = rows.Close() }()
	counts := map[string]int{}
	for rows.Next() {
		var t admission.Target
		if e = rows.Scan(&t.Principal, &t.Device, &t.AccountEpoch, &t.Identity); e != nil {
			return out, e
		}
		if t.AccountEpoch == 0 {
			return out, ErrOlmExhausted
		}
		counts[t.Principal]++
		if counts[t.Principal] > 64 || len(out.Targets) >= admission.MaxTargets {
			return out, ErrOlmLimit
		}
		t.Local = t.Device == source
		out.Targets = append(out.Targets, t)
	}
	if e = rows.Err(); e != nil {
		return out, e
	}
	for _, p := range ps {
		if counts[p] == 0 {
			return out, ErrOlmAuthority
		}
	}
	return out, nil
}
