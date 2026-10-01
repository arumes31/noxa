package query

import (
	"context"
	"time"

	"noxa/internal/auth"
	"noxa/internal/netproto"
)

type RoleCustomMetadataBackend interface {
	WithIntegrationCustomMetadata(context.Context, auth.IntegrationPrincipal, netproto.CustomMetadataQuery, func(context.Context, netproto.CustomMetadataPage) error) error
	ChangeIntegrationCustomMetadata(context.Context, auth.IntegrationPrincipal, netproto.CustomMetadataChange) (netproto.CustomMetadataResult, error)
}

func (s *Server) executeRoleCustomMetadata(ctx context.Context, sess *session, backend RoleIntegrationBackend, cmd command) bool {
	b, ok := backend.(RoleCustomMetadataBackend)
	if !ok {
		return s.write(sess, errorLine(errInsufficientPermissions, "custom metadata unavailable"))
	}
	if len(cmd.args) != 1 || len(cmd.positional) != 0 {
		return s.write(sess, errorLine(errInvalidParameter, "expected one data=<escaped JSON>"))
	}
	if cmd.name == "customquery" {
		var request netproto.CustomMetadataQuery
		if decodeRoleRequest(cmd.args["data"], &request) != nil || !request.Valid() {
			return s.write(sess, errorLine(errInvalidParameter, "invalid custom metadata page"))
		}
		return s.executeIntegrationJSON(ctx, sess, 10*time.Second, func(ctx context.Context, deliver func(context.Context, any) error) error {
			return b.WithIntegrationCustomMetadata(ctx, sess.principal, request, func(ctx context.Context, page netproto.CustomMetadataPage) error { return deliver(ctx, page) })
		})
	}
	var request netproto.CustomMetadataChange
	if decodeRoleRequest(cmd.args["data"], &request) != nil || !request.Valid() {
		return s.write(sess, errorLine(errInvalidParameter, "custom metadata requires unique_id, key and exactly one of value or delete=true"))
	}
	return s.executeIntegrationJSON(ctx, sess, 10*time.Second, func(ctx context.Context, deliver func(context.Context, any) error) error {
		result, err := b.ChangeIntegrationCustomMetadata(ctx, sess.principal, request)
		if err != nil {
			return err
		}
		return deliverIntegrationCommit(ctx, deliver, result)
	})
}
