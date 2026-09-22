package authorization

import "slices"

// RevokedReadScopes includes offline identities and individual exceptions, not
// just connected members. Guest zero represents the implicit everyone-only
// identity class. Channel zero is the server-wide chat scope.
func RevokedReadScopes(before, after *RoleEvaluator) []int64 {
	users := map[int64]bool{0: true, before.policy.OwnerID: true, after.policy.OwnerID: true}
	scopes := map[int64]bool{0: true}
	for _, evaluator := range []*RoleEvaluator{before, after} {
		for id := range evaluator.members {
			users[id] = true
		}
		for id, channel := range evaluator.channels {
			scopes[id] = true
			for _, override := range channel.Overrides {
				if override.UserID > 0 {
					users[override.UserID] = true
				}
			}
		}
	}
	var revoked []int64
	for scope := range scopes {
		for user := range users {
			if before.Evaluate(user, scope, ViewChannel).Allowed && !after.Evaluate(user, scope, ViewChannel).Allowed {
				revoked = append(revoked, scope)
				break
			}
		}
	}
	slices.Sort(revoked)
	return revoked
}
