//go:build integration

package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"noxa/internal/authorization"
)

func TestRoleStoreChannelSettingsEditIsAtomicAndPreservesAccess(t *testing.T) {
	s, owner, member := roleTestStore(t)
	p, err := s.PrepareRolePolicy(t.Context(), owner)
	if err != nil {
		t.Fatal(err)
	}
	id := p.Channels[0].ChannelID
	settings := RoleChannelSettings{Name: "Updated", Topic: "Topic", Description: "Description", MaxClients: 12, SlowModeSeconds: 3, OpusBitrate: 64000, OpusFEC: true}
	if _, err := s.EditRoleChannel(t.Context(), member, id, 1, settings); !errors.Is(err, authorization.ErrRoleForbidden) {
		t.Fatalf("unauthorized edit: %v", err)
	}
	after, err := s.EditRoleChannel(t.Context(), owner, id, 1, settings)
	if err != nil || after.Revision != 2 {
		t.Fatalf("edit: %+v %v", after, err)
	}
	e, err := authorization.NewRoleEvaluator(after)
	if err != nil || e.Evaluate(member, id, authorization.ViewChannel).Allowed {
		t.Fatal("metadata edit changed private access")
	}
	var name, description string
	var capacity, slowmode int
	if err := s.DB().QueryRowContext(t.Context(), `SELECT name,description,max_clients,slow_mode_seconds FROM channels WHERE id=$1`, id).Scan(&name, &description, &capacity, &slowmode); err != nil || name != settings.Name || description != settings.Description || capacity != 12 || slowmode != 3 {
		t.Fatalf("metadata not saved: %s %s %d %d %v", name, description, capacity, slowmode, err)
	}
	settings.Name = "Stale"
	if _, err := s.EditRoleChannel(t.Context(), owner, id, 1, settings); !errors.Is(err, authorization.ErrRoleConflict) {
		t.Fatalf("stale edit: %v", err)
	}
	settings.OpusBitrate = -1
	if _, err := s.EditRoleChannel(t.Context(), owner, id, 2, settings); !errors.Is(err, authorization.ErrRoleInvalid) {
		t.Fatalf("invalid settings: %v", err)
	}
	var audits int
	if err := s.DB().QueryRowContext(t.Context(), `SELECT count(*) FROM audit_log WHERE action='roles.channel_edit'`).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("edit audit count: %d %v", audits, err)
	}
	if err := s.DB().QueryRowContext(t.Context(), `SELECT name FROM channels WHERE id=$1`, id).Scan(&name); err != nil || name != "Updated" {
		t.Fatalf("rejected edit changed metadata: %q %v", name, err)
	}
}

func TestRoleStoreTemporaryCleanupHasSystemAuditAndCannotCascade(t *testing.T) {
	s, owner, _ := roleTestStore(t)
	p, err := s.PrepareRolePolicy(t.Context(), owner)
	if err != nil {
		t.Fatal(err)
	}
	permanent := p.Channels[0].ChannelID
	for _, id := range []int64{permanent, 999999} {
		if _, err := s.PruneTemporaryRoleChannel(t.Context(), id, p.Revision); !errors.Is(err, authorization.ErrLifecycleUnchanged) {
			t.Fatalf("cleanup %d: %v", id, err)
		}
	}
	p, parent, err := s.ChangeRoleChannel(t.Context(), owner, authorization.ChannelTreeChange{Kind: authorization.ChannelCreate, ExpectedRevision: p.Revision, Temporary: true, Access: authorization.ChannelPolicy{Synced: true}}, &RoleChannelCreate{Name: "Temporary parent", ChannelType: 0})
	if err != nil {
		t.Fatal(err)
	}
	p, child, err := s.ChangeRoleChannel(t.Context(), owner, authorization.ChannelTreeChange{Kind: authorization.ChannelCreate, ExpectedRevision: p.Revision, Temporary: true, ParentID: parent, Access: authorization.ChannelPolicy{ParentID: parent, Synced: true}}, &RoleChannelCreate{Name: "Temporary child", ChannelType: 0})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PruneTemporaryRoleChannel(t.Context(), parent, p.Revision); !errors.Is(err, authorization.ErrLifecycleUnchanged) {
		t.Fatalf("non-leaf cleanup: %v", err)
	}
	if _, err := s.PruneTemporaryRoleChannel(t.Context(), child, p.Revision-1); !errors.Is(err, authorization.ErrRoleConflict) {
		t.Fatalf("stale cleanup: %v", err)
	}
	if _, _, err := s.ChangeRoleChannel(t.Context(), 0, authorization.ChannelTreeChange{Kind: authorization.ChannelDelete, ExpectedRevision: p.Revision, ChannelID: child}, nil); !errors.Is(err, authorization.ErrRoleForbidden) {
		t.Fatalf("public actor-zero bypass: %v", err)
	}
	pruned, err := s.PruneTemporaryRoleChannel(t.Context(), child, p.Revision)
	if err != nil || pruned.Revision != p.Revision+1 || len(pruned.Channels) != 2 {
		t.Fatalf("cleanup result: %+v %v", pruned, err)
	}
	var actor, detail string
	if err := s.db.QueryRowContext(t.Context(), `SELECT actor_unique_id,detail FROM audit_log WHERE action='roles.channel_cleanup'`).Scan(&actor, &detail); err != nil || actor != "system:temporary-channel-cleanup" || !strings.Contains(detail, `"revision":4`) {
		t.Fatalf("system audit: %q %q %v", actor, detail, err)
	}
	if _, err := s.PruneTemporaryRoleChannel(t.Context(), child, pruned.Revision); !errors.Is(err, authorization.ErrLifecycleUnchanged) {
		t.Fatalf("repeat cleanup: %v", err)
	}
	pruned, err = s.PruneTemporaryRoleChannel(t.Context(), parent, pruned.Revision)
	if err != nil || len(pruned.Channels) != 1 || pruned.Channels[0].ChannelID != permanent {
		t.Fatalf("cleanup damaged surviving policy: %+v %v", pruned, err)
	}
}

func TestRoleStoreChannelLifecycleCommitsTreePolicyAndAuditTogether(t *testing.T) {
	s, owner, member := roleTestStore(t)
	p, err := s.PrepareRolePolicy(t.Context(), owner)
	if err != nil {
		t.Fatal(err)
	}
	parent := p.Channels[0].ChannelID
	change := authorization.ChannelTreeChange{Kind: authorization.ChannelCreate, ExpectedRevision: p.Revision, ParentID: parent, Access: authorization.ChannelPolicy{ParentID: parent, Synced: true}}
	record := &RoleChannelCreate{Name: "Protected child", ChannelType: 2, MaxClients: 12, PasswordHash: "password-hash-canary", OpusBitrate: 64000}
	p, child, err := s.ChangeRoleChannel(t.Context(), owner, change, record)
	if err != nil {
		t.Fatal(err)
	}
	if child <= 0 || len(p.Channels) != 2 || p.Revision != 2 {
		t.Fatalf("create result: %+v id=%d", p, child)
	}
	var storedParent int64
	var storedHash string
	if err := s.db.QueryRowContext(t.Context(), `SELECT parent_id,password_hash FROM channels WHERE id=$1`, child).Scan(&storedParent, &storedHash); err != nil || storedParent != parent || storedHash != record.PasswordHash {
		t.Fatalf("resource did not persist: %d %q %v", storedParent, storedHash, err)
	}
	var detail string
	if err := s.db.QueryRowContext(t.Context(), `SELECT detail FROM audit_log WHERE action='roles.channel_create'`).Scan(&detail); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(detail, record.PasswordHash) || strings.Contains(detail, "Password") {
		t.Fatalf("secret entered audit: %s", detail)
	}
	// Default move keeps the private parent's effective policy after moving to root.
	p, _, err = s.ChangeRoleChannel(t.Context(), owner, authorization.ChannelTreeChange{Kind: authorization.ChannelMove, ExpectedRevision: p.Revision, ChannelID: child}, nil)
	if err != nil {
		t.Fatal(err)
	}
	e, err := authorization.NewRoleEvaluator(p)
	if err != nil || e.Evaluate(member, child, authorization.ViewChannel).Allowed {
		t.Fatalf("move exposed private child: %v", err)
	}
	if err := s.db.QueryRowContext(t.Context(), `SELECT COALESCE(parent_id,0) FROM channels WHERE id=$1`, child).Scan(&storedParent); err != nil || storedParent != 0 {
		t.Fatalf("resource parent mismatch: %d %v", storedParent, err)
	}
	// Reparent back with sync, then deleting the parent removes both policy rows.
	p, _, err = s.ChangeRoleChannel(t.Context(), owner, authorization.ChannelTreeChange{Kind: authorization.ChannelMove, ExpectedRevision: p.Revision, ChannelID: child, ParentID: parent, SyncToParent: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	p, _, err = s.ChangeRoleChannel(t.Context(), owner, authorization.ChannelTreeChange{Kind: authorization.ChannelDelete, ExpectedRevision: p.Revision, ChannelID: parent}, nil)
	if err != nil || len(p.Channels) != 0 {
		t.Fatalf("delete: %+v %v", p, err)
	}
	for _, table := range []string{"channels", "auth_channel_access", "auth_channel_role_overrides", "auth_channel_member_overrides"} {
		var count int
		// Identifiers come exclusively from the fixed list above.
		if err := s.db.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s retained rows: %d %v", table, count, err)
		}
	}
}

func TestRoleStoreChannelRejectionsLeaveNoResourceOrRevision(t *testing.T) {
	s, owner, member := roleTestStore(t)
	p, err := s.PrepareRolePolicy(t.Context(), owner)
	if err != nil {
		t.Fatal(err)
	}
	create := &RoleChannelCreate{Name: "Rejected", ChannelType: 2}
	change := authorization.ChannelTreeChange{Kind: authorization.ChannelCreate, ExpectedRevision: 1, Access: authorization.ChannelPolicy{Synced: true}}
	if _, _, err := s.ChangeRoleChannel(t.Context(), member, change, create); !errors.Is(err, authorization.ErrRoleForbidden) {
		t.Fatalf("unauthorized create: %v", err)
	}
	change.ExpectedRevision = 0
	if _, _, err := s.ChangeRoleChannel(t.Context(), owner, change, create); !errors.Is(err, authorization.ErrRoleConflict) {
		t.Fatalf("stale create: %v", err)
	}
	change.ExpectedRevision = 1
	// Reject missing override subjects before writes, without closing a serving
	// authority or misclassifying this as an ambiguous database failure.
	change.Access.Synced = false
	change.Access.Overrides = []authorization.RoleOverride{{UserID: 9_999_999, Capability: authorization.ViewChannel, Effect: authorization.Allow}}
	a, err := authorization.NewAuthority(t.Context(), s, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.ChangeLifecyclePolicy(t.Context(), 1, func(ctx context.Context) (authorization.RolePolicy, error) {
		p, _, err := s.ChangeRoleChannel(ctx, owner, change, create)
		return p, err
	}); !errors.Is(err, authorization.ErrRoleInvalid) {
		t.Fatalf("missing member result: %v", err)
	}
	if p, err := a.RolePolicy(t.Context()); err != nil || p.Revision != 1 {
		t.Fatalf("malformed override closed authority: %+v %v", p, err)
	}
	// A failure after resource/policy writes must still roll back both, along
	// with the revision. This trigger exists only in the disposable database.
	if _, err := s.DB().ExecContext(t.Context(), `CREATE FUNCTION reject_channel_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'audit unavailable'; END $$;
		CREATE TRIGGER reject_channel_audit BEFORE INSERT ON audit_log FOR EACH ROW WHEN (NEW.action='roles.channel_create') EXECUTE FUNCTION reject_channel_audit()`); err != nil {
		t.Fatal(err)
	}
	change.Access = authorization.ChannelPolicy{Synced: true}
	if _, _, err := s.ChangeRoleChannel(t.Context(), owner, change, create); err == nil {
		t.Fatal("audit failure accepted")
	}
	after, err := s.RolePolicy(t.Context())
	if err != nil || after.Revision != p.Revision || len(after.Channels) != 1 {
		t.Fatalf("rejection changed policy: %+v %v", after, err)
	}
	var count int
	if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM channels`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("orphan channel: %d %v", count, err)
	}
	if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM audit_log WHERE action='roles.channel_create'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rejected create audited as success: %d %v", count, err)
	}
}
