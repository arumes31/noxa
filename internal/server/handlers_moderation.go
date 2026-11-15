// handlers_moderation.go contains the TCP control moderation handlers.
package server

import (
	"context"
	"net/netip"
	"time"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

// handleKickClient kicks a client from its channel or from the server (and
// optionally records a ban) after a kick/ban power vs needed power check.
func (s *TCPServer) handleKickClient(ctx context.Context, client *Client, f *netproto.Frame) error {
	var msg netproto.KickClient
	if err := netproto.Decode(f, &msg); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "malformed kick_client: "+err.Error())
	}
	if msg.ExpectedChannelID < 0 || ((msg.FromServer || msg.Ban) && msg.ExpectedChannelID != 0) ||
		(msg.AckRequested && !msg.FromServer && !msg.Ban && msg.ExpectedChannelID == 0) {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid channel-disconnect scope; refresh the member list")
	}
	if msg.Ban {
		var result roleBanResult
		err := s.withExclusiveRolePolicy(ctx, func(ctx context.Context) error {
			s.roleMetadataMu.Lock()
			defer s.roleMetadataMu.Unlock()
			if client.sessionRevoked() || client.rulesBlocked() {
				return authorization.ErrRoleForbidden
			}
			var err error
			result, err = s.banRoleMember(ctx, ctx.Value(roleLeaseKey{}).(roleLease).evaluator, client.userID(), client.uniqueID(), client.ID, msg.ClientID, msg.Reason, msg.DurationSeconds)
			return err
		})
		if result.UniqueID != "" && !result.Saved {
			if msg.AckRequested {
				return s.acknowledgeRemoval(client, msg, 0, netproto.BanUnconfirmed, result.Pending)
			}
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "ban persistence could not be confirmed; matching sessions were disconnected; refresh the ban list before retrying")
		}
		if err != nil {
			return s.roleError(ctx, client, err)
		}
		if msg.AckRequested {
			return s.acknowledgeRemoval(client, msg, 0, netproto.BanSaved, result.Pending)
		}
		if result.Pending {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "ban saved and sessions revoked; resource cleanup is pending")
		}
		return nil
	}
	if msg.FromServer {
		var pending bool
		err := s.withExclusiveRolePolicy(ctx, func(ctx context.Context) error {
			s.roleMetadataMu.Lock()
			defer s.roleMetadataMu.Unlock()
			if client.sessionRevoked() || client.rulesBlocked() {
				return authorization.ErrRoleForbidden
			}
			var err error
			pending, err = s.kickRoleMember(ctx, ctx.Value(roleLeaseKey{}).(roleLease).evaluator, client.userID(), client.uniqueID(), client.ID, msg.ClientID, msg.Reason)
			return err
		})
		if err != nil {
			return s.roleError(ctx, client, err)
		}
		return s.acknowledgeRemoval(client, msg, 0, "", pending)
	}
	var result netproto.MemberDisconnectResult
	err := s.withRolePolicy(ctx, func(ctx context.Context) error {
		s.roleMetadataMu.Lock()
		defer s.roleMetadataMu.Unlock()
		if client.sessionRevoked() || client.rulesBlocked() {
			return authorization.ErrRoleForbidden
		}
		e := ctx.Value(roleLeaseKey{}).(roleLease).evaluator
		var err error
		result, err = s.disconnectRoleMember(ctx, e, client.userID(), client.uniqueID(), client.ID, msg.ClientID, msg.ExpectedChannelID, msg.Reason)
		return err
	})
	if err != nil {
		return s.roleError(ctx, client, err)
	}
	return s.acknowledgeRemoval(client, msg, result.ChannelID, "", false)
}

// banExpiration computes a temporary ban's expiry once. A zero result means a
// permanent ban and is deliberately reused for both persistence and event
// publication so the two cannot drift by even a millisecond.
func banExpiration(durationSeconds int64) time.Time {
	if durationSeconds <= 0 {
		return time.Time{}
	}
	return time.Now().UTC().Add(time.Duration(durationSeconds) * time.Second)
}

func persistentBanExpiration(expiresAt time.Time) any {
	if expiresAt.IsZero() {
		return nil
	}
	return expiresAt
}

func banExpirationMillis(expiresAt time.Time) int64 {
	if expiresAt.IsZero() {
		return 0
	}
	return expiresAt.UnixMilli()
}

// insertBan inserts a unique-ID ban into the bans table. expiresAt nil (or
// the nil interface) means a permanent ban. It is a no-op when ban
// persistence is not wired; kicks still proceed. A non-empty guestIP adds an
// IP ban (ban_type 0) in the same statement, so both rows share banned_at:
// store.DeleteBan uses that to lift the IP ban together with the ID ban.
func (s *TCPServer) insertBan(ctx context.Context, uniqueID, guestIP, reason string, bannedBy, expiresAt any) error {
	if s.deps.Bans == nil {
		return nil // ban persistence not wired; kick still proceeds
	}
	q, args := `INSERT INTO bans (ban_type, value, reason, banned_by, expires_at) VALUES (1, $1, $2, $3, $4)`, []any{uniqueID, reason, bannedBy, expiresAt}
	if guestIP != "" {
		q, args = q+`, (0, $5, $2, $3, $4)`, append(args, guestIP)
	}
	_, err := s.deps.Bans.DB().ExecContext(ctx, q, args...)
	return err
}

// guestBanIP is the canonical address to IP-ban with a guest's ID ban (a new
// guest ID costs nothing), or "" for members and for addresses that may be
// a reverse proxy rather than the guest: a web session reports the proxy
// itself when it is loopback/untrusted or sent no X-Forwarded-For.
func (s *TCPServer) guestBanIP(target *Client) string {
	if target.userID() != 0 {
		return ""
	}
	addr, err := netip.ParseAddr(remoteIP(target.Conn))
	if err != nil {
		return ""
	}
	addr = addr.Unmap()
	if target.isWebTransport() {
		if addr.IsLoopback() || addr.IsUnspecified() {
			return ""
		}
		s.configMu.RLock()
		proxies := s.cfg.Web.TrustedProxies
		s.configMu.RUnlock()
		for _, cidr := range proxies {
			if p, err := netip.ParsePrefix(cidr); err == nil && p.Contains(addr) {
				return ""
			}
		}
	}
	return addr.String()
}
