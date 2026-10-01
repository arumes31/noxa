package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

func validateE2EProfile(o options) error {
	switch o.authorizationModel {
	case netproto.AuthorizationModelRolesV1:
		if o.chaos {
			return errors.New("-chaos is not supported by the roles-v1 checklist")
		}
		if o.filePayload <= 0 || o.filePayload > 16<<20 {
			return errors.New("role file payload must be between 1 byte and 16 MiB")
		}
		return nil
	default:
		return errors.New("-authorization-model must be roles-v1")
	}
}

// runRoleChecks stops at the first failed stage. All fixture and authentication
// checks precede test-policy mutations; each scenario owns its cleanup.
func runRoleChecks(o options) int {
	c := &checkCtx{opts: o}
	defer c.close()
	var s *roleScenario
	var owner, alice *querySession
	defer func() {
		for _, q := range []*querySession{alice, owner} {
			if q != nil {
				_ = q.conn.Close()
			}
		}
	}()
	var initial authorization.RolePolicy
	checks := []check{
		{"healthz", checkHealthz}, {"readyz", checkReadyz},
		{"metrics", checkMetrics}, {"udp-ping-pong", checkUDP},
		{"role-fixture-preflight", func(*checkCtx) error {
			var err error
			owner, err = dialQuery(o.queryAddr, o.adminUID, o.adminPass, netproto.AuthorizationModelRolesV1)
			if err != nil {
				return err
			}
			s, err = prepareRoleScenario(owner, o.aliceUID, o.bobUID)
			if err != nil {
				return err
			}
			initial, err = s.policy()
			if err != nil {
				return err
			}
			if err := checkRoleProfileBaseline(s, initial); err != nil {
				return err
			}
			alice, err = dialQuery(o.queryAddr, o.aliceUID, o.alicePass, netproto.AuthorizationModelRolesV1)
			return err
		}},
		{"role-native-authentication", checkRoleNativeLogin},
		{"role-lifecycle", func(*checkCtx) error { return checkRoleLifecycle(s) }},
		{"role-hierarchy-and-revocation", func(*checkCtx) error { return checkRoleHierarchy(s, alice) }},
		{"role-channel-and-access", func(*checkCtx) error { return checkRoleChannelLifecycle(s) }},
		{"role-native-chat-and-files", func(c *checkCtx) error { return checkRoleNativeSessionTraffic(s, c) }},
		{"role-policy-restored", func(*checkCtx) error {
			final, err := s.policy()
			if err != nil {
				return err
			}
			return verifyRolePolicyRestored(initial, final)
		}},
	}
	for i, check := range checks {
		if err := check.run(c); err != nil {
			fmt.Printf("FAIL %s: %v\n%d/%d role checks passed; stopped before later checks\n", check.name, err, i, len(checks))
			return 1
		}
		fmt.Printf("PASS %s\n", check.name)
	}
	fmt.Printf("%d/%d role checks passed\n", len(checks), len(checks))
	return 0
}

func checkRoleProfileBaseline(s *roleScenario, policy authorization.RolePolicy) error {
	evaluator, err := authorization.NewRoleEvaluator(policy)
	if err != nil {
		return err
	}
	for _, userID := range []int64{0, s.alice.UserID, s.bob.UserID} {
		for _, capability := range []authorization.Capability{authorization.ViewChannel, authorization.ReadHistory, authorization.SendMessages} {
			if !evaluator.Evaluate(userID, 0, capability).Allowed {
				return fmt.Errorf("role fixture user %d requires public %s", userID, capability)
			}
		}
		for _, capability := range []authorization.Capability{authorization.Administrator, authorization.ManageRoles, authorization.ManageChannels, authorization.ManageMessages, authorization.BypassSlowmode} {
			if evaluator.Evaluate(userID, 0, capability).Allowed {
				return fmt.Errorf("role fixture user %d must not have %s", userID, capability)
			}
		}
	}
	return nil
}

func verifyRolePolicyRestored(initial, final authorization.RolePolicy) error {
	before, err := canonicalRolePolicy(initial)
	if err != nil {
		return err
	}
	after, err := canonicalRolePolicy(final)
	if err != nil {
		return err
	}
	if before != after {
		return errors.New("final role policy differs from the initial policy; inspect test resources")
	}
	return nil
}

// Order of grant sets and database rows has no policy meaning. Clone through
// the validator before normalizing so the caller's snapshots remain intact.
func canonicalRolePolicy(policy authorization.RolePolicy) (string, error) {
	evaluator, err := authorization.NewRoleEvaluator(policy)
	if err != nil {
		return "", err
	}
	p := evaluator.Policy()
	p.Revision = 1
	// Role deletion compacts sparse positions; only relative rank carries
	// authority. Preserve that order while ignoring gaps in numeric positions.
	slices.SortFunc(p.Roles, func(a, b authorization.Role) int { return cmp.Compare(a.Position, b.Position) })
	for i := range p.Roles {
		p.Roles[i].Position = i
	}
	slices.SortFunc(p.Roles, func(a, b authorization.Role) int { return cmp.Compare(a.ID, b.ID) })
	for i := range p.Roles {
		slices.Sort(p.Roles[i].Permissions)
	}
	slices.SortFunc(p.Members, func(a, b authorization.RoleMember) int { return cmp.Compare(a.UserID, b.UserID) })
	for i := range p.Members {
		slices.Sort(p.Members[i].RoleIDs)
	}
	slices.SortFunc(p.Channels, func(a, b authorization.ChannelPolicy) int { return cmp.Compare(a.ChannelID, b.ChannelID) })
	for i := range p.Channels {
		slices.SortFunc(p.Channels[i].Overrides, func(a, b authorization.RoleOverride) int {
			return cmp.Or(cmp.Compare(a.RoleID, b.RoleID), cmp.Compare(a.UserID, b.UserID), cmp.Compare(a.Capability, b.Capability))
		})
	}
	data, err := json.Marshal(p)
	return string(data), err
}
