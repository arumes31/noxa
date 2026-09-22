package authorization

// StarterRoles returns independent, editable role templates, ordered from
// lowest to highest. Setup assigns IDs; it never assigns members automatically.
func StarterRoles() []Role {
	base := []Capability{ViewChannel, ReadHistory, SendMessages, Connect, Speak}
	moderator := append([]Capability(nil), base...)
	moderator = append(moderator, ManageMessages, MoveMembers, MuteMembers, DeafenMembers, DisconnectMembers, KickMembers)
	return []Role{
		{Name: "Member", Position: 1, Permissions: base},
		{Name: "Moderator", Position: 2, Permissions: moderator},
		{Name: "Administrator", Position: 3, Permissions: []Capability{Administrator}},
	}
}
