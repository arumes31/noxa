package store

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"noxa/internal/netproto"
)

var ErrChannelInviteUnavailable = errors.New("channel invitation unavailable")

// Only a SHA-256 digest is persisted. A regenerated secret is returned once.
func (s *Store) ChannelInvite(ctx context.Context, r netproto.ChannelInviteRequest, hash []byte) (netproto.ChannelInviteResult, error) {
	out := netproto.ChannelInviteResult{Action: r.Action, ChannelID: r.ChannelID}
	if !r.Valid() {
		return out, ErrChannelInviteUnavailable
	}
	switch r.Action {
	case "regenerate":
		if len(hash) != 32 {
			return out, ErrChannelInviteUnavailable
		}
		_, err := s.db.ExecContext(ctx, `INSERT INTO channel_invites(channel_id,token_hash,access) VALUES($1,$2,$3)
            ON CONFLICT(channel_id) DO UPDATE SET token_hash=EXCLUDED.token_hash,access=EXCLUDED.access,created_at=now()`, r.ChannelID, hash, r.Access)
		out.Active, out.Access = err == nil, r.Access
		return out, err
	case "revoke":
		_, err := s.db.ExecContext(ctx, `DELETE FROM channel_invites WHERE channel_id=$1`, r.ChannelID)
		return out, err
	}
	var stored []byte
	err := s.db.QueryRowContext(ctx, `SELECT token_hash,access FROM channel_invites WHERE channel_id=$1`, r.ChannelID).Scan(&stored, &out.Access)
	if errors.Is(err, sql.ErrNoRows) {
		if r.Action == "get" {
			return out, nil
		}
		return out, ErrChannelInviteUnavailable
	}
	if err != nil {
		return out, err
	}
	if r.Action == "resolve" && (len(hash) != 32 || subtle.ConstantTimeCompare(stored, hash) != 1) {
		return netproto.ChannelInviteResult{}, ErrChannelInviteUnavailable
	}
	out.Active = true
	return out, nil
}
