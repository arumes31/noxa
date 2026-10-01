package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/lib/pq"

	"noxa/internal/authorization"
)

// RoleChannelCreate is a prepared resource record, separate from the policy
// change so neither plaintext passwords nor password hashes enter its audit.
// The serving layer hashes passwords before entering the lifecycle barrier.
type RoleChannelCreate struct {
	Name            string
	Topic           string
	OrderIndex      int
	ChannelType     int
	MaxClients      int
	PasswordHash    string `json:"-"`
	OpusBitrate     int
	OpusFEC         bool
	OpusDTX         bool
	OpusStereo      bool
	Description     string
	SlowModeSeconds int
}

// RoleChannelSettings replaces editable metadata at an expected policy
// revision. Tree placement and access are separate, explicit operations.
type RoleChannelSettings struct {
	Name, Topic, Description                             string
	OrderIndex, MaxClients, SlowModeSeconds, OpusBitrate int
	OpusFEC, OpusDTX, OpusStereo                         bool
}

func (s RoleChannelSettings) Valid() bool {
	return RoleChannelCreate{Name: s.Name, ChannelType: 2, MaxClients: s.MaxClients, OpusBitrate: s.OpusBitrate}.valid() &&
		s.SlowModeSeconds >= 0 && s.SlowModeSeconds <= 2_147_483_647 && s.OrderIndex >= -2_147_483_648 && s.OrderIndex <= 2_147_483_647 &&
		validChannelText(s.Topic) && validChannelText(s.Description)
}

func (s *Store) EditRoleChannel(ctx context.Context, actorID, channelID, revision int64, settings RoleChannelSettings) (authorization.RolePolicy, error) {
	p, _, err := s.changeRoleChannel(ctx, actorID, authorization.ChannelTreeChange{
		Kind: authorization.ChannelEdit, ChannelID: channelID, ExpectedRevision: revision,
	}, nil, &settings, false)
	return p, err
}

func (r RoleChannelCreate) valid() bool {
	return strings.TrimSpace(r.Name) != "" && len(r.Name) <= 255 && validChannelText(r.Name) &&
		r.ChannelType >= 0 && r.ChannelType <= 2 && r.MaxClients >= 0 && r.MaxClients <= 2_147_483_647 &&
		r.OpusBitrate >= 0 && r.OpusBitrate <= 510_000 && r.OrderIndex >= -2_147_483_648 && r.OrderIndex <= 2_147_483_647 &&
		r.SlowModeSeconds >= 0 && r.SlowModeSeconds <= 2_147_483_647 && validChannelText(r.Topic) && validChannelText(r.Description)
}

// PostgreSQL text rejects NUL even though it is valid UTF-8. Reject it as
// malformed input before a storage error could close the serving authority.
func validChannelText(value string) bool {
	return utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

// ChangeRoleChannel commits the channel tree and access policy as one revision.
// This is storage, not a serving entry point: an online caller must hold the
// exclusive Authority/lifecycle barrier until state and revocation reconcile.
// Create requires zero request IDs; only this transaction allocates the ID.
func (s *Store) ChangeRoleChannel(ctx context.Context, actorID int64, change authorization.ChannelTreeChange, create *RoleChannelCreate) (_ authorization.RolePolicy, _ int64, retErr error) {
	return s.changeRoleChannel(ctx, actorID, change, create, nil, false)
}

// PruneTemporaryRoleChannel is trusted lifecycle maintenance, never a wire
// operation. The manager must hold Authority and its tree lock and revalidate
// that the exact timer is current and the channel has no live occupants.
// Storage independently enforces temporary/leaf status and audits the system
// action without impersonating the owner or permitting actor-zero user changes.
func (s *Store) PruneTemporaryRoleChannel(ctx context.Context, channelID, revision int64) (authorization.RolePolicy, error) {
	p, _, err := s.changeRoleChannel(ctx, 0, authorization.ChannelTreeChange{
		Kind: authorization.ChannelDelete, ChannelID: channelID, ExpectedRevision: revision,
	}, nil, nil, true)
	return p, err
}

func (s *Store) changeRoleChannel(ctx context.Context, actorID int64, change authorization.ChannelTreeChange, create *RoleChannelCreate, settings *RoleChannelSettings, cleanup bool) (_ authorization.RolePolicy, _ int64, retErr error) {
	if (change.Kind == authorization.ChannelEdit) != (settings != nil) || (settings != nil && !settings.Valid()) {
		return authorization.RolePolicy{}, 0, authorization.ErrRoleInvalid
	}
	if change.Kind == authorization.ChannelCreate {
		if create == nil || !create.valid() || change.ChannelID != 0 || change.Access.ChannelID != 0 || change.Temporary != (create.ChannelType == 0) {
			return authorization.RolePolicy{}, 0, authorization.ErrRoleInvalid
		}
	} else if create != nil {
		return authorization.RolePolicy{}, 0, authorization.ErrRoleInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return authorization.RolePolicy{}, 0, err
	}
	defer rollbackRoleTx(tx, &retErr)
	// Match all policy writers' lock order: configuration before resources.
	// The tree table lock excludes any older resource-only writer as well.
	var revision int64
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM authorization_config WHERE singleton FOR UPDATE`).Scan(&revision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return authorization.RolePolicy{}, 0, authorization.ErrRolesNotConfigured
		}
		return authorization.RolePolicy{}, 0, err
	}
	if _, err := tx.ExecContext(ctx, `LOCK TABLE channels IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return authorization.RolePolicy{}, 0, err
	}
	before, err := readRolePolicy(ctx, tx, false)
	if err != nil {
		return authorization.RolePolicy{}, 0, err
	}
	var channelCount int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM channels`).Scan(&channelCount); err != nil {
		return authorization.RolePolicy{}, 0, err
	}
	if channelCount != len(before.Channels) {
		return authorization.RolePolicy{}, 0, authorization.ErrAuthorizationUnavailable
	}
	if change.Kind == authorization.ChannelCreate {
		if err := tx.QueryRowContext(ctx, `SELECT nextval(pg_get_serial_sequence('channels','id'))`).Scan(&change.ChannelID); err != nil {
			return authorization.RolePolicy{}, 0, err
		}
		change.Access.ChannelID = change.ChannelID
	}
	var after authorization.RolePolicy
	if cleanup {
		after, err = pruneTemporaryRolePolicy(ctx, tx, before, change)
	} else {
		after, err = authorization.ApplyChannelTreeChange(before, actorID, change)
	}
	if err != nil {
		return authorization.RolePolicy{}, 0, err
	}
	if change.Kind == authorization.ChannelCreate {
		if err := validateChannelOverrideMembers(ctx, tx, change.Access.Overrides); err != nil {
			return authorization.RolePolicy{}, 0, err
		}
	}
	oldAudit, err := readChannelAuditState(ctx, tx, before, change.ChannelID)
	if err != nil {
		return authorization.RolePolicy{}, 0, err
	}
	switch change.Kind {
	case authorization.ChannelEdit:
		_, err = tx.ExecContext(ctx, `UPDATE channels SET name=$2,topic=$3,description=$4,order_index=$5,max_clients=NULLIF($6,0),slow_mode_seconds=$7,opus_bitrate=$8,opus_fec=$9,opus_dtx=$10,opus_stereo=$11 WHERE id=$1`,
			change.ChannelID, settings.Name, settings.Topic, settings.Description, settings.OrderIndex, settings.MaxClients, settings.SlowModeSeconds,
			settings.OpusBitrate, settings.OpusFEC, settings.OpusDTX, settings.OpusStereo)
	case authorization.ChannelCreate:
		_, err = tx.ExecContext(ctx, `INSERT INTO channels
			(id,parent_id,name,topic,order_index,channel_type,max_clients,password_hash,created_by,opus_bitrate,opus_fec,opus_dtx,opus_stereo,description,slow_mode_seconds)
			VALUES($1,NULLIF($2,0),$3,$4,$5,$6,NULLIF($7,0),NULLIF($8,''),$9,$10,$11,$12,$13,$14,$15)`,
			change.ChannelID, change.ParentID, create.Name, create.Topic, create.OrderIndex, create.ChannelType, create.MaxClients, create.PasswordHash,
			actorID, create.OpusBitrate, create.OpusFEC, create.OpusDTX, create.OpusStereo, create.Description, create.SlowModeSeconds)
	case authorization.ChannelMove:
		_, err = tx.ExecContext(ctx, `UPDATE channels SET parent_id=NULLIF($1,0),order_index=COALESCE($3,order_index) WHERE id=$2`, change.ParentID, change.ChannelID, change.OrderIndex)
	case authorization.ChannelDelete:
		_, err = tx.ExecContext(ctx, `DELETE FROM channels WHERE id=$1`, change.ChannelID)
	default:
		return authorization.RolePolicy{}, 0, authorization.ErrRoleInvalid
	}
	if err != nil {
		return authorization.RolePolicy{}, 0, err
	}
	if err := writeRolePolicy(ctx, tx, before, after); err != nil {
		return authorization.RolePolicy{}, 0, err
	}
	// Read back before commit. A foreign cascade or resource/policy mismatch
	// must roll back the entire change instead of publishing a partial tree.
	persisted, err := readRolePolicy(ctx, tx, false)
	if err != nil {
		return authorization.RolePolicy{}, 0, err
	}
	if len(persisted.Channels) != len(after.Channels) {
		return authorization.RolePolicy{}, 0, fmt.Errorf("channel policy/tree count mismatch")
	}
	nextAudit, err := readChannelAuditState(ctx, tx, persisted, change.ChannelID)
	if err != nil {
		return authorization.RolePolicy{}, 0, err
	}
	audit := AuditDetail{Version: AuditDetailVersion, Revision: after.Revision,
		Channels: auditChannelScopes(auditChannel(before, change.ChannelID), auditChannel(persisted, change.ChannelID)),
		Before:   oldAudit, After: nextAudit}
	if cleanup {
		err = appendRoleCleanupAudit(ctx, tx, audit)
	} else {
		err = appendRoleAudit(ctx, tx, actorID, string(change.Kind), audit)
	}
	if err != nil {
		return authorization.RolePolicy{}, 0, err
	}
	if err := tx.Commit(); err != nil {
		return authorization.RolePolicy{}, 0, err
	}
	return persisted, change.ChannelID, nil
}

func validateChannelOverrideMembers(ctx context.Context, tx *sql.Tx, overrides []authorization.RoleOverride) error {
	seen := map[int64]bool{}
	var ids []int64
	for _, override := range overrides {
		if override.UserID > 0 && !seen[override.UserID] {
			seen[override.UserID] = true
			ids = append(ids, override.UserID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	// Lock in stable order so a target cannot disappear between validation and
	// inserting its override. A missing subject is invalid input, not an
	// ambiguous storage failure that should close all authorization.
	rows, err := tx.QueryContext(ctx, `SELECT id FROM users WHERE id=ANY($1) ORDER BY id FOR KEY SHARE`, pq.Array(ids))
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	count := 0
	for rows.Next() {
		count++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if count != len(ids) {
		return authorization.ErrRoleInvalid
	}
	return nil
}

func pruneTemporaryRolePolicy(ctx context.Context, tx *sql.Tx, before authorization.RolePolicy, change authorization.ChannelTreeChange) (authorization.RolePolicy, error) {
	if change.ExpectedRevision != before.Revision {
		return authorization.RolePolicy{}, authorization.ErrRoleConflict
	}
	var eligible bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM channels c WHERE c.id=$1 AND c.channel_type=0 AND NOT EXISTS(SELECT 1 FROM channels child WHERE child.parent_id=c.id))`, change.ChannelID).Scan(&eligible); err != nil {
		return authorization.RolePolicy{}, err
	}
	if !eligible {
		return authorization.RolePolicy{}, authorization.ErrLifecycleUnchanged
	}
	before.Channels = slices.DeleteFunc(slices.Clone(before.Channels), func(ch authorization.ChannelPolicy) bool { return ch.ChannelID == change.ChannelID })
	before.Revision++
	e, err := authorization.NewRoleEvaluator(before)
	if err != nil {
		return authorization.RolePolicy{}, err
	}
	return e.Policy(), nil
}

func appendRoleCleanupAudit(ctx context.Context, tx *sql.Tx, entry AuditDetail) error {
	detail, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_log(actor_unique_id,action,target,detail,scope_channel_ids) VALUES('system:temporary-channel-cleanup','roles.channel_cleanup','authorization',$1,$2)`, string(detail), pq.Array(entry.Channels))
	return err
}
