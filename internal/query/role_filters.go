package query

import (
	"context"
	"time"

	"noxa/internal/auth"
	"noxa/internal/netproto"
)

type RoleFilterBackend interface {
	WithIntegrationChatFilters(context.Context, auth.IntegrationPrincipal, func(context.Context, netproto.ChatFilterResponse) error) error
	SetIntegrationChatFilters(context.Context, auth.IntegrationPrincipal, netproto.ChatFilterSet) (netproto.ChatFilterResponse, error)
}

func (s *Server) executeRoleFilters(ctx context.Context, sess *session, backend RoleIntegrationBackend, cmd command) bool {
	b, ok := backend.(RoleFilterBackend)
	if !ok {
		return s.write(sess, errorLine(errInsufficientPermissions, "chat-filter management unavailable"))
	}
	if cmd.name == "chatfilterquery" {
		if len(cmd.args) != 0 || len(cmd.positional) != 0 {
			return s.write(sess, errorLine(errInvalidParameter, "expected chatfilterquery without arguments"))
		}
		return s.executeIntegrationJSON(ctx, sess, 10*time.Second, func(ctx context.Context, deliver func(context.Context, any) error) error {
			return b.WithIntegrationChatFilters(ctx, sess.principal, func(ctx context.Context, result netproto.ChatFilterResponse) error { return deliver(ctx, result) })
		})
	}
	var patch netproto.ChatFilterSet
	if len(cmd.args) != 1 || len(cmd.positional) != 0 || decodeRoleRequest(cmd.args["data"], &patch) != nil || !patch.ValidLists() || (patch.WordFilter == nil && patch.LinkBlacklist == nil && patch.LinkWhitelist == nil) {
		return s.write(sess, errorLine(errInvalidParameter, "expected data=<escaped JSON with at least one filter list>"))
	}
	return s.executeIntegrationJSON(ctx, sess, 10*time.Second, func(ctx context.Context, deliver func(context.Context, any) error) error {
		result, err := b.SetIntegrationChatFilters(ctx, sess.principal, patch)
		if err != nil {
			return err
		}
		return deliverIntegrationCommit(ctx, deliver, result)
	})
}
