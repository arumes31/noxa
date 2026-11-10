package query

import (
	"context"
	"time"

	"noxa/internal/auth"
	"noxa/internal/netproto"
)

// RoleBotBackend serves the bot integration commands: bulk message delete
// (838) and integration scope ceilings (807).
type RoleBotBackend interface {
	BulkDeleteIntegrationMessages(context.Context, auth.IntegrationPrincipal, netproto.MessageBulkDelete) (netproto.MessageBulkDeleteResult, error)
	GetIntegrationScopes(context.Context, auth.IntegrationPrincipal, netproto.IntegrationScopesQuery) (netproto.IntegrationScopes, error)
	SetIntegrationScopes(context.Context, auth.IntegrationPrincipal, netproto.IntegrationScopesSet) (netproto.IntegrationScopes, error)
}

// commandScopes maps every protected ServerQuery command to the integration
// scope it needs (807). A command missing here is refused for scoped
// accounts, so a new command cannot slip past a ceiling unnoticed.
var commandScopes = map[string]string{
	"clientlist": auth.ScopeServerRead, "channellist": auth.ScopeServerRead, "channelinfo": auth.ScopeServerRead,
	"channelquery": auth.ScopeServerRead, "serverinfo": auth.ScopeServerRead, "clientinfo": auth.ScopeServerRead,
	"serverconfig": auth.ScopeServerRead, "medialimits": auth.ScopeServerRead, "rulesquery": auth.ScopeServerRead,
	"chatfilterquery": auth.ScopeServerRead, "customquery": auth.ScopeServerRead,
	"serverconfigset": auth.ScopeServerManage, "medialimitsset": auth.ScopeServerManage, "chatfilterset": auth.ScopeServerManage,
	"servertextset": auth.ScopeServerManage, "broadcast": auth.ScopeServerManage, "ingressshield": auth.ScopeServerManage, "ingressshieldset": auth.ScopeServerManage, "customchange": auth.ScopeServerManage,
	"integrationscopes": auth.ScopeServerManage, "integrationscopesset": auth.ScopeServerManage,
	"rolelist": auth.ScopeRolesRead, "rolemembers": auth.ScopeRolesRead, "accesscheck": auth.ScopeRolesRead,
	"rolechange":    auth.ScopeRolesManage,
	"channelchange": auth.ScopeChannelsManage,
	"membervoice":   auth.ScopeMembersVoice,
	"membermove":    auth.ScopeMembersMove, "memberdisconnect": auth.ScopeMembersMove,
	"memberkick": auth.ScopeMembersKick,
	"memberban":  auth.ScopeMembersBan, "banquery": auth.ScopeMembersBan,
	"devicelist": auth.ScopeDevicesManage, "devicerevoke": auth.ScopeDevicesManage, "devicereset": auth.ScopeDevicesManage,
	"auditquery":     auth.ScopeAuditRead,
	"complaintquery": auth.ScopeComplaints, "complaintclear": auth.ScopeComplaints,
	"messagebulkdelete": auth.ScopeMessagesManage,
}

// CommandScope returns the scope a ServerQuery command needs.
func CommandScope(name string) (string, bool) {
	scope, ok := commandScopes[name]
	return scope, ok
}

// commandAllowedByScope applies the principal's ceiling before any effect.
func commandAllowedByScope(principal auth.IntegrationPrincipal, name string) bool {
	if _, scoped := principal.Scopes(); !scoped {
		return true
	}
	scope, ok := commandScopes[name]
	return ok && principal.AllowsScope(scope)
}

func (s *Server) executeRoleBots(ctx context.Context, sess *session, backend RoleIntegrationBackend, cmd command) bool {
	b, ok := backend.(RoleBotBackend)
	if !ok {
		return s.write(sess, errorLine(errInsufficientPermissions, "bot integration commands unavailable"))
	}
	if len(cmd.args) != 1 || len(cmd.positional) != 0 {
		return s.write(sess, errorLine(errInvalidParameter, "expected data=<escaped JSON>"))
	}
	switch cmd.name {
	case "messagebulkdelete":
		var request netproto.MessageBulkDelete
		if decodeRoleRequest(cmd.args["data"], &request) != nil || !request.Valid() {
			return s.write(sess, errorLine(errInvalidParameter, "expected data=<escaped JSON with channel_id and 1-100 unique message_ids>"))
		}
		return s.executeIntegrationJSON(ctx, sess, 30*time.Second, func(ctx context.Context, deliver func(context.Context, any) error) error {
			result, err := b.BulkDeleteIntegrationMessages(ctx, sess.principal, request)
			if err != nil {
				return err
			}
			return deliverIntegrationCommit(ctx, deliver, result)
		})
	case "integrationscopes":
		var request netproto.IntegrationScopesQuery
		if decodeRoleRequest(cmd.args["data"], &request) != nil || !request.Valid() {
			return s.write(sess, errorLine(errInvalidParameter, "expected data=<escaped JSON with unique_id>"))
		}
		return s.executeIntegrationJSON(ctx, sess, 10*time.Second, func(ctx context.Context, deliver func(context.Context, any) error) error {
			result, err := b.GetIntegrationScopes(ctx, sess.principal, request)
			if err != nil {
				return err
			}
			return deliver(ctx, result)
		})
	default:
		var request netproto.IntegrationScopesSet
		if decodeRoleRequest(cmd.args["data"], &request) != nil || !request.Valid() {
			return s.write(sess, errorLine(errInvalidParameter, "expected data=<escaped JSON with unique_id and scopes, or full=true>"))
		}
		return s.executeIntegrationJSON(ctx, sess, 10*time.Second, func(ctx context.Context, deliver func(context.Context, any) error) error {
			result, err := b.SetIntegrationScopes(ctx, sess.principal, request)
			if err != nil {
				return err
			}
			return deliverIntegrationCommit(ctx, deliver, result)
		})
	}
}
