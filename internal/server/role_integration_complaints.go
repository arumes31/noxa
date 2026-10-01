package server

import (
	"context"
	"fmt"
	"time"

	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"noxa/internal/store"
)

type complaintPageStore interface {
	ListComplaintPage(context.Context, int64, int) ([]store.Complaint, error)
}

// WithIntegrationComplaints applies the native global BanMembers gate and
// nickname projection, retaining current admission and policy through delivery.
func (s *TCPServer) WithIntegrationComplaints(ctx context.Context, principal auth.IntegrationPrincipal, request netproto.ComplaintQuery, deliver func(context.Context, netproto.ComplaintPage) error) error {
	if deliver == nil || request.AfterID < 0 || request.Limit < 0 || request.Limit > 100 {
		return authorization.ErrRoleInvalid
	}
	if request.Limit == 0 {
		request.Limit = 50
	}
	return s.withIntegrationPolicy(ctx, principal, func(ctx context.Context, e *authorization.RoleEvaluator) error {
		if !e.Evaluate(principal.UserID(), 0, authorization.BanMembers).Allowed {
			return authorization.ErrRoleForbidden
		}
		backend, ok := s.deps.Complaints.(complaintPageStore)
		if !ok {
			return authorization.ErrAuthorizationUnavailable
		}
		rows, err := backend.ListComplaintPage(ctx, request.AfterID, request.Limit)
		if err != nil {
			return err
		}
		page := netproto.ComplaintPage{Entries: []netproto.ComplaintPageEntry{}}
		if len(rows) > request.Limit {
			rows = rows[:request.Limit]
			page.NextAfterID = rows[len(rows)-1].ID
		}
		projected, err := s.complaintRowsResponse(ctx, rows)
		if err != nil {
			return err
		}
		for i, entry := range projected.Entries {
			page.Entries = append(page.Entries, netproto.ComplaintPageEntry{ID: rows[i].ID, ComplaintEntry: entry})
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return deliver(ctx, page)
	})
}

// ClearIntegrationComplaints resolves one target's complaints without doing a
// new protected read after deletion. Repeating a successful clear is harmless.
func (s *TCPServer) ClearIntegrationComplaints(ctx context.Context, principal auth.IntegrationPrincipal, request netproto.ComplaintClear) (netproto.ComplaintClearResult, error) {
	if request.TargetUniqueID == "" {
		return netproto.ComplaintClearResult{}, authorization.ErrRoleInvalid
	}
	var result netproto.ComplaintClearResult
	err := s.withIntegrationPolicy(ctx, principal, func(ctx context.Context, e *authorization.RoleEvaluator) error {
		if !e.Evaluate(principal.UserID(), 0, authorization.BanMembers).Allowed {
			return authorization.ErrRoleForbidden
		}
		var err error
		result.Deleted, err = s.clearComplaints(ctx, principal.UniqueID(), request)
		return err
	})
	return result, err
}

// Callers hold their validated management lease. A completed delete receives a
// separate bounded audit attempt even if its operation context just expired.
func (s *TCPServer) clearComplaints(ctx context.Context, actor string, request netproto.ComplaintClear) (int64, error) {
	if s.deps == nil || s.deps.Complaints == nil {
		return 0, authorization.ErrAuthorizationUnavailable
	}
	if request.TargetUniqueID == "" {
		return 0, authorization.ErrRoleInvalid
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	n, err := s.deps.Complaints.DeleteComplaintsAgainst(ctx, request.TargetUniqueID, request.FromUniqueID)
	if err != nil || n == 0 {
		return n, err
	}
	detail := fmt.Sprintf("count=%d", n)
	if request.FromUniqueID != "" {
		detail = fmt.Sprintf("from=%s count=%d", request.FromUniqueID, n)
	}
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	s.audit(auditCtx, actor, "complaint_clear", request.TargetUniqueID, detail)
	return n, nil
}
