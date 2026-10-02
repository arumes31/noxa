package store

import (
	"context"
	"database/sql"
	"errors"

	"noxa/internal/authorization"
)

type roleGuestIdentity struct {
	uniqueID, publicKey, nickname string
	userID                        int64
}

// AssignGuestRoles enrolls only the identity explicitly selected by a role
// manager. The native server must supply a live, key-verified identity. Creation,
// normal role/hierarchy checks and the audit commit share one transaction: a
// rejected grant leaves no account behind and no default roles are assigned.
func (s *Store) AssignGuestRoles(ctx context.Context, actorID int64, uniqueID, publicKey, nickname string, change authorization.RoleChange) (authorization.RolePolicy, int64, error) {
	if change.Kind != authorization.MemberRolesSet || change.UserID != 0 || change.MemberUniqueID != uniqueID ||
		uniqueID == "" || publicKey == "" || len(change.RoleIDs) == 0 {
		return authorization.RolePolicy{}, 0, authorization.ErrRoleInvalid
	}
	guest := &roleGuestIdentity{uniqueID: uniqueID, publicKey: publicKey, nickname: nickname}
	change.MemberUniqueID = ""
	policy, err := s.changeRolePolicy(ctx, actorID, change, guest)
	if err != nil {
		return authorization.RolePolicy{}, 0, err
	}
	return policy, guest.userID, nil
}

func enrollRoleGuest(ctx context.Context, tx *sql.Tx, guest *roleGuestIdentity, change *authorization.RoleChange) error {
	// Do not adopt an existing account: a fresh roster must select it by ID.
	err := tx.QueryRowContext(ctx, `INSERT INTO users(unique_id,public_key,identity_display_name)
		VALUES($1,$2,$3) ON CONFLICT(unique_id) DO NOTHING RETURNING id`,
		guest.uniqueID, guest.publicKey, guest.nickname).Scan(&guest.userID)
	if errors.Is(err, sql.ErrNoRows) {
		return authorization.ErrRoleInvalid
	}
	change.UserID = guest.userID
	return err
}
