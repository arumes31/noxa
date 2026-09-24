package authorization

import (
	"errors"
	"reflect"
	"slices"
	"testing"
)

func TestChannelMoveKeepsAccessUnlessSyncIsExplicit(t *testing.T) {
	p := roleFixture()
	for _, sync := range []bool{false, true} {
		next, err := ApplyChannelTreeChange(p, 1, ChannelTreeChange{Kind: ChannelMove, ExpectedRevision: 1, ChannelID: 101, ParentID: 102, SyncToParent: sync})
		if err != nil {
			t.Fatal(err)
		}
		e, err := NewRoleEvaluator(next)
		if err != nil {
			t.Fatal(err)
		}
		if e.Evaluate(3, 101, ViewChannel).Allowed != sync {
			t.Fatalf("sync=%t changed the wrong access", sync)
		}
		ch := e.channels[101]
		if ch.ParentID != 102 || ch.Synced != sync || (len(ch.Overrides) > 0) == sync {
			t.Fatalf("unexpected moved policy %+v", ch)
		}
	}
	if p.Channels[1].ParentID != 100 || !p.Channels[1].Synced || p.Revision != 1 {
		t.Fatal("mutated source policy")
	}
}

func TestChannelMoveValidatesBothScopesAndAccessDelegation(t *testing.T) {
	p := roleFixture()
	p.Roles[2].Permissions = []Capability{ManageChannels}
	change := ChannelTreeChange{Kind: ChannelMove, ExpectedRevision: 1, ChannelID: 101, ParentID: 102}
	if _, err := ApplyChannelTreeChange(p, 2, change); err != nil {
		t.Fatalf("keep requires unrelated access grant: %v", err)
	}
	change.SyncToParent = true
	if _, err := ApplyChannelTreeChange(p, 2, change); !errors.Is(err, ErrRoleForbidden) {
		t.Fatalf("sync changed grants without access authority: %v", err)
	}
	change.SyncToParent = false
	p.Channels[2].Overrides = []RoleOverride{{UserID: 2, Capability: ManageChannels, Effect: Deny}}
	if _, err := ApplyChannelTreeChange(p, 2, change); !errors.Is(err, ErrRoleForbidden) {
		t.Fatalf("destination management bypassed: %v", err)
	}
}

func TestExplicitChannelSyncRequiresAccessAuthorityEvenWithoutCurrentDifference(t *testing.T) {
	p := roleFixture()
	p.Roles[2].Permissions = []Capability{ManageChannels}
	change := ChannelTreeChange{Kind: ChannelMove, ExpectedRevision: 1, ChannelID: 102, SyncToParent: true}
	if _, err := ApplyChannelTreeChange(p, 2, change); !errors.Is(err, ErrRoleForbidden) {
		t.Fatalf("sync silently delegated future access: %v", err)
	}
	change.SyncToParent = false
	if _, err := ApplyChannelTreeChange(p, 2, change); err != nil {
		t.Fatalf("keeping access requires extra grant: %v", err)
	}
}

func TestChannelDeleteRequiresWholeSubtreeAuthority(t *testing.T) {
	p := roleFixture()
	p.Roles[2].Permissions = append(p.Roles[2].Permissions, ManageChannels)
	p.Channels[2].Overrides = []RoleOverride{{UserID: 2, Capability: ManageChannels, Effect: Deny}}
	change := ChannelTreeChange{Kind: ChannelDelete, ExpectedRevision: 1, ChannelID: 100}
	if _, err := ApplyChannelTreeChange(p, 2, change); !errors.Is(err, ErrRoleForbidden) {
		t.Fatalf("deleted protected descendant: %v", err)
	}
	move := ChannelTreeChange{Kind: ChannelMove, ExpectedRevision: 1, ChannelID: 100}
	if _, err := ApplyChannelTreeChange(p, 2, move); !errors.Is(err, ErrRoleForbidden) {
		t.Fatalf("moved protected descendant: %v", err)
	}
	next, err := ApplyChannelTreeChange(p, 1, change)
	if err != nil || len(next.Channels) != 0 || len(p.Channels) != 3 {
		t.Fatalf("subtree removal: %+v %v", next, err)
	}
}

func TestChannelCreationSeparatesTemporaryAndAccessPrivileges(t *testing.T) {
	p := roleFixture()
	p.Roles[1].Permissions = []Capability{CreateTemporaryChannels}
	change := ChannelTreeChange{Kind: ChannelCreate, ExpectedRevision: 1, ChannelID: 200, ParentID: 102, Temporary: true, Access: ChannelPolicy{ChannelID: 200, ParentID: 102, Synced: true}}
	next, err := ApplyChannelTreeChange(p, 3, change)
	if err != nil || len(next.Channels) != 4 {
		t.Fatalf("temporary creation: %+v %v", next, err)
	}
	change.Temporary = false
	if _, err := ApplyChannelTreeChange(p, 3, change); !errors.Is(err, ErrRoleForbidden) {
		t.Fatalf("temporary grant created permanent channel: %v", err)
	}
	change.Temporary = true
	change.Access.Synced = false
	if _, err := ApplyChannelTreeChange(p, 3, change); !errors.Is(err, ErrRoleForbidden) {
		t.Fatalf("custom creation bypassed access authority: %v", err)
	}
	p.Roles[2].Permissions = []Capability{ManageChannels, ManageChannelAccess}
	change.ParentID, change.Access.ParentID = 0, 0
	change.Access.Overrides = []RoleOverride{{RoleID: 10, Capability: ViewChannel, Effect: Deny}}
	if _, err := ApplyChannelTreeChange(p, 2, change); err != nil {
		t.Fatalf("manager cannot create private root: %v", err)
	}
	change.Access.Overrides[0].RoleID = 40
	if _, err := ApplyChannelTreeChange(p, 2, change); !errors.Is(err, ErrRoleForbidden) {
		t.Fatalf("creation bypassed role hierarchy: %v", err)
	}
}

func TestChannelTreeRejectsInvalidOperationsWithoutMutation(t *testing.T) {
	p := roleFixture()
	before := cloneRolePolicy(p)
	for _, change := range []ChannelTreeChange{
		{Kind: ChannelMove, ExpectedRevision: 1, ChannelID: 100, ParentID: 101},
		{Kind: ChannelMove, ExpectedRevision: 1, ChannelID: 101, ParentID: 101},
		{Kind: ChannelCreate, ExpectedRevision: 1, ChannelID: 100, Access: ChannelPolicy{ChannelID: 100}},
		{Kind: ChannelCreate, ExpectedRevision: 1, ChannelID: 200, Access: ChannelPolicy{ChannelID: 201}},
		{Kind: ChannelDelete, ExpectedRevision: 1, ChannelID: 999},
		{Kind: "cleanup", ExpectedRevision: 1, ChannelID: 100},
	} {
		if _, err := ApplyChannelTreeChange(p, 1, change); !errors.Is(err, ErrRoleInvalid) {
			t.Fatalf("accepted %+v: %v", change, err)
		}
		if !reflect.DeepEqual(p, before) {
			t.Fatal("invalid change mutated source")
		}
	}
	if _, err := ApplyChannelTreeChange(p, 0, ChannelTreeChange{Kind: ChannelDelete, ExpectedRevision: 1, ChannelID: 100}); !errors.Is(err, ErrRoleForbidden) {
		t.Fatalf("public cleanup bypass: %v", err)
	}
	if _, err := ApplyChannelTreeChange(p, 1, ChannelTreeChange{Kind: ChannelDelete, ExpectedRevision: 0, ChannelID: 100}); !errors.Is(err, ErrRoleConflict) {
		t.Fatalf("stale change: %v", err)
	}
}

func TestMovingSyncedBranchKeepsItsDescendantsButNotUnrelatedPolicies(t *testing.T) {
	p := roleFixture()
	p.Channels = append(p.Channels, ChannelPolicy{ChannelID: 103, ParentID: 101, Synced: true}, ChannelPolicy{ChannelID: 104, ParentID: 101})
	next, err := ApplyChannelTreeChange(p, 1, ChannelTreeChange{Kind: ChannelMove, ExpectedRevision: 1, ChannelID: 101, ParentID: 102})
	if err != nil {
		t.Fatal(err)
	}
	e, _ := NewRoleEvaluator(next)
	if e.Evaluate(3, 103, ViewChannel).Allowed || !e.Evaluate(3, 104, ViewChannel).Allowed {
		t.Fatal("move changed descendant effective access")
	}
	if !slices.Equal(e.EffectiveOverrides(103), p.Channels[0].Overrides) {
		t.Fatal("synced descendant lost old parent access")
	}
}
