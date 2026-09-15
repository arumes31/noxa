package store

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// AssignGuestGroup persists an authenticated guest and membership atomically.
// The caller must authorize the assignment and resolve an online identity.
func (s *Store) AssignGuestGroup(ctx context.Context, groupType string, groupID, channelID int64, uniqueID, nickname string, expiry time.Duration) (int64, error) {
	if uniqueID == "" || (groupType != "server" && groupType != "channel") {
		return 0, errors.New("invalid guest group assignment")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	const ensureUser = `INSERT INTO users (unique_id, nickname, created_at)
	    VALUES ($1, $2, NOW()) ON CONFLICT (unique_id) DO UPDATE
	    SET nickname = users.nickname
	    RETURNING id, COALESCE(password_hash, ''), COALESCE(public_key, '')`
	var userID int64
	var passwordHash, publicKey string
	if err := tx.QueryRowContext(ctx, ensureUser, uniqueID, nickname).Scan(&userID, &passwordHash, &publicKey); err != nil {
		return 0, fmt.Errorf("persisting guest identity: %w", err)
	}
	if passwordHash != "" || publicKey != "" {
		return 0, errors.New("identity already has account credentials; reconnect before assignment")
	}
	if groupType == "channel" {
		err = assignChannelGroupTx(ctx, tx, groupID, userID, channelID)
	} else {
		var expires any
		if expiry > 0 {
			expires = time.Now().Add(expiry)
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO server_group_members (user_id, server_group_id, expires_at)
		    VALUES ($1, $2, $3) ON CONFLICT (user_id, server_group_id)
		    DO UPDATE SET expires_at = EXCLUDED.expires_at`, userID, groupID, expires)
	}
	if err != nil {
		return 0, fmt.Errorf("assigning guest group: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return userID, nil
}
