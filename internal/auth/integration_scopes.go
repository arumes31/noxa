package auth

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/lib/pq"
)

// Integration scopes (807) are a ceiling on top of roles-v1: an integration
// call needs both its scope and the role permission the operation checks.
// An account without stored scopes keeps full integration access.
const (
	ScopeServerRead     = "server:read"
	ScopeServerManage   = "server:manage"
	ScopeRolesRead      = "roles:read"
	ScopeRolesManage    = "roles:manage"
	ScopeChannelsManage = "channels:manage"
	ScopeMembersVoice   = "members:voice"
	ScopeMembersMove    = "members:move"
	ScopeMembersKick    = "members:kick"
	ScopeMembersBan     = "members:ban"
	ScopeDevicesManage  = "devices:manage"
	ScopeAuditRead      = "audit:read"
	ScopeComplaints     = "complaints:manage"
	ScopeEventsRead     = "events:read"
	ScopeMessagesManage = "messages:manage"
)

// IntegrationScopes lists every scope, in a stable order.
var IntegrationScopes = []string{
	ScopeServerRead, ScopeServerManage, ScopeRolesRead, ScopeRolesManage,
	ScopeChannelsManage, ScopeMembersVoice, ScopeMembersMove, ScopeMembersKick,
	ScopeMembersBan, ScopeDevicesManage, ScopeAuditRead, ScopeComplaints,
	ScopeEventsRead, ScopeMessagesManage,
}

// ErrIntegrationScopeInvalid reports an unknown or duplicated scope.
var ErrIntegrationScopeInvalid = errors.New("invalid integration scope")

// ErrIntegrationNotFound reports a target that is not an integration account.
var ErrIntegrationNotFound = errors.New("integration account not found")

// NormalizeIntegrationScopes validates scopes and returns them in the stable
// IntegrationScopes order. An empty list is valid and allows nothing.
func NormalizeIntegrationScopes(scopes []string) ([]string, error) {
	if len(scopes) > len(IntegrationScopes) {
		return nil, ErrIntegrationScopeInvalid
	}
	seen := make(map[string]bool, len(scopes))
	for _, scope := range scopes {
		if seen[scope] || !slices.Contains(IntegrationScopes, scope) {
			return nil, ErrIntegrationScopeInvalid
		}
		seen[scope] = true
	}
	out := make([]string, 0, len(scopes))
	for _, scope := range IntegrationScopes {
		if seen[scope] {
			out = append(out, scope)
		}
	}
	return out, nil
}

// AllowsScope reports whether the principal's scope ceiling admits scope.
// It never implies the role permission the operation also needs.
func (p IntegrationPrincipal) AllowsScope(scope string) bool {
	return !p.scoped || slices.Contains(p.scopes, scope)
}

// Scopes returns the principal's scopes and whether it is scoped at all
// (false means full legacy access).
func (p IntegrationPrincipal) Scopes() ([]string, bool) {
	return slices.Clone(p.scopes), p.scoped
}

// loadIntegrationScopes reads the stored ceiling. scoped is false for NULL.
func (a *AuthService) loadIntegrationScopes(ctx context.Context, userID int64) (scopes []string, scoped bool, err error) {
	var list pq.StringArray
	const q = `SELECT integration_scopes IS NOT NULL, COALESCE(integration_scopes, '{}') FROM users WHERE id = $1`
	if err := a.store.DB().QueryRowContext(ctx, q, userID).Scan(&scoped, &list); err != nil {
		return nil, false, fmt.Errorf("reading integration scopes: %w", err)
	}
	if !scoped {
		return nil, false, nil
	}
	// Stored values pass through normalization, so an unknown historical
	// value narrows instead of widening.
	known := make([]string, 0, len(list))
	for _, scope := range IntegrationScopes {
		if slices.Contains(list, scope) {
			known = append(known, scope)
		}
	}
	return known, true, nil
}

// IntegrationScopeState is an integration account's current ceiling.
type IntegrationScopeState struct {
	UniqueID string
	Full     bool
	Scopes   []string
}

// GetIntegrationScopes reads the ceiling of an integration-enabled account.
func (a *AuthService) GetIntegrationScopes(ctx context.Context, uniqueID string) (IntegrationScopeState, error) {
	var id int64
	const q = `SELECT id FROM users WHERE unique_id = $1 AND integration_enabled`
	if err := a.store.DB().QueryRowContext(ctx, q, uniqueID).Scan(&id); err != nil {
		return IntegrationScopeState{}, ErrIntegrationNotFound
	}
	scopes, scoped, err := a.loadIntegrationScopes(ctx, id)
	if err != nil {
		return IntegrationScopeState{}, err
	}
	return IntegrationScopeState{UniqueID: uniqueID, Full: !scoped, Scopes: scopes}, nil
}

// SetIntegrationScopes stores a ceiling (full=true clears it). Active
// integration sessions of the account fail their next validation and must
// sign in again, so a narrowed ceiling applies at once.
func (a *AuthService) SetIntegrationScopes(ctx context.Context, uniqueID string, full bool, scopes []string) (IntegrationScopeState, error) {
	var value any
	if !full {
		normalized, err := NormalizeIntegrationScopes(scopes)
		if err != nil {
			return IntegrationScopeState{}, err
		}
		scopes = normalized
		value = pq.StringArray(normalized)
	} else {
		scopes = nil
	}
	const q = `UPDATE users SET integration_scopes = $2 WHERE unique_id = $1 AND integration_enabled`
	res, err := a.store.DB().ExecContext(ctx, q, uniqueID, value)
	if err != nil {
		return IntegrationScopeState{}, fmt.Errorf("storing integration scopes: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return IntegrationScopeState{}, ErrIntegrationNotFound
	}
	return IntegrationScopeState{UniqueID: uniqueID, Full: full, Scopes: scopes}, nil
}
