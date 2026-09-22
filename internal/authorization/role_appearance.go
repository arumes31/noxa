package authorization

import "slices"

// RoleAppearance contains public cosmetics only, never grants or overrides.
type RoleAppearance struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Position int    `json:"position"`
	Color    string `json:"color,omitempty"`
	Icon     string `json:"icon,omitempty"`
	Hoist    bool   `json:"hoist,omitempty"`
}

// MemberAppearance returns assigned roles from highest to lowest. Everyone is
// implicit and has no chip; presentation order never participates in evaluation.
func (e *RoleEvaluator) MemberAppearance(userID int64) []RoleAppearance {
	roles := make([]RoleAppearance, 0, len(e.members[userID]))
	for _, id := range e.members[userID] {
		r := e.roles[id]
		roles = append(roles, RoleAppearance{ID: r.ID, Name: r.Name, Position: r.Position, Color: r.Color, Icon: r.Icon, Hoist: r.Hoist})
	}
	slices.SortFunc(roles, func(a, b RoleAppearance) int {
		if a.Position > b.Position {
			return -1
		}
		if a.Position < b.Position {
			return 1
		}
		return 0
	})
	return roles
}
