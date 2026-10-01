package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/lib/pq"
)

var (
	ErrRoleSetupOwnerNotFound     = errors.New("selected owner unique ID does not exist")
	ErrRoleSetupSchemaUnavailable = errors.New("role setup inspection requires an existing compatible schema")
)

// RoleSetupIdentity is an operator-facing identity projection without credentials.
type RoleSetupIdentity struct {
	UserID   int64  `json:"user_id"`
	UniqueID string `json:"unique_id"`
	Nickname string `json:"nickname"`
}

// RoleSetupOwner reports credential presence, not successful authentication or recovery.
type RoleSetupOwner struct {
	RoleSetupIdentity
	PasswordConfigured    bool `json:"password_configured"`
	IdentityKeyConfigured bool `json:"identity_key_configured"`
}

// RoleSetupReport describes one database snapshot. It never certifies activation
// readiness, live server state, backup integrity or owner credential possession.
type RoleSetupReport struct {
	State             string           `json:"state"`
	Configured        bool             `json:"configured"`
	Model             string           `json:"model"`
	Active            bool             `json:"active"`
	Revision          int64            `json:"revision"`
	ConfiguredOwnerID int64            `json:"configured_owner_id"`
	Owner             RoleSetupOwner   `json:"selected_owner"`
	Counts            map[string]int64 `json:"counts"`
	UncoveredChannels int64            `json:"uncovered_channels"`
	Issues            []string         `json:"issues"`
}

// InspectRoleSetup reads setup prerequisites without migrating, preparing,
// changing grants or activating a model. Owner selection is exact unique ID only.
func (s *Store) InspectRoleSetup(ctx context.Context, ownerUID string) (_ RoleSetupReport, retErr error) {
	if strings.TrimSpace(ownerUID) == "" {
		return RoleSetupReport{}, ErrRoleSetupOwnerNotFound
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return RoleSetupReport{}, err
	}
	defer rollbackRoleTx(tx, &retErr)
	report, err := inspectRoleSetup(ctx, tx, ownerUID)
	if err != nil {
		var dbErr *pq.Error
		if errors.As(err, &dbErr) && (dbErr.Code == "42P01" || dbErr.Code == "42703") {
			return RoleSetupReport{}, errors.Join(ErrRoleSetupSchemaUnavailable, err)
		}
		return RoleSetupReport{}, err
	}
	return report, tx.Commit()
}

func inspectRoleSetup(ctx context.Context, tx *sql.Tx, ownerUID string) (RoleSetupReport, error) {
	r := RoleSetupReport{State: "unprepared", Counts: map[string]int64{}, Issues: []string{}}
	err := tx.QueryRowContext(ctx, `SELECT id,unique_id,COALESCE(nickname,''),
		COALESCE(password_hash,'')<>'',COALESCE(public_key,'')<>'' FROM users WHERE unique_id=$1`, ownerUID).
		Scan(&r.Owner.UserID, &r.Owner.UniqueID, &r.Owner.Nickname, &r.Owner.PasswordConfigured, &r.Owner.IdentityKeyConfigured)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrRoleSetupOwnerNotFound
	}
	if err != nil {
		return r, err
	}
	err = tx.QueryRowContext(ctx, `SELECT model,active,revision,owner_id FROM authorization_config WHERE singleton`).
		Scan(&r.Model, &r.Active, &r.Revision, &r.ConfiguredOwnerID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return r, err
	}
	r.Configured = err == nil
	// Table names are a fixed inspection inventory, never operator input.
	for _, table := range []string{
		"users", "channels", "auth_roles",
		"auth_role_grants", "auth_member_roles", "auth_channel_access",
		"auth_channel_role_overrides", "auth_channel_member_overrides",
	} {
		var count int64
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM `+pq.QuoteIdentifier(table)).Scan(&count); err != nil {
			return r, err
		}
		r.Counts[table] = count
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM channels c WHERE NOT EXISTS (SELECT 1 FROM auth_channel_access a WHERE a.channel_id=c.id)`).Scan(&r.UncoveredChannels); err != nil {
		return r, err
	}
	if !r.Configured {
		if r.Counts["auth_roles"] != 0 || r.Counts["auth_channel_access"] != 0 {
			r.State = "inconsistent"
			r.Issues = append(r.Issues, "orphan_staging")
		}
		return r, nil
	}
	r.State = "prepared_inactive"
	if r.Active {
		r.State = "active"
	}
	if r.UncoveredChannels != 0 {
		r.State = "inconsistent"
		r.Issues = append(r.Issues, "channels_without_policy")
	}
	_, err = readRolePolicy(ctx, tx, false)
	if errors.Is(err, errStoredRolePolicyInvalid) {
		r.State = "inconsistent"
		r.Issues = append(r.Issues, "invalid_role_policy")
	} else if err != nil {
		return r, err
	}
	return r, nil
}
