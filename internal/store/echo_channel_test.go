//go:build integration

package store

import (
	"errors"
	"sync"
	"testing"

	"noxa/internal/authorization"
)

func echoTestStore(t *testing.T) (*Store, authorization.RolePolicy) {
	t.Helper()
	s, owner, _ := roleTestStore(t)
	p, err := s.PrepareRolePolicy(t.Context(), owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActivatePreparedRolePolicy(t.Context(), "role-owner", func() (uint16, []byte, error) { return 1, []byte("test-key"), nil }); err != nil {
		t.Fatal(err)
	}
	return s, p
}

func TestEnsureEchoChannelConcurrentStartup(t *testing.T) {
	s, before := echoTestStore(t)
	var wg sync.WaitGroup
	ids := make([]int64, 8)
	for i := range ids {
		wg.Go(func() {
			id, err := s.EnsureEchoChannel(t.Context(), "Echo Test")
			if err != nil {
				t.Error(err)
				return
			}
			ids[i] = id
		})
	}
	wg.Wait()
	for _, id := range ids {
		if id == 0 || id != ids[0] {
			t.Fatalf("duplicate or missing channels: %v", ids)
		}
	}
	after, err := s.ActiveRolePolicy(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != before.Revision+1 || after.OwnerID != before.OwnerID || len(after.Channels) != len(before.Channels)+1 {
		t.Fatalf("unexpected policy: %+v", after)
	}
	e, err := authorization.NewRoleEvaluator(after)
	if err != nil {
		t.Fatal(err)
	}
	for _, capability := range []authorization.Capability{authorization.ViewChannel, authorization.Connect, authorization.Speak} {
		if !e.Evaluate(0, ids[0], capability).Allowed {
			t.Fatalf("echo guest missing %s", capability)
		}
		if e.Evaluate(0, before.Channels[0].ChannelID, capability).Allowed {
			t.Fatalf("echo granted %s in another channel", capability)
		}
	}
	if e.Evaluate(0, ids[0], authorization.ManageChannels).Allowed {
		t.Fatal("echo granted channel management")
	}
	var count int
	if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM audit_log WHERE actor_unique_id='system:echo-channel' AND action='roles.echo_channel_create'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("audit count=%d: %v", count, err)
	}
	var permanent, systemCreated bool
	if err := s.db.QueryRowContext(t.Context(), `SELECT channel_type=2,created_by IS NULL FROM channels WHERE id=$1`, ids[0]).Scan(&permanent, &systemCreated); err != nil || !permanent || !systemCreated {
		t.Fatalf("echo resource: %t %t %v", permanent, systemCreated, err)
	}
}

func TestEnsureEchoChannelPreservesExistingPrivateChannel(t *testing.T) {
	s, before := echoTestStore(t)
	id, err := s.EnsureEchoChannel(t.Context(), "Private")
	if err != nil || id != before.Channels[0].ChannelID {
		t.Fatalf("reuse: %d %v", id, err)
	}
	after, err := s.ActiveRolePolicy(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	e, err := authorization.NewRoleEvaluator(after)
	if err != nil || after.Revision != before.Revision || len(after.Channels) != len(before.Channels) || e.Evaluate(0, id, authorization.ViewChannel).Allowed {
		t.Fatal("existing private channel changed")
	}
}

func TestEnsureEchoChannelRequiresActivePolicy(t *testing.T) {
	s, owner, _ := roleTestStore(t)
	if _, err := s.EnsureEchoChannel(t.Context(), "Echo Test"); !errors.Is(err, authorization.ErrRolesNotConfigured) {
		t.Fatalf("unconfigured: %v", err)
	}
	if _, err := s.PrepareRolePolicy(t.Context(), owner); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureEchoChannel(t.Context(), "Echo Test"); !errors.Is(err, authorization.ErrRolesInactive) {
		t.Fatalf("inactive: %v", err)
	}
}

func TestEnsureEchoChannelRollsBackOnAuditFailure(t *testing.T) {
	s, before := echoTestStore(t)
	if _, err := s.db.ExecContext(t.Context(), `ALTER TABLE audit_log ADD CONSTRAINT reject_echo_audit CHECK (action <> 'roles.echo_channel_create')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureEchoChannel(t.Context(), "Echo Test"); err == nil {
		t.Fatal("audit failure accepted")
	}
	after, err := s.ActiveRolePolicy(t.Context())
	if err != nil || after.Revision != before.Revision || len(after.Channels) != len(before.Channels) {
		t.Fatalf("partial policy: %+v %v", after, err)
	}
	var count int
	if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM channels WHERE name='Echo Test'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial channel: %d %v", count, err)
	}
}
