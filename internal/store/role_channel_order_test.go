//go:build integration

package store

import (
	"encoding/json"
	"errors"
	"testing"

	"noxa/internal/authorization"
)

func TestRoleChannelMoveOrderCommitsAndRollsBackWithParent(t *testing.T) {
	s, owner, member := roleTestStore(t)
	p, err := s.PrepareRolePolicy(t.Context(), owner)
	if err != nil {
		t.Fatal(err)
	}
	parent := p.Channels[0].ChannelID
	p, child, err := s.ChangeRoleChannel(t.Context(), owner, authorization.ChannelTreeChange{
		Kind: authorization.ChannelCreate, ExpectedRevision: p.Revision, ParentID: parent,
		Access: authorization.ChannelPolicy{ParentID: parent, Synced: true},
	}, &RoleChannelCreate{Name: "Child", ChannelType: 2, OrderIndex: 9})
	if err != nil {
		t.Fatal(err)
	}
	order := int32(0)
	move := authorization.ChannelTreeChange{Kind: authorization.ChannelMove, ExpectedRevision: p.Revision, ChannelID: child, OrderIndex: &order}
	assertStored := func(wantParent int64, wantOrder int, revision int64) {
		t.Helper()
		var gotParent, gotRevision int64
		var gotOrder int
		if err := s.db.QueryRowContext(t.Context(), `SELECT COALESCE(parent_id,0),order_index,(SELECT revision FROM authorization_config WHERE singleton) FROM channels WHERE id=$1`, child).Scan(&gotParent, &gotOrder, &gotRevision); err != nil || gotParent != wantParent || gotOrder != wantOrder || gotRevision != revision {
			t.Fatalf("placement/revision = %d/%d/%d, want %d/%d/%d: %v", gotParent, gotOrder, gotRevision, wantParent, wantOrder, revision, err)
		}
	}
	if _, _, err := s.ChangeRoleChannel(t.Context(), member, move, nil); !errors.Is(err, authorization.ErrRoleForbidden) {
		t.Fatal(err)
	}
	stale := move
	stale.ExpectedRevision--
	if _, _, err := s.ChangeRoleChannel(t.Context(), owner, stale, nil); !errors.Is(err, authorization.ErrRoleConflict) {
		t.Fatal(err)
	}
	assertStored(parent, 9, p.Revision)
	if _, err := s.db.ExecContext(t.Context(), `CREATE FUNCTION reject_order_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'audit unavailable'; END $$;
		CREATE TRIGGER reject_order_audit BEFORE INSERT ON audit_log FOR EACH ROW WHEN (NEW.action='roles.channel_move') EXECUTE FUNCTION reject_order_audit()`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ChangeRoleChannel(t.Context(), owner, move, nil); err == nil {
		t.Fatal("accepted failed audit")
	}
	assertStored(parent, 9, p.Revision)
	if _, err := s.db.ExecContext(t.Context(), `DROP TRIGGER reject_order_audit ON audit_log`); err != nil {
		t.Fatal(err)
	}
	p, _, err = s.ChangeRoleChannel(t.Context(), owner, move, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertStored(0, 0, p.Revision)
	e, err := authorization.NewRoleEvaluator(p)
	if err != nil || e.Evaluate(member, child, authorization.ViewChannel).Allowed {
		t.Fatal("placement exposed private child", err)
	}
	var detail string
	if err := s.db.QueryRowContext(t.Context(), `SELECT detail FROM audit_log WHERE action='roles.channel_move' ORDER BY id DESC LIMIT 1`).Scan(&detail); err != nil {
		t.Fatalf("order audit: %s %v", detail, err)
	}
	var audit struct {
		Before, After struct{ Settings RoleChannelSettings }
	}
	if err := json.Unmarshal([]byte(detail), &audit); err != nil || audit.Before.Settings.OrderIndex != 9 || audit.After.Settings.OrderIndex != 0 {
		t.Fatalf("order audit: %s %v", detail, err)
	}
	order = -7
	move.ExpectedRevision, move.ParentID = p.Revision, parent
	p, _, err = s.ChangeRoleChannel(t.Context(), owner, move, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertStored(parent, -7, p.Revision)
	move.ExpectedRevision, move.ParentID, move.OrderIndex = p.Revision, 0, nil
	p, _, err = s.ChangeRoleChannel(t.Context(), owner, move, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertStored(0, -7, p.Revision)
}
