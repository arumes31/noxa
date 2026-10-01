package authorization

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestChannelTreeImpactMoveKeepsOrSyncsAccess(t *testing.T) {
	p := impactFixture()
	p.Channels = append(p.Channels, ChannelPolicy{ChannelID: 102, Overrides: []RoleOverride{{RoleID: 10, Capability: Speak, Effect: Deny}}})
	for _, sync := range []bool{false, true} {
		change := ChannelTreeChange{Kind: ChannelMove, ExpectedRevision: 1, ChannelID: 101, ParentID: 102, SyncToParent: sync}
		result, err := PreviewChannelTree(context.Background(), p, 1, change, []int64{0, 1, 2, 3})
		if err != nil {
			t.Fatal(err)
		}
		if result.ChannelID != 101 || result.Revision != 1 {
			t.Fatalf("scope: %+v", result)
		}
		for _, member := range result.Members {
			want := 0
			if sync && (member.UserID == 0 || member.UserID == 3) {
				want = 1
			}
			if len(member.Changes) != want || (want > 0 && !reflect.DeepEqual(member.Changes, []AccessImpactChange{{Capability: Speak, Before: true, After: false}})) {
				t.Fatalf("sync=%v: %+v", sync, member)
			}
		}
	}
}

func TestChannelTreeImpactCreationHasNoPersistedIDOrEffects(t *testing.T) {
	p := impactFixture()
	original := cloneRolePolicy(p)
	change := ChannelTreeChange{Kind: ChannelCreate, ExpectedRevision: 1, ParentID: 100, Access: ChannelPolicy{ParentID: 100, Synced: true}}
	result, err := PreviewChannelTree(context.Background(), p, 1, change, []int64{0, 1, 2, 3})
	if err != nil {
		t.Fatal(err)
	}
	if result.ChannelID != 0 || result.Revision != 1 || !reflect.DeepEqual(p, original) {
		t.Fatalf("created a resource: %+v", result)
	}
	for _, member := range result.Members {
		if len(member.Changes) < 4 {
			t.Fatalf("missing proposed access: %+v", member)
		}
		for _, change := range member.Changes {
			if change.Before || !change.After {
				t.Fatalf("new channel has old access: %+v", change)
			}
		}
	}
	change.Access.Synced = false
	change.Access.Overrides = []RoleOverride{{RoleID: 10, Capability: ViewChannel, Effect: Deny}}
	result, err = PreviewChannelTree(context.Background(), p, 1, change, []int64{0, 1})
	if err != nil || len(result.Members[0].Changes) != 0 || len(result.Members[1].Changes) == 0 {
		t.Fatalf("private creation: %+v, %v", result, err)
	}
}

func TestChannelTreeImpactRejectsScopeAndAuthorityViolations(t *testing.T) {
	p := impactFixture()
	valid := ChannelTreeChange{Kind: ChannelMove, ExpectedRevision: 1, ChannelID: 101, ParentID: 100}
	for _, tt := range []struct {
		name   string
		actor  int64
		change ChannelTreeChange
		users  []int64
		want   error
	}{
		{"unauthorized", 3, valid, []int64{0}, ErrRoleForbidden},
		{"duplicate users", 1, valid, []int64{0, 0}, ErrRoleInvalid},
		{"too many users", 1, valid, make([]int64, 102), ErrRoleInvalid},
		{"no users", 1, valid, nil, ErrRoleInvalid},
		{"negative user", 1, valid, []int64{-1}, ErrRoleInvalid},
		{"cycle", 1, ChannelTreeChange{Kind: ChannelMove, ExpectedRevision: 1, ChannelID: 100, ParentID: 101}, []int64{0}, ErrRoleInvalid},
		{"stale", 1, ChannelTreeChange{Kind: ChannelMove, ExpectedRevision: 2, ChannelID: 101}, []int64{0}, ErrRoleConflict},
		{"delete", 1, ChannelTreeChange{Kind: ChannelDelete, ExpectedRevision: 1, ChannelID: 101}, []int64{0}, ErrRoleInvalid},
		{"client allocated ID", 1, ChannelTreeChange{Kind: ChannelCreate, ExpectedRevision: 1, ChannelID: 200}, []int64{0}, ErrRoleInvalid},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := PreviewChannelTree(context.Background(), p, tt.actor, tt.change, tt.users); !errors.Is(err, tt.want) {
				t.Fatalf("got %v, want %v", err, tt.want)
			}
		})
	}
}
