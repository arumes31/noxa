package store

import (
	"context"
	"database/sql"
	"github.com/lib/pq"
	"noxa/internal/e2ee/admission"
	"noxa/internal/e2ee/olmdir"
	"slices"
	"sort"
	"strconv"
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

// lockMutationPrincipalsLimited locks the rosters of the principals selected by
// predicate, at most limit of them in principal order (0 = all).
func lockMutationPrincipalsLimited(ctx context.Context, tx *sql.Tx, predicate string, args []any, limit int) ([]string, error) {
	query := `SELECT DISTINCT principal FROM user_devices WHERE ` + predicate // #nosec G202 -- predicate is a fixed SQL fragment from internal callers; values are bound parameters.
	if limit > 0 {
		query += ` ORDER BY principal LIMIT ` + strconv.Itoa(limit)
	}
	rows, e := tx.QueryContext(ctx, query, args...)
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

func readOlmRoster(ctx context.Context, tx *sql.Tx, ps []string, a OlmSource) (OlmRosterSnapshot, error) {
	return readOlmRosterDevices(ctx, tx, ps, a, nil)
}

// Controls need only the source and the named recipient device to be ready.
// Keep both principals' roster locks/versions, and the same active-device and
// publication checks as complete chat fanout, without requiring idle siblings.
func readOlmControlRoster(ctx context.Context, tx *sql.Tx, a OlmSource, peer, target string) (OlmRosterSnapshot, error) {
	if !olmdir.ValidText(peer) || !olmdir.ValidText(target) || target == a.Device {
		return OlmRosterSnapshot{}, admission.ErrInvalid
	}
	ps := []string{a.Principal}
	if peer != a.Principal {
		ps = append(ps, peer)
	}
	out, err := readOlmRosterDevices(ctx, tx, ps, a, []string{a.Device, target})
	if err != nil {
		return out, err
	}
	if len(out.Targets) != 2 {
		return out, ErrOlmAuthority
	}
	for _, t := range out.Targets {
		if t.Local && t.Principal != a.Principal || !t.Local && (t.Device != target || t.Principal != peer) {
			return out, ErrOlmAuthority
		}
	}
	return out, nil
}

// olmUnpublishedIdle is how long an approved device without an Olm bundle may
// stay unseen before rosters stop requiring it (see readOlmRosterDevices).
const olmUnpublishedIdle = "7 days"

func readOlmRosterDevices(ctx context.Context, tx *sql.Tx, ps []string, a OlmSource, devices []string) (OlmRosterSnapshot, error) {
	var out OlmRosterSnapshot
	versions, e := lockOlmRosters(ctx, tx, ps)
	if e != nil {
		return out, e
	}
	sort.Strings(ps)
	for _, p := range ps {
		out.Rosters = append(out.Rosters, admission.Roster{Principal: p, Version: versions[p]})
	}
	// A device counts as published only while it has an unexpired publication
	// for this server: ClaimOlmKey claims from nothing else. An approved
	// device that is not published still counts while it was seen recently:
	// the roster then fails as exhausted (no state) or its claim does (expired
	// bundle), so a new or outdated device is waited for and its owner told to
	// set it up, never silently omitted. One idle for olmUnpublishedIdle will
	// not publish soon; listing it would block every conversation with its
	// owner forever (e.g. web sessions from before secure messaging, or an
	// idle web session whose bundle expired), so it is left out until it signs
	// in again and publishes. Trust is decided in Go (DMTrusted, the clients'
	// approval-chain rule), so every active device is read and locked.
	type row struct {
		d                 Device
		t                 admission.Target
		published, recent bool
	}
	byPrincipal := map[string][]row{}
	rows, e := tx.QueryContext(ctx, `SELECT d.principal,d.device_id,d.kind,d.ed25519_public,d.x25519_public,d.first_device,d.approved_by,d.approval_signature,COALESCE(a.account_epoch,0),COALESCE(a.identity,''),
 a.device_id IS NOT NULL AND EXISTS(SELECT 1 FROM olm_directory_publications p WHERE p.device_id=d.device_id AND p.server=$2 AND p.expires_at>now()),d.last_seen_at > now() - $3::interval
 FROM user_devices d LEFT JOIN olm_directory_state a USING(device_id) WHERE d.principal=ANY($1) AND d.revoked_at IS NULL ORDER BY d.device_id COLLATE "C" FOR UPDATE OF d`, pq.Array(ps), a.Server, olmUnpublishedIdle)
	if e != nil {
		return out, e
	}
	defer func() { _ = rows.Close() }()
	var order []string
	for rows.Next() {
		var r row
		if e = rows.Scan(&r.d.Principal, &r.d.DeviceID, &r.d.Kind, &r.d.Ed25519Public, &r.d.X25519Public, &r.d.FirstDevice, &r.d.ApprovedBy, &r.d.ApprovalSignature,
			&r.t.AccountEpoch, &r.t.Identity, &r.published, &r.recent); e != nil {
			return out, e
		}
		r.t.Principal, r.t.Device = r.d.Principal, r.d.DeviceID
		byPrincipal[r.d.Principal] = append(byPrincipal[r.d.Principal], r)
		order = append(order, r.d.DeviceID)
	}
	if e = rows.Err(); e != nil {
		return out, e
	}
	selected := map[string]admission.Target{}
	for p, rs := range byPrincipal {
		list := make([]Device, len(rs))
		for i, r := range rs {
			list[i] = r.d
		}
		trusted := map[string]bool{}
		for _, d := range DMTrusted(p, list) {
			trusted[d.DeviceID] = true
		}
		for _, r := range rs {
			if trusted[r.d.DeviceID] && (r.published || r.recent) && (len(devices) == 0 || slices.Contains(devices, r.d.DeviceID)) {
				selected[r.d.DeviceID] = r.t
			}
		}
	}
	counts := map[string]int{}
	for _, id := range order {
		t, ok := selected[id]
		if !ok {
			continue
		}
		if t.AccountEpoch == 0 {
			return out, ErrOlmExhausted
		}
		counts[t.Principal]++
		if counts[t.Principal] > 64 || len(out.Targets) >= admission.MaxTargets {
			return out, ErrOlmLimit
		}
		t.Local = t.Device == a.Device
		out.Targets = append(out.Targets, t)
	}
	for _, p := range ps {
		if counts[p] == 0 {
			return out, ErrOlmAuthority
		}
	}
	return out, nil
}
