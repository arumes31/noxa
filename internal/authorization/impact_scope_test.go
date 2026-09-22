package authorization

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestChannelImpactSyncedDescendantScopes(t *testing.T) {
	p := impactFixture()
	p.Channels[1].Synced = true
	p.Channels = append(p.Channels, ChannelPolicy{ChannelID: 102, ParentID: 101, Synced: true},
		ChannelPolicy{ChannelID: 103, ParentID: 100}, ChannelPolicy{ChannelID: 104, ParentID: 103, Synced: true})
	e, err := NewRoleEvaluator(p)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := e.ImpactDescendants(context.Background(), 1, 100)
	if err != nil || !reflect.DeepEqual(ids, []int64{101, 102}) {
		t.Fatalf("scopes: %v, %v", ids, err)
	}
	change := RoleChange{Kind: ChannelAccessSet, ExpectedRevision: 1, Channel: ChannelPolicy{ChannelID: 100,
		Overrides: []RoleOverride{{RoleID: 10, Capability: Speak, Effect: Deny}}}}
	for _, id := range []int64{100, 101, 102} {
		result, err := PreviewChannelAccessInScope(context.Background(), p, 1, change, id, []int64{0, 1})
		if err != nil || result.ChannelID != id || len(result.Members) != 2 || !reflect.DeepEqual(result.Members[0].Changes, []AccessImpactChange{{Capability: Speak, Before: true, After: false}}) || len(result.Members[1].Changes) != 0 {
			t.Fatalf("scope %d: %+v, %v", id, result, err)
		}
	}
	for _, id := range []int64{103, 104, 999} {
		if _, err := PreviewChannelAccessInScope(context.Background(), p, 1, change, id, []int64{0}); !errors.Is(err, ErrRoleForbidden) {
			t.Fatalf("unrelated/custom scope %d: %v", id, err)
		}
	}
	if _, err := PreviewChannelAccessInScope(context.Background(), p, 1, change, -1, []int64{0}); !errors.Is(err, ErrRoleInvalid) {
		t.Fatalf("negative scope: %v", err)
	}
}

func TestChannelMoveImpactDescendantMatchesMovedPolicy(t *testing.T) {
	p := impactFixture()
	p.Channels[1].Synced = true
	p.Channels = append(p.Channels, ChannelPolicy{ChannelID: 102, Overrides: []RoleOverride{{RoleID: 10, Capability: Speak, Effect: Deny}}})
	for _, sync := range []bool{false, true} {
		change := ChannelTreeChange{Kind: ChannelMove, ExpectedRevision: 1, ChannelID: 100, ParentID: 102, SyncToParent: sync}
		result, err := PreviewChannelTreeInScope(context.Background(), p, 1, change, 101, []int64{0})
		if err != nil || result.ChannelID != 101 {
			t.Fatalf("move scope: %+v, %v", result, err)
		}
		want := 0
		if sync {
			want = 1
		}
		if len(result.Members[0].Changes) != want {
			t.Fatalf("sync=%v: %+v", sync, result)
		}
	}
	create := ChannelTreeChange{Kind: ChannelCreate, ExpectedRevision: 1, Access: ChannelPolicy{Synced: true}}
	if _, err := PreviewChannelTreeInScope(context.Background(), p, 1, create, 101, []int64{0}); !errors.Is(err, ErrRoleInvalid) {
		t.Fatalf("create descendant: %v", err)
	}
}

func TestImpactScopeCannotInspectWithoutSourceAuthority(t *testing.T) {
	p := impactFixture()
	p.Channels[1].Synced = true
	e, err := NewRoleEvaluator(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.ImpactDescendants(context.Background(), 3, 100); !errors.Is(err, ErrRoleForbidden) {
		t.Fatalf("unauthorized discovery: %v", err)
	}
	change := RoleChange{Kind: ChannelAccessSet, ExpectedRevision: 1, Channel: p.Channels[0]}
	if _, err := PreviewChannelAccessInScope(context.Background(), p, 3, change, 101, []int64{0}); !errors.Is(err, ErrRoleForbidden) {
		t.Fatalf("unauthorized preview: %v", err)
	}
}
