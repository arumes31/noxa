package store

import "testing"

func TestRoleChannelCreateRejectsTemporaryTextChannel(t *testing.T) {
	if (RoleChannelCreate{Name: "general", ChannelType: 0, TextOnly: true}).valid() {
		t.Fatal("temporary text channel accepted")
	}
	for _, channelType := range []int{1, 2} {
		if !(RoleChannelCreate{Name: "general", ChannelType: channelType, TextOnly: true}).valid() {
			t.Fatalf("type %d text channel rejected", channelType)
		}
	}
}
