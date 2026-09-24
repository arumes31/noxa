package store

import (
	"context"
	"database/sql"
	"strings"

	"noxa/internal/authorization"
)

// RoleMembers uses the same snapshot for authorization and returned membership.
// The bounded roster includes offline identities, never addresses or auth data.
func (s *Store) RoleMembers(ctx context.Context, actorID int64, query authorization.MemberQuery) (_ authorization.MemberPage, retErr error) {
	if query.AfterID < 0 || query.ChannelID < 0 || len(query.Search) > 100 {
		return authorization.MemberPage{}, authorization.ErrRoleInvalid
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return authorization.MemberPage{}, err
	}
	defer rollbackRoleTx(tx, &retErr)
	p, err := readRolePolicy(ctx, tx, false)
	if err != nil {
		return authorization.MemberPage{}, err
	}
	e, err := authorization.NewRoleEvaluator(p)
	if err != nil {
		return authorization.MemberPage{}, err
	}
	capability := authorization.ManageRoles
	if query.ChannelID != 0 {
		capability = authorization.ManageChannelAccess
	}
	if actorID < 1 || !e.Evaluate(actorID, query.ChannelID, capability).Allowed {
		return authorization.MemberPage{}, authorization.ErrRoleForbidden
	}
	if p.Revision != query.ExpectedRevision {
		return authorization.MemberPage{}, authorization.ErrRoleConflict
	}
	pattern := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(strings.TrimSpace(query.Search)) + "%"
	rows, err := tx.QueryContext(ctx, `SELECT id,unique_id,COALESCE(nickname,'') FROM users WHERE id>$1 AND (nickname ILIKE $2 OR unique_id ILIKE $2) ORDER BY id LIMIT 101`, query.AfterID, pattern)
	if err != nil {
		return authorization.MemberPage{}, err
	}
	page := authorization.MemberPage{Revision: p.Revision, Entries: []authorization.MemberIdentity{}}
	for rows.Next() {
		var member authorization.MemberIdentity
		if err := rows.Scan(&member.UserID, &member.UniqueID, &member.Nickname); err != nil {
			_ = rows.Close()
			return page, err
		}
		member.Manageable = e.CanManageMember(actorID, member.UserID)
		member.RoleIDs = []int64{}
		for _, m := range p.Members {
			if m.UserID == member.UserID {
				member.RoleIDs = m.RoleIDs
				break
			}
		}
		page.Entries = append(page.Entries, member)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return page, err
	}
	if err := rows.Close(); err != nil {
		return page, err
	}
	if len(page.Entries) > 100 {
		page.More = true
		page.Entries = page.Entries[:100]
	}
	return page, tx.Commit()
}
