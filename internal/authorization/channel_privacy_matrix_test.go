package authorization

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"testing"
)

// This fixture is also checked against the frontend's emitted preset overrides.
// Expected decisions are explicit, separate from the evaluator under test.
func TestChannelPresetPrivacyMatrix(t *testing.T) {
	var fixture struct {
		Capabilities []Capability `json:"capabilities"`
		Presets      []struct {
			Key              string         `json:"key"`
			Overrides        []RoleOverride `json:"overrides"`
			Denied           []Capability   `json:"denied"`
			AllowedFromEmpty []Capability   `json:"allowed_from_empty"`
		} `json:"presets"`
	}
	data, err := os.ReadFile("../../testdata/channel-access-presets.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Presets) != 4 || len(fixture.Capabilities) != 7 {
		t.Fatal("incomplete fixture")
	}
	for _, preset := range fixture.Presets {
		for _, baseline := range []string{"granted", "empty"} {
			for _, operation := range []string{"set", "customize", "resync", "move_keep", "move_sync", "create_custom", "create_inherit"} {
				t.Run(preset.Key+"/"+baseline+"/"+operation, func(t *testing.T) {
					exceptions := []RoleOverride{{RoleID: 30, Capability: ViewChannel, Effect: Allow}, {UserID: 4, Capability: ViewChannel, Effect: Deny}}
					presetOverrides := append(slices.Clone(preset.Overrides), exceptions...)
					p := RolePolicy{Revision: 1, OwnerID: 1, EveryoneID: 10,
						Roles:   []Role{{ID: 10, Name: "@everyone", Permissions: fixture.Capabilities}, {ID: 30, Name: "Selected", Position: 1}, {ID: 20, Name: "Administrator", Position: 2, Permissions: []Capability{Administrator}}},
						Members: []RoleMember{{UserID: 2, RoleIDs: []int64{20}}, {UserID: 3, RoleIDs: []int64{30}}, {UserID: 4, RoleIDs: []int64{30}}},
						Channels: []ChannelPolicy{{ChannelID: 90, Overrides: exceptions}, {ChannelID: 100, Overrides: presetOverrides},
							{ChannelID: 101, ParentID: 100, Overrides: exceptions}, {ChannelID: 102, ParentID: 101, Synced: true},
							{ChannelID: 103, ParentID: 101}, {ChannelID: 104, ParentID: 103, Synced: true}},
					}
					if baseline == "empty" {
						p.Roles[0].Permissions = nil
					}
					if operation == "customize" {
						p.Channels[2].Synced, p.Channels[2].Overrides = true, nil
					}
					if operation == "move_keep" || operation == "move_sync" {
						p.Channels[2].ParentID, p.Channels[2].Synced, p.Channels[2].Overrides = 90, true, nil
					}
					original := cloneRolePolicy(p)
					role := RoleChange{Kind: ChannelAccessSet, ExpectedRevision: 1, Channel: ChannelPolicy{ChannelID: 101, ParentID: 100, Overrides: presetOverrides}}
					tree := ChannelTreeChange{Kind: ChannelMove, ExpectedRevision: 1, ChannelID: 101, ParentID: 100, SyncToParent: operation == "move_sync"}
					creation := operation == "create_custom" || operation == "create_inherit"
					if creation {
						tree = ChannelTreeChange{Kind: ChannelCreate, ExpectedRevision: 1, ParentID: 100, Access: ChannelPolicy{ParentID: 100, Overrides: presetOverrides}}
					}
					if operation == "resync" {
						role.Channel.Synced, role.Channel.Overrides = true, nil
					}
					if operation == "create_inherit" {
						tree.Access.Synced, tree.Access.Overrides = true, nil
					}
					var next RolePolicy
					useTree := creation || operation == "move_keep" || operation == "move_sync"
					if useTree {
						commit := tree
						if creation {
							commit.ChannelID, commit.Access.ChannelID = 200, 200
						}
						next, err = ApplyChannelTreeChange(p, 1, commit)
					} else {
						next, err = ApplyRoleChange(p, 1, role)
					}
					if err != nil {
						t.Fatal(err)
					}
					e, err := NewRoleEvaluator(next)
					if err != nil {
						t.Fatal(err)
					}
					// Users: anonymous guest, owner, Administrator, allowed role,
					// member denied despite that role, and unassigned account.
					users := []int64{0, 1, 2, 3, 4, 5}
					scopes := []int64{101, 102}
					if creation {
						scopes = []int64{200}
					}
					for _, scope := range scopes {
						var impact ChannelAccessImpact
						if useTree {
							previewScope := scope
							if creation {
								previewScope = 0
							}
							impact, err = PreviewChannelTreeInScope(t.Context(), p, 1, tree, previewScope, users)
						} else {
							impact, err = PreviewChannelAccessInScope(t.Context(), p, 1, role, scope, users)
						}
						if err != nil {
							t.Fatal(err)
						}
						responseScope := scope
						if creation {
							responseScope = 0
						}
						if impact.Revision != 1 || impact.ChannelID != responseScope || len(impact.Members) != len(users) {
							t.Fatalf("scope: %+v", impact)
						}
						for index, user := range users {
							wantChanges := []AccessImpactChange{}
							for _, cap := range Capabilities() {
								if !cap.Channel {
									continue
								}
								base := user == 1 || user == 2 || (user != 4 && baseline == "granted" && slices.Contains(fixture.Capabilities, cap.Key)) || (user == 3 && cap.Key == ViewChannel)
								want := base
								if operation != "move_keep" && user != 1 && user != 2 && (preset.Key != "private" || user != 3) && slices.Contains(preset.Denied, cap.Key) {
									want = false
								}
								if baseline == "empty" && operation != "move_keep" && user != 1 && user != 2 {
									want = user != 4 && (slices.Contains(preset.AllowedFromEmpty, cap.Key) || (user == 3 && cap.Key == ViewChannel))
								}
								if actual := e.Evaluate(user, scope, cap.Key).Allowed; actual != want {
									t.Fatalf("channel %d user %d %s: got %v want %v", scope, user, cap.Key, actual, want)
								}
								before := base
								if creation {
									before = false
								} else if operation == "customize" {
									before = want
								}
								if before != want {
									wantChanges = append(wantChanges, AccessImpactChange{Capability: cap.Key, Before: before, After: want})
								}
							}
							if impact.Members[index].UserID != user || !reflect.DeepEqual(impact.Members[index].Changes, wantChanges) {
								t.Fatalf("channel %d user %d impact: %+v want %+v", scope, user, impact.Members[index], wantChanges)
							}
						}
					}
					for _, scope := range []int64{103, 104} {
						for _, user := range users {
							for _, cap := range fixture.Capabilities {
								if e.Evaluate(user, scope, cap).Allowed != (baseline == "granted" || user == 1 || user == 2) {
									t.Fatalf("changed independent custom branch: channel %d user %d %s", scope, user, cap)
								}
							}
						}
					}
					if operation == "customize" {
						if ch := e.channels[101]; ch.Synced || !reflect.DeepEqual(ch.Overrides, presetOverrides) {
							t.Fatalf("customization did not detach: %+v", ch)
						}
						parentChange := RoleChange{Kind: ChannelAccessSet, ExpectedRevision: next.Revision, Channel: ChannelPolicy{ChannelID: 100, Overrides: []RoleOverride{{RoleID: 10, Capability: ViewChannel, Effect: Deny}}}}
						changed, err := ApplyRoleChange(next, 1, parentChange)
						if err != nil {
							t.Fatal(err)
						}
						changedEval, err := NewRoleEvaluator(changed)
						if err != nil {
							t.Fatal(err)
						}
						if !e.Evaluate(3, 100, ViewChannel).Allowed || changedEval.Evaluate(3, 100, ViewChannel).Allowed {
							t.Fatal("parent change did not restrict selected role")
						}
						for _, scope := range []int64{101, 102} {
							for _, user := range users {
								for _, cap := range Capabilities() {
									if cap.Channel && e.Evaluate(user, scope, cap.Key).Allowed != changedEval.Evaluate(user, scope, cap.Key).Allowed {
										t.Fatalf("custom branch still followed parent: channel %d user %d %s", scope, user, cap.Key)
									}
								}
							}
						}
					}
					if !reflect.DeepEqual(p, original) {
						t.Fatal("mutated original policy")
					}
				})
			}
		}
	}
}
