package server

import (
	"testing"

	"noxa/internal/authorization"
)

func TestRoleManagementProjectionProtectsParentPolicy(t *testing.T) {
	for _, parentVisible := range []bool{false, true} {
		t.Run(map[bool]string{false: "hidden parent", true: "visible unmanaged parent"}[parentVisible], func(t *testing.T) {
			p := serverRoleFixture().policy
			parent := authorization.ChannelPolicy{ChannelID: 1, Overrides: []authorization.RoleOverride{
				{UserID: 99, Capability: authorization.Speak, Effect: authorization.Deny},
			}}
			if parentVisible {
				parent.Overrides = append(parent.Overrides, authorization.RoleOverride{UserID: 3, Capability: authorization.ViewChannel, Effect: authorization.Allow})
			}
			child := authorization.ChannelPolicy{ChannelID: 2, ParentID: 1, Overrides: []authorization.RoleOverride{
				{UserID: 3, Capability: authorization.ViewChannel, Effect: authorization.Allow},
				{UserID: 3, Capability: authorization.ManageChannelAccess, Effect: authorization.Allow},
			}}
			p.Channels = []authorization.ChannelPolicy{parent, child}
			e, err := authorization.NewRoleEvaluator(p)
			if err != nil {
				t.Fatal(err)
			}
			response, err := buildRoleState(t.Context(), e.Policy(), e, 3, 2)
			if err != nil {
				t.Fatal(err)
			}
			if len(response.Policy.Channels) != 1 || len(response.ParentOverrides) != 0 || response.ParentAccessAvailable {
				t.Fatalf("parent policy leaked: %+v", response)
			}
			projected := response.Policy.Channels[0]
			if (projected.ParentID != 0) != parentVisible {
				t.Fatalf("parent reference was not scoped: %+v", projected)
			}
			updated, err := authorization.ApplyRoleChange(p, 3, authorization.RoleChange{Kind: authorization.ChannelAccessSet, ExpectedRevision: 1, Channel: projected})
			if err != nil {
				t.Fatalf("projected child cannot save access: %v", err)
			}
			if updated.Channels[1].ParentID != 1 {
				t.Fatal("access change reparented channel")
			}
			owner, err := buildRoleState(t.Context(), e.Policy(), e, p.OwnerID, 2)
			if err != nil || !owner.ParentAccessAvailable || len(owner.ParentOverrides) == 0 {
				t.Fatalf("owner lost parent policy: %+v %v", owner, err)
			}
		})
	}
}
