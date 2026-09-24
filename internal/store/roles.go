package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/lib/pq"

	"noxa/internal/authorization"
)

var errStoredRolePolicyInvalid = errors.New("stored role policy is invalid")

// RolePolicy reads one consistent snapshot, including the real channel tree.
// An absent configuration is distinct from an empty or inaccessible policy.
func (s *Store) RolePolicy(ctx context.Context) (_ authorization.RolePolicy, retErr error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return authorization.RolePolicy{}, err
	}
	defer rollbackRoleTx(tx, &retErr)
	p, err := readRolePolicy(ctx, tx, false)
	if err != nil {
		return authorization.RolePolicy{}, err
	}
	return p, tx.Commit()
}

// PrepareRolePolicy stages an explicitly selected owner, an empty @everyone
// baseline and unassigned starter roles. It does not activate the model, touch
// legacy grants or admit users.
// The operator must stop the server before the later reset/activation step.
func (s *Store) PrepareRolePolicy(ctx context.Context, ownerID int64) (_ authorization.RolePolicy, retErr error) {
	if ownerID < 1 {
		return authorization.RolePolicy{}, authorization.ErrRoleInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return authorization.RolePolicy{}, err
	}
	defer rollbackRoleTx(tx, &retErr)
	// There is no singleton row to lock during first-time preparation.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(723947203110)`); err != nil {
		return authorization.RolePolicy{}, err
	}
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM authorization_config)`).Scan(&exists); err != nil {
		return authorization.RolePolicy{}, err
	}
	if exists {
		return authorization.RolePolicy{}, authorization.ErrRoleConflict
	}
	// A configuration-free database can still contain an interrupted or manually
	// staged policy. Do not silently adopt its grants or member assignments.
	if _, err := tx.ExecContext(ctx, `LOCK TABLE auth_roles, auth_channel_access IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return authorization.RolePolicy{}, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM auth_roles) OR EXISTS(SELECT 1 FROM auth_channel_access)`).Scan(&exists); err != nil {
		return authorization.RolePolicy{}, err
	}
	if exists {
		return authorization.RolePolicy{}, authorization.ErrRoleConflict
	}
	if _, err := tx.ExecContext(ctx, `LOCK TABLE channels IN SHARE MODE`); err != nil {
		return authorization.RolePolicy{}, err
	}
	p := authorization.RolePolicy{Revision: 1, OwnerID: ownerID}
	if err := tx.QueryRowContext(ctx, `INSERT INTO auth_roles(name,position) VALUES('@everyone',0) RETURNING id`).Scan(&p.EveryoneID); err != nil {
		return authorization.RolePolicy{}, err
	}
	for _, role := range authorization.StarterRoles() {
		if err := tx.QueryRowContext(ctx, `INSERT INTO auth_roles(name,position) VALUES($1,$2) RETURNING id`, role.Name, role.Position).Scan(&role.ID); err != nil {
			return authorization.RolePolicy{}, err
		}
		for _, capability := range role.Permissions {
			if _, err := tx.ExecContext(ctx, `INSERT INTO auth_role_grants(role_id,capability) VALUES($1,$2)`, role.ID, capability); err != nil {
				return authorization.RolePolicy{}, err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO authorization_config(revision,owner_id,everyone_role_id) VALUES(1,$1,$2)`, ownerID, p.EveryoneID); err != nil {
		return authorization.RolePolicy{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO auth_channel_access(channel_id) SELECT id FROM channels`); err != nil {
		return authorization.RolePolicy{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO auth_channel_role_overrides(channel_id,role_id,capability,effect) SELECT id,$1,'view_channel','deny' FROM channels`, p.EveryoneID); err != nil {
		return authorization.RolePolicy{}, err
	}
	p, err = readRolePolicy(ctx, tx, false)
	if err != nil {
		return authorization.RolePolicy{}, err
	}
	if err := appendRoleAudit(ctx, tx, ownerID, "prepare", AuditDetail{Version: AuditDetailVersion, Revision: p.Revision, Channels: []int64{}, After: map[string]int64{"owner_id": p.OwnerID}}); err != nil {
		return authorization.RolePolicy{}, err
	}
	return p, tx.Commit()
}

// ChangeRolePolicy serializes policy writers on the configuration row. Permission
// checks, hierarchy comparisons, revision changes and auditing share this lock
// and transaction; a rejected or stale operation cannot partially apply.
func (s *Store) ChangeRolePolicy(ctx context.Context, actorID int64, change authorization.RoleChange) (_ authorization.RolePolicy, retErr error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return authorization.RolePolicy{}, err
	}
	defer rollbackRoleTx(tx, &retErr)
	before, err := readRolePolicy(ctx, tx, true)
	if err != nil {
		return authorization.RolePolicy{}, err
	}
	if change.Kind == authorization.RoleCreate {
		if change.Role.ID != 0 {
			return authorization.RolePolicy{}, authorization.ErrRoleInvalid
		}
		if err := tx.QueryRowContext(ctx, `SELECT nextval(pg_get_serial_sequence('auth_roles','id'))`).Scan(&change.Role.ID); err != nil {
			return authorization.RolePolicy{}, err
		}
	}
	after, err := authorization.ApplyRoleChange(before, actorID, change)
	if err != nil {
		return authorization.RolePolicy{}, err
	}
	if change.Kind == authorization.OwnerTransfer {
		var targetID int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 FOR KEY SHARE`, after.OwnerID).Scan(&targetID)
		if errors.Is(err, sql.ErrNoRows) {
			return authorization.RolePolicy{}, authorization.ErrRoleInvalid
		}
		if err != nil {
			return authorization.RolePolicy{}, err
		}
	}
	if err := writeRolePolicy(ctx, tx, before, after); err != nil {
		return authorization.RolePolicy{}, err
	}
	if err := appendRoleAudit(ctx, tx, actorID, string(change.Kind), roleAuditDetail(before, after, change)); err != nil {
		return authorization.RolePolicy{}, err
	}
	return after, tx.Commit()
}

func rollbackRoleTx(tx *sql.Tx, retErr *error) {
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		*retErr = errors.Join(*retErr, fmt.Errorf("rolling back role transaction: %w", err))
	}
}

func readRolePolicy(ctx context.Context, tx *sql.Tx, lock bool) (authorization.RolePolicy, error) {
	var p authorization.RolePolicy
	query := `SELECT revision,owner_id,everyone_role_id,COALESCE(default_member_role_id,0) FROM authorization_config WHERE singleton`
	if lock {
		query += ` FOR UPDATE`
	}
	err := tx.QueryRowContext(ctx, query).Scan(&p.Revision, &p.OwnerID, &p.EveryoneID, &p.DefaultMemberRoleID)
	if errors.Is(err, sql.ErrNoRows) {
		return p, authorization.ErrRolesNotConfigured
	}
	if err != nil {
		return p, err
	}
	roleIndex := map[int64]int{}
	err = roleRows(ctx, tx, `SELECT id,name,position,color,icon,hoist,mentionable FROM auth_roles ORDER BY position`, func(rows *sql.Rows) error {
		var r authorization.Role
		if err := rows.Scan(&r.ID, &r.Name, &r.Position, &r.Color, &r.Icon, &r.Hoist, &r.Mentionable); err != nil {
			return err
		}
		roleIndex[r.ID] = len(p.Roles)
		p.Roles = append(p.Roles, r)
		return nil
	})
	if err != nil {
		return p, err
	}
	err = roleRows(ctx, tx, `SELECT role_id,capability FROM auth_role_grants ORDER BY role_id,capability`, func(rows *sql.Rows) error {
		var id int64
		var c authorization.Capability
		if err := rows.Scan(&id, &c); err != nil {
			return err
		}
		i, ok := roleIndex[id]
		if !ok {
			return errors.New("grant references missing role")
		}
		p.Roles[i].Permissions = append(p.Roles[i].Permissions, c)
		return nil
	})
	if err != nil {
		return p, err
	}
	err = roleRows(ctx, tx, `SELECT user_id,role_id FROM auth_member_roles ORDER BY user_id,role_id`, func(rows *sql.Rows) error {
		var userID, roleID int64
		if err := rows.Scan(&userID, &roleID); err != nil {
			return err
		}
		if len(p.Members) == 0 || p.Members[len(p.Members)-1].UserID != userID {
			p.Members = append(p.Members, authorization.RoleMember{UserID: userID})
		}
		i := len(p.Members) - 1
		p.Members[i].RoleIDs = append(p.Members[i].RoleIDs, roleID)
		return nil
	})
	if err != nil {
		return p, err
	}
	channelIndex := map[int64]int{}
	err = roleRows(ctx, tx, `SELECT a.channel_id,COALESCE(c.parent_id,0),a.synced FROM auth_channel_access a JOIN channels c ON c.id=a.channel_id ORDER BY a.channel_id`, func(rows *sql.Rows) error {
		var ch authorization.ChannelPolicy
		if err := rows.Scan(&ch.ChannelID, &ch.ParentID, &ch.Synced); err != nil {
			return err
		}
		channelIndex[ch.ChannelID] = len(p.Channels)
		p.Channels = append(p.Channels, ch)
		return nil
	})
	if err != nil {
		return p, err
	}
	err = roleRows(ctx, tx, `SELECT channel_id,role_id,0,capability,effect FROM auth_channel_role_overrides UNION ALL SELECT channel_id,0,user_id,capability,effect FROM auth_channel_member_overrides ORDER BY 1,2,3,4`, func(rows *sql.Rows) error {
		var channelID int64
		var o authorization.RoleOverride
		if err := rows.Scan(&channelID, &o.RoleID, &o.UserID, &o.Capability, &o.Effect); err != nil {
			return err
		}
		i, ok := channelIndex[channelID]
		if !ok {
			return errors.New("override references missing channel policy")
		}
		p.Channels[i].Overrides = append(p.Channels[i].Overrides, o)
		return nil
	})
	if err != nil {
		return p, err
	}
	if _, err := authorization.NewRoleEvaluator(p); err != nil {
		return p, fmt.Errorf("reading authorization policy: %w: %w", errStoredRolePolicyInvalid, err)
	}
	return p, nil
}

func roleRows(ctx context.Context, tx *sql.Tx, query string, scan func(*sql.Rows) error) (retErr error) {
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, rows.Close()) }()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

func writeRolePolicy(ctx context.Context, tx *sql.Tx, before, after authorization.RolePolicy) error {
	oldRoles := map[int64]authorization.Role{}
	for _, r := range before.Roles {
		oldRoles[r.ID] = r
	}
	for _, r := range after.Roles {
		old, exists := oldRoles[r.ID]
		delete(oldRoles, r.ID)
		if exists && reflect.DeepEqual(old, r) {
			continue
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO auth_roles(id,name,position,color,icon,hoist,mentionable) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(id) DO UPDATE SET name=EXCLUDED.name,position=EXCLUDED.position,color=EXCLUDED.color,icon=EXCLUDED.icon,hoist=EXCLUDED.hoist,mentionable=EXCLUDED.mentionable`, r.ID, r.Name, r.Position, r.Color, r.Icon, r.Hoist, r.Mentionable)
		if err != nil {
			return err
		}
		if exists && reflect.DeepEqual(old.Permissions, r.Permissions) {
			continue
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM auth_role_grants WHERE role_id=$1`, r.ID); err != nil {
			return err
		}
		for _, c := range r.Permissions {
			if _, err := tx.ExecContext(ctx, `INSERT INTO auth_role_grants(role_id,capability) VALUES($1,$2)`, r.ID, c); err != nil {
				return err
			}
		}
	}
	for id := range oldRoles {
		if _, err := tx.ExecContext(ctx, `DELETE FROM auth_roles WHERE id=$1`, id); err != nil {
			return err
		}
	}
	oldMembers := map[int64][]int64{}
	for _, m := range before.Members {
		oldMembers[m.UserID] = m.RoleIDs
	}
	for _, m := range after.Members {
		old := oldMembers[m.UserID]
		delete(oldMembers, m.UserID)
		if reflect.DeepEqual(old, m.RoleIDs) {
			continue
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM auth_member_roles WHERE user_id=$1`, m.UserID); err != nil {
			return err
		}
		for _, id := range m.RoleIDs {
			if _, err := tx.ExecContext(ctx, `INSERT INTO auth_member_roles(user_id,role_id) VALUES($1,$2)`, m.UserID, id); err != nil {
				return err
			}
		}
	}
	for id := range oldMembers {
		if _, err := tx.ExecContext(ctx, `DELETE FROM auth_member_roles WHERE user_id=$1`, id); err != nil {
			return err
		}
	}
	oldChannels := map[int64]authorization.ChannelPolicy{}
	for _, ch := range before.Channels {
		oldChannels[ch.ChannelID] = ch
	}
	for _, ch := range after.Channels {
		if reflect.DeepEqual(oldChannels[ch.ChannelID], ch) {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO auth_channel_access(channel_id,synced) VALUES($1,$2) ON CONFLICT(channel_id) DO UPDATE SET synced=EXCLUDED.synced`, ch.ChannelID, ch.Synced); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM auth_channel_role_overrides WHERE channel_id=$1`, ch.ChannelID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM auth_channel_member_overrides WHERE channel_id=$1`, ch.ChannelID); err != nil {
			return err
		}
		for _, o := range ch.Overrides {
			if o.RoleID != 0 {
				if _, err := tx.ExecContext(ctx, `INSERT INTO auth_channel_role_overrides(channel_id,role_id,capability,effect) VALUES($1,$2,$3,$4)`, ch.ChannelID, o.RoleID, o.Capability, o.Effect); err != nil {
					return err
				}
			} else {
				if _, err := tx.ExecContext(ctx, `INSERT INTO auth_channel_member_overrides(channel_id,user_id,capability,effect) VALUES($1,$2,$3,$4)`, ch.ChannelID, o.UserID, o.Capability, o.Effect); err != nil {
					return err
				}
			}
		}
	}
	_, err := tx.ExecContext(ctx, `UPDATE authorization_config SET revision=$1,default_member_role_id=NULLIF($2,0),owner_id=$3 WHERE singleton`, after.Revision, after.DefaultMemberRoleID, after.OwnerID)
	return err
}

func appendRoleAudit(ctx context.Context, tx *sql.Tx, actorID int64, action string, entry AuditDetail) error {
	detail, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO audit_log(actor_unique_id,action,target,detail,scope_channel_ids) SELECT unique_id,$2,'authorization',$3,$4 FROM users WHERE id=$1`, actorID, "roles."+action, string(detail), pq.Array(entry.Channels))
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("role audit actor does not exist")
	}
	return nil
}
