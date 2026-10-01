package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"noxa/internal/authorization"
	"noxa/internal/safecast"
)

var (
	ErrRoleSetupAlreadyActive = errors.New("role authorization is already active")
	ErrRoleSetupNotReady      = errors.New("role authorization is not ready for activation")
)

// RoleActivationKeyFactory creates one newly wrapped random scope key. The
// setup caller owns the KEK; the store only persists opaque wrapped bytes.
type RoleActivationKeyFactory func() (kekID uint16, wrapped []byte, err error)

type RoleActivationResult struct {
	Policy        authorization.RolePolicy
	RotatedScopes int
}

// ActiveRolePolicy returns a serving policy only after activation committed.
// Prepared or interrupted setup state is deliberately unavailable at runtime.
func (s *Store) ActiveRolePolicy(ctx context.Context) (_ authorization.RolePolicy, retErr error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return authorization.RolePolicy{}, err
	}
	defer rollbackRoleTx(tx, &retErr)
	var active bool
	err = tx.QueryRowContext(ctx, `SELECT active FROM authorization_config WHERE singleton`).Scan(&active)
	if errors.Is(err, sql.ErrNoRows) {
		return authorization.RolePolicy{}, authorization.ErrRolesNotConfigured
	}
	if err != nil {
		return authorization.RolePolicy{}, err
	}
	if !active {
		return authorization.RolePolicy{}, authorization.ErrRolesInactive
	}
	p, err := readRolePolicy(ctx, tx, false)
	if err != nil {
		return authorization.RolePolicy{}, err
	}
	return p, tx.Commit()
}

// ActivatePreparedRolePolicy atomically rotates the current global/channel
// keys and enables a clean prepared policy. Legacy authority rows are retained
// only as inert rollback data; the roles-v1 runtime never reads them.
func (s *Store) ActivatePreparedRolePolicy(ctx context.Context, ownerUID string, newScopeKey RoleActivationKeyFactory) (_ RoleActivationResult, retErr error) {
	if strings.TrimSpace(ownerUID) == "" {
		return RoleActivationResult{}, ErrRoleSetupOwnerNotFound
	}
	if newScopeKey == nil {
		return RoleActivationResult{}, ErrRoleSetupNotReady
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RoleActivationResult{}, err
	}
	defer rollbackRoleTx(tx, &retErr)
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(723947203110)`); err != nil {
		return RoleActivationResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `LOCK TABLE users, channels, authorization_config,
		auth_roles, auth_role_grants, auth_member_roles, auth_channel_access,
		auth_channel_role_overrides, auth_channel_member_overrides,
		chat_scope_keys, chat_scope_seq IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return RoleActivationResult{}, err
	}
	var active bool
	err = tx.QueryRowContext(ctx, `SELECT active FROM authorization_config WHERE singleton FOR UPDATE`).Scan(&active)
	if errors.Is(err, sql.ErrNoRows) {
		return RoleActivationResult{}, authorization.ErrRolesNotConfigured
	}
	if err != nil {
		return RoleActivationResult{}, err
	}
	if active {
		return RoleActivationResult{}, ErrRoleSetupAlreadyActive
	}
	var ownerID int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE unique_id=$1`, ownerUID).Scan(&ownerID)
	if errors.Is(err, sql.ErrNoRows) {
		return RoleActivationResult{}, ErrRoleSetupOwnerNotFound
	}
	if err != nil {
		return RoleActivationResult{}, err
	}
	p, err := readRolePolicy(ctx, tx, false)
	if err != nil {
		return RoleActivationResult{}, err
	}
	if !cleanPreparedRolePolicy(p, ownerID) {
		return RoleActivationResult{}, ErrRoleSetupNotReady
	}
	var totalChannels int64
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM channels`).Scan(&totalChannels); err != nil {
		return RoleActivationResult{}, err
	}
	if int64(len(p.Channels)) != totalChannels {
		return RoleActivationResult{}, ErrRoleSetupNotReady
	}

	scopes := []int64{0}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM channels ORDER BY id`)
	if err != nil {
		return RoleActivationResult{}, err
	}
	for rows.Next() {
		var scope int64
		if err := rows.Scan(&scope); err != nil {
			_ = rows.Close()
			return RoleActivationResult{}, err
		}
		scopes = append(scopes, scope)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return RoleActivationResult{}, err
	}
	for _, scope := range scopes {
		kekID, wrapped, err := newScopeKey()
		if err != nil {
			return RoleActivationResult{}, err
		}
		if kekID == 0 || len(wrapped) == 0 {
			return RoleActivationResult{}, ErrRoleSetupNotReady
		}
		var allocated int64
		if err := tx.QueryRowContext(ctx, `INSERT INTO chat_scope_seq(scope_id,next_key_id) VALUES($1,2)
			ON CONFLICT(scope_id) DO UPDATE SET next_key_id=chat_scope_seq.next_key_id+1
			RETURNING next_key_id-1`, scope).Scan(&allocated); err != nil {
			return RoleActivationResult{}, err
		}
		keyID, err := safecast.Int64ToUint32(allocated)
		if err != nil {
			return RoleActivationResult{}, fmt.Errorf("allocating activation scope key for %d: %w", scope, err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE chat_scope_keys SET retired_at=NOW() WHERE scope_id=$1 AND retired_at IS NULL`, scope); err != nil {
			return RoleActivationResult{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO chat_scope_keys(scope_id,key_id,wrapped_key,kek_id) VALUES($1,$2,$3,$4)`,
			scope, int64(keyID), wrapped, int32(kekID)); err != nil {
			return RoleActivationResult{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE authorization_config SET active=TRUE WHERE singleton`); err != nil {
		return RoleActivationResult{}, err
	}
	if err := appendRoleAudit(ctx, tx, ownerID, "activate", AuditDetail{
		Version: AuditDetailVersion, Revision: p.Revision, Channels: []int64{},
		After: map[string]int64{"owner_id": ownerID, "rotated_scopes": int64(len(scopes))},
	}); err != nil {
		return RoleActivationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return RoleActivationResult{}, err
	}
	return RoleActivationResult{Policy: p, RotatedScopes: len(scopes)}, nil
}

func cleanPreparedRolePolicy(p authorization.RolePolicy, ownerID int64) bool {
	if p.OwnerID != ownerID || p.DefaultMemberRoleID != 0 || len(p.Members) != 0 || len(p.Roles) != 4 {
		return false
	}
	everyone := p.Roles[0]
	if everyone.ID != p.EveryoneID || everyone.Name != "@everyone" || everyone.Position != 0 ||
		everyone.Color != "" || everyone.Icon != "" || everyone.Hoist || len(everyone.Permissions) != 0 {
		return false
	}
	for index, expected := range authorization.StarterRoles() {
		actual := p.Roles[index+1]
		if actual.Name != expected.Name || actual.Position != expected.Position || actual.Color != expected.Color ||
			actual.Icon != expected.Icon || actual.Hoist != expected.Hoist || !sameCapabilities(actual.Permissions, expected.Permissions) {
			return false
		}
	}
	for _, channel := range p.Channels {
		if channel.Synced || len(channel.Overrides) != 1 {
			return false
		}
		override := channel.Overrides[0]
		if override.RoleID != p.EveryoneID || override.UserID != 0 ||
			override.Capability != authorization.ViewChannel || override.Effect != authorization.Deny {
			return false
		}
	}
	return true
}

func sameCapabilities(left, right []authorization.Capability) bool {
	left = append([]authorization.Capability(nil), left...)
	right = append([]authorization.Capability(nil), right...)
	slices.Sort(left)
	slices.Sort(right)
	return slices.Equal(left, right)
}
