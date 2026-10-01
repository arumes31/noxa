package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// RegisterUserWithDefaultRole creates one account and, when roles-v1 is active,
// assigns the configured default member role in the same transaction. The
// authorization row is locked against concurrent default-role changes. The
// serving admission path refreshes Authority before a newly registered account
// can use this assignment; registration itself does not advance policy revision.
func (s *Store) RegisterUserWithDefaultRole(ctx context.Context, uniqueID, nickname, passwordHash, publicKey string) (_ int64, retErr error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("beginning user registration: %w", err)
	}
	defer func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			retErr = errors.Join(retErr, fmt.Errorf("rolling back user registration: %w", err))
		}
	}()
	var active bool
	var defaultRole sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT active,default_member_role_id FROM authorization_config WHERE singleton FOR SHARE`).Scan(&active, &defaultRole)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("reading registration role policy: %w", err)
	}
	var userID int64
	err = tx.QueryRowContext(ctx, `INSERT INTO users(unique_id,nickname,password_hash,public_key,created_at)
		VALUES($1,$2,$3,$4,NOW()) RETURNING id`, uniqueID, nickname, passwordHash, publicKey).Scan(&userID)
	if err != nil {
		return 0, err
	}
	if active && defaultRole.Valid {
		if _, err := tx.ExecContext(ctx, `INSERT INTO auth_member_roles(user_id,role_id) VALUES($1,$2)`, userID, defaultRole.Int64); err != nil {
			return 0, fmt.Errorf("assigning default member role: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("committing user registration: %w", err)
	}
	return userID, nil
}
