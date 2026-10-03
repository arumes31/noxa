package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/lib/pq"

	"noxa/internal/authorization"
)

// EnsureEchoChannel provisions the configured loopback channel before the
// serving authority and channel manager load their snapshots. It is trusted
// startup maintenance, never an online or user-facing mutation entry point.
// Existing channels retain their metadata and access rules. A newly created
// channel grants only viewing, joining and speaking to admitted users.
func (s *Store) EnsureEchoChannel(ctx context.Context, name string) (_ int64, retErr error) {
	if !(RoleChannelCreate{Name: name, ChannelType: 2}).valid() {
		return 0, authorization.ErrRoleInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer rollbackRoleTx(tx, &retErr)
	// Match policy writers' lock order and serialize concurrent startups.
	var active bool
	err = tx.QueryRowContext(ctx, `SELECT active FROM authorization_config WHERE singleton FOR UPDATE`).Scan(&active)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, authorization.ErrRolesNotConfigured
	}
	if err != nil {
		return 0, err
	}
	if !active {
		return 0, authorization.ErrRolesInactive
	}
	if _, err := tx.ExecContext(ctx, `LOCK TABLE channels IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return 0, err
	}
	before, err := readRolePolicy(ctx, tx, false)
	if err != nil {
		return 0, err
	}
	e, err := authorization.NewRoleEvaluator(before)
	if err != nil {
		return 0, err
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM channels`).Scan(&count); err != nil {
		return 0, err
	}
	if count != len(before.Channels) {
		return 0, authorization.ErrAuthorizationUnavailable
	}
	var id int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM channels WHERE name=$1 ORDER BY id LIMIT 1`, name).Scan(&id)
	if err == nil {
		return id, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	err = tx.QueryRowContext(ctx, `INSERT INTO channels(name,topic,channel_type,opus_bitrate,opus_fec)
		VALUES($1,'Shared microphone echo test. Wear headphones; others in this channel can hear you.',2,32000,TRUE) RETURNING id`, name).Scan(&id)
	if err != nil {
		return 0, err
	}
	after := e.Policy()
	channel := authorization.ChannelPolicy{ChannelID: id}
	for _, capability := range []authorization.Capability{authorization.ViewChannel, authorization.Connect, authorization.Speak} {
		channel.Overrides = append(channel.Overrides, authorization.RoleOverride{RoleID: after.EveryoneID, Capability: capability, Effect: authorization.Allow})
	}
	after.Channels = append(after.Channels, channel)
	after.Revision++
	if _, err := authorization.NewRoleEvaluator(after); err != nil {
		return 0, err
	}
	if err := writeRolePolicy(ctx, tx, before, after); err != nil {
		return 0, err
	}
	nextAudit, err := readChannelAuditState(ctx, tx, after, id)
	if err != nil {
		return 0, err
	}
	detail, err := json.Marshal(AuditDetail{Version: AuditDetailVersion, Revision: after.Revision, Channels: []int64{id}, After: nextAudit})
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_log(actor_unique_id,action,target,detail,scope_channel_ids)
		VALUES('system:echo-channel','roles.echo_channel_create','authorization',$1,$2)`, string(detail), pq.Array([]int64{id})); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}
