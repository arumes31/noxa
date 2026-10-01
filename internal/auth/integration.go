package auth

import (
	"context"
	"errors"
	"fmt"
	"net/netip"

	"github.com/lib/pq"
)

// ErrIntegrationDenied deliberately hides account eligibility and ban details.
var ErrIntegrationDenied = errors.New("integration authentication denied")

// IntegrationPrincipal proves password authentication by this process's auth
// service. It carries identity only, never cached roles or permission grants.
// Fields are private so transport request bodies cannot construct a principal.
// Transports must discard it at logout/disconnect and validate it on each action.
type IntegrationPrincipal struct {
	issuer   *AuthService
	userID   int64
	uniqueID string
	remoteIP string
}

func (p IntegrationPrincipal) UserID() int64    { return p.userID }
func (p IntegrationPrincipal) UniqueID() string { return p.uniqueID }

// AuthenticateIntegration resolves a canonical account using one password
// verification. remoteIP must come from the transport peer, never the request.
// Integration eligibility is independent of legacy admin/bot flags and roles.
func (a *AuthService) AuthenticateIntegration(ctx context.Context, identifier, password, remoteIP string) (IntegrationPrincipal, error) {
	u, err := a.AuthenticateIdentifier(ctx, identifier, password)
	if errors.Is(err, ErrUserNotFound) || (err == nil && u == nil) {
		return IntegrationPrincipal{}, ErrIntegrationDenied
	}
	if err != nil {
		return IntegrationPrincipal{}, err
	}
	ip, err := netip.ParseAddr(remoteIP)
	if err != nil || ip.Zone() != "" || ip.IsUnspecified() {
		return IntegrationPrincipal{}, ErrIntegrationDenied
	}
	p := IntegrationPrincipal{issuer: a, userID: u.ID, uniqueID: u.UniqueID, remoteIP: ip.Unmap().String()}
	if err := a.ValidateIntegration(ctx, p); err != nil {
		return IntegrationPrincipal{}, err
	}
	return p, nil
}

// ValidateIntegration rechecks the account and server-wide bans in one database
// snapshot. Call inside the operation's authority lease, before its bounded
// effect. Offline eligibility changes require a stopped serving process; live
// bans must serialize with the protected operation.
func (a *AuthService) ValidateIntegration(ctx context.Context, p IntegrationPrincipal) error {
	if p.issuer != a || p.userID <= 0 || p.uniqueID == "" || p.remoteIP == "" {
		return ErrIntegrationDenied
	}
	const q = `SELECT EXISTS (
		SELECT 1 FROM users u
		WHERE u.id = $1 AND u.unique_id = $2 AND u.integration_enabled
		AND NOT EXISTS (
			SELECT 1 FROM bans b WHERE b.channel_id IS NULL
			AND b.ban_type = 1 AND b.value = $2
			AND (b.expires_at IS NULL OR b.expires_at > NOW())
		)
	), ARRAY(SELECT value FROM bans WHERE channel_id IS NULL AND ban_type = 0
		AND (expires_at IS NULL OR expires_at > NOW()))`
	var eligible bool
	var bannedIPs pq.StringArray
	if err := a.store.DB().QueryRowContext(ctx, q, p.userID, p.uniqueID).Scan(&eligible, &bannedIPs); err != nil {
		return fmt.Errorf("validating integration identity: %w", err)
	}
	if !eligible {
		return ErrIntegrationDenied
	}
	for _, value := range bannedIPs {
		// Historical bans are text, not inet. Parse without casting in SQL so
		// malformed history cannot break login, and equivalent IPv6/mapped
		// IPv4 spellings still match the canonical transport peer.
		if address, err := netip.ParseAddr(value); err == nil && address.Unmap().String() == p.remoteIP {
			return ErrIntegrationDenied
		}
	}
	return nil
}
