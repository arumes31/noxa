package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/lib/pq"
	"noxa/internal/authorization"
)

// EnsureHomeChannels runs before serving; no live memberships exist yet.
func (s *Store) EnsureHomeChannels(ctx context.Context) (authorization.RolePolicy, error) {
	p, err := s.reconcileHomeChannels(ctx, 0, nil, true)
	if errors.Is(err, authorization.ErrLifecycleUnchanged) {
		return s.RolePolicy(ctx)
	}
	return p, err
}

// ReconcilePublicChannels is trusted maintenance. The caller holds Authority
// exclusively and the channel tree lock while collecting occupied IDs and
// committing. It must release tree locks before Authority reconciles state.
func (s *Store) ReconcilePublicChannels(ctx context.Context, revision int64, occupied map[int64]bool) (authorization.RolePolicy, error) {
	return s.reconcileHomeChannels(ctx, revision, occupied, false)
}

type homeRoom struct {
	id, parent int64
	name, kind string
}

func (s *Store) reconcileHomeChannels(ctx context.Context, revision int64, occupied map[int64]bool, startup bool) (_ authorization.RolePolicy, retErr error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return authorization.RolePolicy{}, err
	}
	defer rollbackRoleTx(tx, &retErr)
	var active bool
	var current int64
	if err = tx.QueryRowContext(ctx, `SELECT active,revision FROM authorization_config WHERE singleton FOR UPDATE`).Scan(&active, &current); err != nil {
		return authorization.RolePolicy{}, err
	}
	if !active {
		return authorization.RolePolicy{}, authorization.ErrRolesInactive
	}
	if !startup && revision != current {
		return authorization.RolePolicy{}, authorization.ErrRoleConflict
	}
	if _, err = tx.ExecContext(ctx, `LOCK TABLE channels IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return authorization.RolePolicy{}, err
	}
	before, err := readRolePolicy(ctx, tx, false)
	if err != nil {
		return authorization.RolePolicy{}, err
	}
	evaluator, err := authorization.NewRoleEvaluator(before)
	if err != nil {
		return authorization.RolePolicy{}, err
	}
	after := evaluator.Policy()
	var rooms []homeRoom
	rows, err := tx.QueryContext(ctx, `SELECT id,COALESCE(parent_id,0),name,system_kind FROM channels ORDER BY id`)
	if err != nil {
		return authorization.RolePolicy{}, err
	}
	for rows.Next() {
		var r homeRoom
		if err = rows.Scan(&r.id, &r.parent, &r.name, &r.kind); err != nil {
			_ = rows.Close()
			return authorization.RolePolicy{}, err
		}
		rooms = append(rooms, r)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return authorization.RolePolicy{}, err
	}
	if len(rooms) != len(before.Channels) {
		return authorization.RolePolicy{}, authorization.ErrAuthorizationUnavailable
	}
	var home int64
	for _, r := range rooms {
		if r.kind == "home" {
			home = r.id
		}
	}
	changed := false
	var affected []int64
	if home == 0 && startup {
		for _, r := range rooms {
			if r.parent == 0 && r.name == "Lobby" {
				home = r.id
				break
			}
		}
		if home == 0 {
			if err = tx.QueryRowContext(ctx, `INSERT INTO channels(name,channel_type,system_kind,is_default) VALUES('Lobby',2,'home',TRUE) RETURNING id`).Scan(&home); err != nil {
				return authorization.RolePolicy{}, err
			}
			after.Channels = append(after.Channels, authorization.ChannelPolicy{ChannelID: home, SystemKind: "home"})
		} else {
			if _, err = tx.ExecContext(ctx, `UPDATE channels SET system_kind='home',channel_type=2,text_only=FALSE,parent_id=NULL,password_hash=NULL,is_default=TRUE WHERE id=$1`, home); err != nil {
				return authorization.RolePolicy{}, err
			}
			for i := range after.Channels {
				if after.Channels[i].ChannelID == home {
					after.Channels[i].SystemKind = "home"
				}
			}
		}
		affected = append(affected, home)
		changed = true
	}
	if home == 0 {
		return authorization.RolePolicy{}, authorization.ErrAuthorizationUnavailable
	}
	// Adopt only the existing conventionally named direct Public rooms. Other
	// manually created channels are never candidates for automatic deletion.
	if startup {
		for i := range rooms {
			r := &rooms[i]
			n, parseErr := strconv.Atoi(strings.TrimPrefix(r.name, "Public"))
			if r.parent != home || r.kind != "" || parseErr != nil || n < 1 || r.name != fmt.Sprintf("Public%d", n) {
				continue
			}
			result, e := tx.ExecContext(ctx, `UPDATE channels SET system_kind='public',channel_type=2 WHERE id=$1 AND NOT text_only AND COALESCE(password_hash,'')='' AND NOT EXISTS(SELECT 1 FROM channels child WHERE child.parent_id=$1)`, r.id)
			if e != nil {
				return authorization.RolePolicy{}, e
			}
			count, e := result.RowsAffected()
			if e != nil {
				return authorization.RolePolicy{}, e
			}
			if count == 0 {
				continue
			}
			r.kind = "public"
			changed = true
			affected = append(affected, r.id)
			for j := range after.Channels {
				if after.Channels[j].ChannelID == r.id {
					after.Channels[j].SystemKind = "public"
				}
			}
		}
	}
	var empty []homeRoom
	names := map[string]bool{}
	for _, r := range rooms {
		if r.parent == home {
			names[r.name] = true
		}
		if r.kind == "public" && r.parent == home && !occupied[r.id] {
			empty = append(empty, r)
		}
	}
	sort.Slice(empty, func(i, j int) bool {
		a, _ := strconv.Atoi(strings.TrimPrefix(empty[i].name, "Public"))
		b, _ := strconv.Atoi(strings.TrimPrefix(empty[j].name, "Public"))
		if a != b {
			return a < b
		}
		return empty[i].id < empty[j].id
	})
	for len(empty) > 2 {
		r := empty[len(empty)-1]
		empty = empty[:len(empty)-1]
		result, deleteErr := tx.ExecContext(ctx, `DELETE FROM channels WHERE id=$1 AND system_kind='public' AND NOT EXISTS(SELECT 1 FROM channels child WHERE child.parent_id=$1)`, r.id)
		if deleteErr != nil {
			return authorization.RolePolicy{}, deleteErr
		}
		removed, deleteErr := result.RowsAffected()
		if deleteErr != nil {
			return authorization.RolePolicy{}, deleteErr
		}
		if removed != 1 {
			return authorization.RolePolicy{}, authorization.ErrAuthorizationUnavailable
		}
		kept := after.Channels[:0]
		for _, ch := range after.Channels {
			if ch.ChannelID != r.id {
				kept = append(kept, ch)
			}
		}
		after.Channels = kept
		delete(names, r.name)
		affected = append(affected, r.id)
		changed = true
	}
	for len(empty) < 2 {
		number := 1
		for names[fmt.Sprintf("Public%d", number)] {
			number++
		}
		name := fmt.Sprintf("Public%d", number)
		var id int64
		if err = tx.QueryRowContext(ctx, `INSERT INTO channels(parent_id,name,channel_type,system_kind,order_index,opus_bitrate,opus_fec) VALUES($1,$2,2,'public',$3,32000,TRUE) RETURNING id`, home, name, number).Scan(&id); err != nil {
			return authorization.RolePolicy{}, err
		}
		channel := authorization.ChannelPolicy{ChannelID: id, ParentID: home, SystemKind: "public"}
		for _, capability := range []authorization.Capability{authorization.ViewChannel, authorization.Connect, authorization.Speak, authorization.ShareCamera, authorization.ShareScreen, authorization.ReadHistory, authorization.SendMessages} {
			channel.Overrides = append(channel.Overrides, authorization.RoleOverride{RoleID: after.EveryoneID, Capability: capability, Effect: authorization.Allow})
		}
		after.Channels = append(after.Channels, channel)
		empty = append(empty, homeRoom{id: id, name: name})
		names[name] = true
		affected = append(affected, id)
		changed = true
	}
	if !changed {
		return authorization.RolePolicy{}, authorization.ErrLifecycleUnchanged
	}
	after.Revision++
	if _, err = authorization.NewRoleEvaluator(after); err != nil {
		return authorization.RolePolicy{}, err
	}
	if err = writeRolePolicy(ctx, tx, before, after); err != nil {
		return authorization.RolePolicy{}, err
	}
	persisted, err := readRolePolicy(ctx, tx, false)
	if err != nil {
		return authorization.RolePolicy{}, err
	}
	if len(persisted.Channels) != len(after.Channels) {
		return authorization.RolePolicy{}, authorization.ErrAuthorizationUnavailable
	}
	detail, _ := json.Marshal(map[string]any{"revision": after.Revision, "channels": affected})
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit_log(actor_unique_id,action,target,detail,scope_channel_ids) VALUES('system:home-channels','roles.public_pool_reconcile','authorization',$1,$2)`, string(detail), pq.Array(affected)); err != nil {
		return authorization.RolePolicy{}, err
	}
	if err = tx.Commit(); err != nil {
		return authorization.RolePolicy{}, err
	}
	return persisted, nil
}
