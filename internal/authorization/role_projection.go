package authorization

import "context"

// BoundedPolicy limits the complete nested management projection before cloning
// it. Grants, member assignments and overrides count toward the item budget.
func (e *RoleEvaluator) BoundedPolicy(ctx context.Context, limit int) (RolePolicy, error) {
	consume := func(count int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if count > limit {
			return ErrAuthorizationUnavailable
		}
		limit -= count
		return nil
	}
	for _, count := range []int{len(e.policy.Roles), len(e.policy.Members), len(e.policy.Channels)} {
		if err := consume(count); err != nil {
			return RolePolicy{}, err
		}
	}
	for _, role := range e.policy.Roles {
		if err := consume(len(role.Permissions)); err != nil {
			return RolePolicy{}, err
		}
	}
	for _, member := range e.policy.Members {
		if err := consume(len(member.RoleIDs)); err != nil {
			return RolePolicy{}, err
		}
	}
	for _, channel := range e.policy.Channels {
		if err := consume(len(channel.Overrides)); err != nil {
			return RolePolicy{}, err
		}
	}
	return e.Policy(), nil
}
