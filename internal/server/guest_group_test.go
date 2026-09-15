package server

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"noxa/internal/netproto"
)

type guestAssignableGroups struct{ *fakeGroups }

func (g *guestAssignableGroups) AssignGuestGroup(ctx context.Context, groupType string, groupID, channelID int64, uniqueID, _ string, expiry time.Duration) (int64, error) {
	const userID = 42
	var err error
	if groupType == "channel" {
		err = g.AssignChannelGroup(ctx, groupID, userID, channelID)
	} else {
		err = g.AssignServerGroup(ctx, groupID, userID, expiry)
	}
	g.mu.Lock()
	g.uniqueIDs[userID] = uniqueID
	g.mu.Unlock()
	return userID, err
}

func TestGroupAssignOnlineGuest(t *testing.T) {
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) {
		d.Groups = &guestAssignableGroups{d.Groups.(*fakeGroups)}
	})
	defer env.stop()
	admin, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = admin.Close() }()
	guest, identity := dialGuest(t, env.addr, "new-member", "")
	defer func() { _ = guest.Close() }()
	if !slices.Contains(identity.Capabilities, netproto.CapabilityGroupAssignAck) {
		t.Fatal("server did not advertise group assignment acknowledgements")
	}
	groupID, err := env.groups.CreateGroup(context.Background(), "server", "Member", 0)
	if err != nil {
		t.Fatal(err)
	}
	send(t, admin, netproto.MsgGroupAssign, netproto.GroupAssign{Type: "server", GroupID: groupID, UniqueID: identity.UniqueID})
	data := readEventOfType(t, guest, "group_assigned")
	var event struct {
		Promoted bool `json:"promoted"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatal(err)
	}
	if !event.Promoted || !env.groups.hasServerMember(groupID, 42) {
		t.Fatal("online guest was not promoted and assigned")
	}
}
