package main

import (
	"noxa/internal/query"
	"reflect"
	"testing"
)

func TestQueryBackendExposesOnlyRoleIntegrationMethods(t *testing.T) {
	var _ query.Backend = (*queryBackend)(nil)
	for _, name := range []string{"CreateChannel", "DeleteChannel", "ListChannels", "KickClient", "BanClient", "Authenticate"} {
		if _, ok := reflect.TypeFor[*queryBackend]().MethodByName(name); ok {
			t.Errorf("query backend still exposes retired method %s", name)
		}
	}
}

// Optional role features are discovered by type assertion, so a missing
// delegator silently disables the feature for ServerQuery and gRPC alike.
func TestQueryBackendImplementsEveryRoleFeature(t *testing.T) {
	var (
		_ query.RoleAuditBackend            = (*queryBackend)(nil)
		_ query.RoleBanInspectionBackend    = (*queryBackend)(nil)
		_ query.RoleChannelBackend          = (*queryBackend)(nil)
		_ query.RoleComplaintBackend        = (*queryBackend)(nil)
		_ query.RoleConfigBackend           = (*queryBackend)(nil)
		_ query.RoleCustomMetadataBackend   = (*queryBackend)(nil)
		_ query.RoleDeviceBackend           = (*queryBackend)(nil)
		_ query.RoleEventBackend            = (*queryBackend)(nil)
		_ query.RoleFilterBackend           = (*queryBackend)(nil)
		_ query.RoleInspectionBackend       = (*queryBackend)(nil)
		_ query.RoleManagementBackend       = (*queryBackend)(nil)
		_ query.RoleMediaLimitsBackend      = (*queryBackend)(nil)
		_ query.RoleMetadataBackend         = (*queryBackend)(nil)
		_ query.RoleVoiceModerationBackend  = (*queryBackend)(nil)
		_ query.RoleMemberMoveBackend       = (*queryBackend)(nil)
		_ query.RoleMemberDisconnectBackend = (*queryBackend)(nil)
		_ query.RoleMemberRemovalBackend    = (*queryBackend)(nil)
		_ query.RoleRulesBackend            = (*queryBackend)(nil)
		_ query.RoleTextBackend             = (*queryBackend)(nil)
	)
}
