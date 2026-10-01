package authorization

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func impactFixture() RolePolicy {
	return RolePolicy{Revision: 1, OwnerID: 1, EveryoneID: 10,
		Roles: []Role{
			{ID: 10, Name: "@everyone", Permissions: []Capability{ViewChannel, ReadHistory, Connect, Speak}},
			{ID: 20, Name: "Admin", Position: 1, Permissions: []Capability{Administrator}},
		}, Members: []RoleMember{{UserID: 2, RoleIDs: []int64{20}}},
		Channels: []ChannelPolicy{{ChannelID: 100}, {ChannelID: 101, ParentID: 100}},
	}
}

func TestChannelAccessImpactMatchesCommittedPolicyWithoutMutation(t *testing.T) {
	p := impactFixture()
	before := cloneRolePolicy(p)
	change := RoleChange{Kind: ChannelAccessSet, ExpectedRevision: 1,
		Channel: ChannelPolicy{ChannelID: 101, ParentID: 100, Overrides: []RoleOverride{{RoleID: 10, Capability: ViewChannel, Effect: Deny}}}}
	impact, err := PreviewChannelAccess(context.Background(), p, 1, change, []int64{0, 1, 2, 3})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p, before) || impact.Revision != 1 || impact.ChannelID != 101 || len(impact.Members) != 4 {
		t.Fatalf("preview changed state or scope: %+v", impact)
	}
	committed, err := ApplyRoleChange(p, 1, change)
	if err != nil {
		t.Fatal(err)
	}
	oldEval, _ := NewRoleEvaluator(p)
	newEval, _ := NewRoleEvaluator(committed)
	for _, member := range impact.Members {
		var want []AccessImpactChange
		for _, capability := range Capabilities() {
			if !capability.Channel {
				continue
			}
			old, next := oldEval.Evaluate(member.UserID, 101, capability.Key), newEval.Evaluate(member.UserID, 101, capability.Key)
			if old.Allowed != next.Allowed {
				want = append(want, AccessImpactChange{Capability: capability.Key, Before: old.Allowed, After: next.Allowed})
			}
		}
		if len(want) != len(member.Changes) || (len(want) > 0 && !reflect.DeepEqual(want, member.Changes)) {
			t.Fatalf("member %d: %+v, want %+v", member.UserID, member.Changes, want)
		}
		if (member.UserID == 1 || member.UserID == 2) && len(member.Changes) != 0 {
			t.Fatal("owner/Administrator lost access")
		}
		if (member.UserID == 0 || member.UserID == 3) && len(member.Changes) != 4 {
			t.Fatalf("prerequisites omitted: %+v", member)
		}
	}
}

func TestChannelAccessImpactRejectsInvalidOrUnauthorizedDrafts(t *testing.T) {
	p := impactFixture()
	change := RoleChange{Kind: ChannelAccessSet, ExpectedRevision: 1, Channel: p.Channels[1]}
	for _, tt := range []struct {
		name   string
		actor  int64
		change RoleChange
		users  []int64
		want   error
	}{
		{"unprivileged", 3, change, []int64{3}, ErrRoleForbidden},
		{"stale", 1, RoleChange{Kind: ChannelAccessSet, ExpectedRevision: 2, Channel: p.Channels[1]}, []int64{3}, ErrRoleConflict},
		{"unknown channel", 1, RoleChange{Kind: ChannelAccessSet, ExpectedRevision: 1, Channel: ChannelPolicy{ChannelID: 999}}, []int64{3}, ErrRoleForbidden},
		{"another mutation", 1, RoleChange{Kind: RoleDelete, ExpectedRevision: 1, RoleID: 20}, []int64{3}, ErrRoleInvalid},
		{"no subjects", 1, change, nil, ErrRoleInvalid},
		{"negative subject", 1, change, []int64{-1}, ErrRoleInvalid},
		{"duplicate subject", 1, change, []int64{3, 3}, ErrRoleInvalid},
		{"oversized", 1, change, make([]int64, 102), ErrRoleInvalid},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := PreviewChannelAccess(context.Background(), p, tt.actor, tt.change, tt.users); !errors.Is(err, tt.want) {
				t.Fatalf("got %v, want %v", err, tt.want)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := PreviewChannelAccess(ctx, p, 1, change, []int64{3}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled: %v", err)
	}
}

func TestChannelAccessImpactResyncAndMemberException(t *testing.T) {
	p := impactFixture()
	p.Channels[0].Overrides = []RoleOverride{{RoleID: 10, Capability: Speak, Effect: Deny}, {UserID: 3, Capability: Speak, Effect: Allow}}
	change := RoleChange{Kind: ChannelAccessSet, ExpectedRevision: 1, Channel: ChannelPolicy{ChannelID: 101, ParentID: 100, Synced: true}}
	impact, err := PreviewChannelAccess(context.Background(), p, 1, change, []int64{3, 4})
	if err != nil {
		t.Fatal(err)
	}
	if len(impact.Members[0].Changes) != 0 || !reflect.DeepEqual(impact.Members[1].Changes, []AccessImpactChange{{Capability: Speak, Before: true, After: false}}) {
		t.Fatalf("resync: %+v", impact)
	}
}
