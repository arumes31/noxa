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
		if (RoleChannelCreate{Name: "general", ChannelType: channelType, TextOnly: true, PasswordHash: "hash"}).valid() {
			t.Fatalf("type %d passworded text channel accepted", channelType)
		}
		if !(RoleChannelCreate{Name: "general", ChannelType: channelType, PasswordHash: "hash"}).valid() {
			t.Fatalf("type %d passworded voice channel rejected", channelType)
		}
	}
}
