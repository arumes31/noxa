//go:build integration

package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"noxa/internal/authorization"
)

func TestRoleSchemaOmitsRetiredPermissions(t *testing.T) {
	s, _, _ := roleTestStore(t)
	for _, name := range []string{
		"server_groups", "channel_groups", "permissions",
		"server_group_permissions", "channel_group_permissions", "client_permissions",
		"channel_client_permissions", "channel_permissions", "server_group_members",
		"channel_group_members", "tokens", "channel_group_auto_rules",
	} {
		var exists bool
		if err := s.db.QueryRowContext(t.Context(), `SELECT to_regclass('public.' || $1) IS NOT NULL`, name).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists {
			t.Errorf("retired table %q still exists", name)
		}
	}
	for _, column := range []struct{ table, name string }{
		{"users", "is_admin"},
		{"channels", "needed_join_power"},
		{"channels", "inherit_permissions"},
	} {
		var exists bool
		if err := s.db.QueryRowContext(t.Context(), `SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema='public' AND table_name=$1 AND column_name=$2
		)`, column.table, column.name).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists {
			t.Errorf("retired %s.%s column still exists", column.table, column.name)
		}
	}
}

func TestRoleSetupInspectionExactOwnerAndSafeProjection(t *testing.T) {
	s, owner, member := roleTestStore(t)
	if _, err := s.db.ExecContext(t.Context(), `UPDATE users SET nickname='role-owner',password_hash='secret-password-canary',public_key='secret-key-canary' WHERE id=$1`, member); err != nil {
		t.Fatal(err)
	}
	report, err := s.InspectRoleSetup(t.Context(), "role-owner")
	if err != nil {
		t.Fatal(err)
	}
	if report.Owner.UserID != owner || report.Owner.PasswordConfigured || report.Owner.IdentityKeyConfigured || report.State != "unprepared" {
		t.Fatalf("owner was not selected by exact unique ID: %+v", report)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "secret-") {
		t.Fatal("credential material leaked into report")
	}
	if _, err := s.InspectRoleSetup(t.Context(), "Role Owner"); !errors.Is(err, ErrRoleSetupOwnerNotFound) {
		t.Fatalf("nickname fallback or unknown owner accepted: %v", err)
	}
	var configCount, auditCount int
	if err := s.db.QueryRowContext(t.Context(), `SELECT (SELECT count(*) FROM authorization_config),(SELECT count(*) FROM audit_log)`).Scan(&configCount, &auditCount); err != nil {
		t.Fatal(err)
	}
	if configCount != 0 || auditCount != 0 {
		t.Fatal("inspection prepared a policy or wrote an audit")
	}
}

func TestRoleSetupInspectionReportsStateAndIncompleteCoverage(t *testing.T) {
	s, owner, _ := roleTestStore(t)
	if _, err := s.PrepareRolePolicy(t.Context(), owner); err != nil {
		t.Fatal(err)
	}
	for _, active := range []bool{false, true} {
		if _, err := s.db.ExecContext(t.Context(), `UPDATE authorization_config SET active=$1`, active); err != nil {
			t.Fatal(err)
		}
		report, err := s.InspectRoleSetup(t.Context(), "role-owner")
		if err != nil {
			t.Fatal(err)
		}
		want := "prepared_inactive"
		if active {
			want = "active"
		}
		if report.State != want || report.Active != active || report.ConfiguredOwnerID != owner || report.Revision != 1 || report.UncoveredChannels != 0 {
			t.Fatalf("incorrect stored state: %+v", report)
		}
	}
	if _, err := s.db.ExecContext(t.Context(), `INSERT INTO channels(name,channel_type) VALUES('Created after staging',2)`); err != nil {
		t.Fatal(err)
	}
	report, err := s.InspectRoleSetup(t.Context(), "role-owner")
	if err != nil || report.UncoveredChannels != 1 || report.Counts["channels"] != 2 {
		t.Fatalf("channel coverage not reported: %+v %v", report, err)
	}
}

func TestRoleSetupInspectionRefusesMissingSchemaWithoutMigration(t *testing.T) {
	s := testScratchStore(t)
	if _, err := s.InspectRoleSetup(t.Context(), "owner"); err == nil {
		t.Fatal("missing schema accepted")
	}
	var untouched bool
	if err := s.db.QueryRowContext(t.Context(), `SELECT to_regclass('public.users') IS NULL AND to_regclass('public.authorization_config') IS NULL`).Scan(&untouched); err != nil || !untouched {
		t.Fatalf("inspection created schema: %v %v", untouched, err)
	}
}

func TestRoleSetupInspectionReportsInvalidStoredPolicy(t *testing.T) {
	for _, invalid := range []string{"role color", "uncovered parent"} {
		t.Run(invalid, func(t *testing.T) {
			s, owner, _ := roleTestStore(t)
			if _, err := s.PrepareRolePolicy(t.Context(), owner); err != nil {
				t.Fatal(err)
			}
			if invalid == "role color" {
				if _, err := s.db.ExecContext(t.Context(), `UPDATE auth_roles SET color='invalid'`); err != nil {
					t.Fatal(err)
				}
			} else {
				var parent int64
				if err := s.db.QueryRowContext(t.Context(), `INSERT INTO channels(name,channel_type) VALUES('Unstaged parent',2) RETURNING id`).Scan(&parent); err != nil {
					t.Fatal(err)
				}
				if _, err := s.db.ExecContext(t.Context(), `UPDATE channels SET parent_id=$1 WHERE id<>$1`, parent); err != nil {
					t.Fatal(err)
				}
			}
			r, err := s.InspectRoleSetup(t.Context(), "role-owner")
			if err != nil || r.State != "inconsistent" || r.ConfiguredOwnerID != owner || r.Revision != 1 || len(r.Issues) == 0 || r.Issues[len(r.Issues)-1] != "invalid_role_policy" {
				t.Fatalf("invalid policy lost its safe report: %+v %v", r, err)
			}
		})
	}
}

func TestRoleSetupActivationRequiresCleanPreparationAndRotatesEveryScope(t *testing.T) {
	s, owner, _ := roleTestStore(t)
	var secondChannel int64
	if err := s.db.QueryRowContext(t.Context(), `INSERT INTO channels(name,channel_type) VALUES('Second',2) RETURNING id`).Scan(&secondChannel); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PrepareRolePolicy(t.Context(), owner); err != nil {
		t.Fatal(err)
	}
	oldGlobalID, err := s.AllocScopeKeyID(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	oldWrapped := []byte{9, 8, 7}
	if err := s.InsertScopeKey(t.Context(), 0, oldGlobalID, oldWrapped, 3); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActiveRolePolicy(t.Context()); !errors.Is(err, authorization.ErrRolesInactive) {
		t.Fatalf("inactive policy served: %v", err)
	}
	generated := 0
	result, err := s.ActivatePreparedRolePolicy(t.Context(), "role-owner", func() (uint16, []byte, error) {
		generated++
		return 7, []byte{byte(generated), 1, 2, 3}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Policy.OwnerID != owner || result.RotatedScopes != 3 || generated != 3 {
		t.Fatalf("activation result = %+v; generated=%d", result, generated)
	}
	active, err := s.ActiveRolePolicy(t.Context())
	if err != nil || active.Revision != result.Policy.Revision || len(active.Channels) != 2 {
		t.Fatalf("active policy = %+v: %v", active, err)
	}
	var activeFlag bool
	var currentKeys, retiredKeys, activationAudits int
	if err := s.db.QueryRowContext(t.Context(), `SELECT
		(SELECT active FROM authorization_config),
		(SELECT count(*) FROM chat_scope_keys WHERE retired_at IS NULL),
		(SELECT count(*) FROM chat_scope_keys WHERE retired_at IS NOT NULL),
		(SELECT count(*) FROM audit_log WHERE action='roles.activate')`).Scan(
		&activeFlag, &currentKeys, &retiredKeys, &activationAudits); err != nil {
		t.Fatal(err)
	}
	oldGlobal, err := s.GetScopeKey(t.Context(), 0, oldGlobalID)
	if err != nil || oldGlobal == nil || oldGlobal.RetiredAt == nil || !slices.Equal(oldGlobal.Wrapped, oldWrapped) {
		t.Fatalf("historical global key = %+v: %v", oldGlobal, err)
	}
	if !activeFlag || currentKeys != 3 || retiredKeys != 1 || activationAudits != 1 {
		t.Fatalf("active=%v current=%d retired=%d audits=%d", activeFlag, currentKeys, retiredKeys, activationAudits)
	}
	if _, err := s.ActivatePreparedRolePolicy(t.Context(), "role-owner", func() (uint16, []byte, error) {
		return 7, []byte{9}, nil
	}); !errors.Is(err, ErrRoleSetupAlreadyActive) {
		t.Fatalf("repeated activation = %v", err)
	}
	_ = secondChannel
}

func TestRoleSetupActivationRollsBackKeyFailureAndRejectsDirtyPreparation(t *testing.T) {
	for _, scenario := range []string{"key failure", "assigned member", "channel exception", "changed starter role", "channel after preparation"} {
		t.Run(scenario, func(t *testing.T) {
			s, owner, member := roleTestStore(t)
			if _, err := s.PrepareRolePolicy(t.Context(), owner); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "assigned member":
				var roleID int64
				if err := s.db.QueryRowContext(t.Context(), `SELECT id FROM auth_roles WHERE name='Member'`).Scan(&roleID); err != nil {
					t.Fatal(err)
				}
				if _, err := s.db.ExecContext(t.Context(), `INSERT INTO auth_member_roles(user_id,role_id) VALUES($1,$2)`, member, roleID); err != nil {
					t.Fatal(err)
				}
			case "channel exception":
				var channelID int64
				if err := s.db.QueryRowContext(t.Context(), `SELECT id FROM channels LIMIT 1`).Scan(&channelID); err != nil {
					t.Fatal(err)
				}
				if _, err := s.db.ExecContext(t.Context(), `INSERT INTO auth_channel_member_overrides(channel_id,user_id,capability,effect) VALUES($1,$2,'view_channel','allow')`, channelID, member); err != nil {
					t.Fatal(err)
				}
			case "changed starter role":
				if _, err := s.db.ExecContext(t.Context(), `DELETE FROM auth_role_grants WHERE role_id=(SELECT id FROM auth_roles WHERE name='Moderator')`); err != nil {
					t.Fatal(err)
				}
			case "channel after preparation":
				if _, err := s.db.ExecContext(t.Context(), `INSERT INTO channels(name,channel_type) VALUES('Late',2)`); err != nil {
					t.Fatal(err)
				}
			}
			keyErr := errors.New("key generation failed")
			calls := 0
			_, err := s.ActivatePreparedRolePolicy(t.Context(), "role-owner", func() (uint16, []byte, error) {
				calls++
				if scenario == "key failure" {
					return 0, nil, keyErr
				}
				return 1, []byte{1}, nil
			})
			if scenario == "key failure" {
				if !errors.Is(err, keyErr) {
					t.Fatalf("activation error = %v", err)
				}
			} else if !errors.Is(err, ErrRoleSetupNotReady) || calls != 0 {
				t.Fatalf("dirty preparation error=%v calls=%d", err, calls)
			}
			var active bool
			var keys, audits int
			if err := s.db.QueryRowContext(context.Background(), `SELECT
				(SELECT active FROM authorization_config),
				(SELECT count(*) FROM chat_scope_keys),
				(SELECT count(*) FROM audit_log WHERE action='roles.activate')`).Scan(&active, &keys, &audits); err != nil {
				t.Fatal(err)
			}
			if active || keys != 0 || audits != 0 {
				t.Fatalf("failed activation changed state: active=%v keys=%d audits=%d", active, keys, audits)
			}
		})
	}
}

func TestRegistrationAssignsOnlyTheActiveDefaultMemberRole(t *testing.T) {
	s, owner, _ := roleTestStore(t)
	if _, err := s.PrepareRolePolicy(t.Context(), owner); err != nil {
		t.Fatal(err)
	}
	stagedUser, err := s.RegisterUserWithDefaultRole(t.Context(), "staged-user", "Staged", "hash", "key")
	if err != nil {
		t.Fatal(err)
	}
	var stagedAssignments int
	if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM auth_member_roles WHERE user_id=$1`, stagedUser).Scan(&stagedAssignments); err != nil || stagedAssignments != 0 {
		t.Fatalf("inactive registration assignments=%d: %v", stagedAssignments, err)
	}
	if _, err := s.ActivatePreparedRolePolicy(t.Context(), "role-owner", func() (uint16, []byte, error) {
		return 1, []byte{1, 2, 3}, nil
	}); err != nil {
		t.Fatal(err)
	}
	p, err := s.ActiveRolePolicy(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var memberRoleID int64
	for _, role := range p.Roles {
		if role.Name == "Member" {
			memberRoleID = role.ID
			break
		}
	}
	if memberRoleID == 0 {
		t.Fatal("prepared Member role missing")
	}
	if _, err := s.ChangeRolePolicy(t.Context(), owner, authorization.RoleChange{
		Kind: authorization.DefaultMemberRoleSet, ExpectedRevision: p.Revision, RoleID: memberRoleID,
	}); err != nil {
		t.Fatal(err)
	}
	memberID, err := s.RegisterUserWithDefaultRole(t.Context(), "active-user", "Active", "hash", "key")
	if err != nil {
		t.Fatal(err)
	}
	var assignedRoleID int64
	if err := s.db.QueryRowContext(t.Context(), `SELECT role_id FROM auth_member_roles WHERE user_id=$1`, memberID).Scan(&assignedRoleID); err != nil || assignedRoleID != memberRoleID {
		t.Fatalf("active registration role=%d want=%d: %v", assignedRoleID, memberRoleID, err)
	}
}
