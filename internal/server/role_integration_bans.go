package server

import (
	"context"

	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"noxa/internal/store"
)

type banPageStore interface {
	ListBanPage(context.Context, int64, int) ([]store.BanRecord, error)
}

// WithIntegrationBans retains current admission, BanMembers and metadata locks
// through the bounded delivery callback, like other protected integration reads.
func (s *TCPServer) WithIntegrationBans(ctx context.Context, principal auth.IntegrationPrincipal, query netproto.BanQuery, deliver func(context.Context, netproto.BanPage) error) error {
	if deliver == nil || query.BeforeID < 0 || query.Limit < 0 || query.Limit > 100 {
		return authorization.ErrRoleInvalid
	}
	if query.Limit == 0 {
		query.Limit = 50
	}
	return s.withIntegrationPolicy(ctx, principal, func(ctx context.Context, e *authorization.RoleEvaluator) error {
		if !e.Evaluate(principal.UserID(), 0, authorization.BanMembers).Allowed {
			return authorization.ErrRoleForbidden
		}
		bans, ok := s.deps.BanAdmin.(banPageStore)
		if !ok {
			return authorization.ErrAuthorizationUnavailable
		}
		rows, err := bans.ListBanPage(ctx, query.BeforeID, query.Limit)
		if err != nil {
			return err
		}
		page := netproto.BanPage{Bans: make([]netproto.BanEntry, 0, query.Limit)}
		if len(rows) > query.Limit {
			rows = rows[:query.Limit]
			page.NextBeforeID = rows[len(rows)-1].ID
		}
		for _, b := range rows {
			entry := netproto.BanEntry{ID: b.ID, Type: b.Type, Value: b.Value, Reason: b.Reason,
				BannedBy: b.BannedBy, CreatedAt: b.CreatedAt.Unix()}
			if b.ExpiresAt != nil {
				entry.ExpiresAt = b.ExpiresAt.Unix()
			}
			page.Bans = append(page.Bans, entry)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return deliver(ctx, page)
	})
}
