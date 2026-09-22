package store

import "testing"

func TestRoleChannelSettingsRejectNULInEveryTextField(t *testing.T) {
	for _, field := range []string{"name", "topic", "description"} {
		t.Run(field, func(t *testing.T) {
			settings := RoleChannelSettings{Name: "Valid"}
			create := RoleChannelCreate{Name: "Valid", ChannelType: 2}
			switch field {
			case "name":
				settings.Name, create.Name = "bad\x00name", "bad\x00name"
			case "topic":
				settings.Topic, create.Topic = "bad\x00topic", "bad\x00topic"
			case "description":
				settings.Description, create.Description = "bad\x00description", "bad\x00description"
			}
			if settings.Valid() || create.valid() {
				t.Fatal("NUL text accepted")
			}
		})
	}
}
