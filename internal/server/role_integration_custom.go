package server

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"noxa/internal/store"
)

// CustomMetadataStore stores opaque management annotations. Values never
// participate in account authentication, role assignment or capability checks.
type CustomMetadataStore interface {
	ListCustomPage(context.Context, string, string, int) ([]store.CustomEntry, error)
	CustomSet(context.Context, string, string, string) error
	CustomDel(context.Context, string, string) error
}

func (s *TCPServer) WithIntegrationCustomMetadata(ctx context.Context, principal auth.IntegrationPrincipal, request netproto.CustomMetadataQuery, deliver func(context.Context, netproto.CustomMetadataPage) error) error {
	if !request.Valid() || deliver == nil {
		return authorization.ErrRoleInvalid
	}
	if request.Limit == 0 {
		request.Limit = 50
	}
	return s.withIntegrationPolicy(ctx, principal, func(ctx context.Context, e *authorization.RoleEvaluator) error {
		if !e.Evaluate(principal.UserID(), 0, authorization.ManageServer).Allowed {
			return authorization.ErrRoleForbidden
		}
		if s.deps.CustomMetadata == nil {
			return authorization.ErrAuthorizationUnavailable
		}
		rows, err := s.deps.CustomMetadata.ListCustomPage(ctx, request.UniqueID, request.AfterKey, request.Limit)
		if err != nil {
			return err
		}
		page := netproto.CustomMetadataPage{UniqueID: request.UniqueID, Entries: []netproto.CustomMetadataEntry{}}
		if len(rows) > request.Limit {
			rows = rows[:request.Limit]
			page.NextAfterKey = rows[len(rows)-1].Key
		}
		for _, row := range rows {
			if !(netproto.CustomMetadataChange{UniqueID: request.UniqueID, Key: row.Key, Value: &row.Value}).Valid() {
				return authorization.ErrAuthorizationUnavailable
			}
			page.Entries = append(page.Entries, netproto.CustomMetadataEntry{Key: row.Key, Value: row.Value})
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return deliver(ctx, page)
	})
}

func (s *TCPServer) ChangeIntegrationCustomMetadata(ctx context.Context, principal auth.IntegrationPrincipal, request netproto.CustomMetadataChange) (netproto.CustomMetadataResult, error) {
	if !request.Valid() {
		return netproto.CustomMetadataResult{}, authorization.ErrRoleInvalid
	}
	var result netproto.CustomMetadataResult
	err := s.withIntegrationPolicy(ctx, principal, func(ctx context.Context, e *authorization.RoleEvaluator) error {
		if !e.Evaluate(principal.UserID(), 0, authorization.ManageServer).Allowed {
			return authorization.ErrRoleForbidden
		}
		if s.deps.CustomMetadata == nil {
			return authorization.ErrAuthorizationUnavailable
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		var err error
		action, valueBytes := "custom_metadata_delete", 0
		if request.Delete {
			err = s.deps.CustomMetadata.CustomDel(ctx, request.UniqueID, request.Key)
		} else {
			action, valueBytes = "custom_metadata_set", len(*request.Value)
			err = s.deps.CustomMetadata.CustomSet(ctx, request.UniqueID, request.Key, *request.Value)
		}
		if err != nil {
			return err
		}
		result = netproto.CustomMetadataResult{UniqueID: request.UniqueID, Key: request.Key, Deleted: request.Delete}
		auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		s.audit(auditCtx, principal.UniqueID(), action, request.UniqueID, fmt.Sprintf("key_sha256=%x value_bytes=%d", sha256.Sum256([]byte(request.Key)), valueBytes))
		return nil
	})
	return result, err
}
