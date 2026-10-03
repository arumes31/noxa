// handlers_auth.go contains the TCP control auth handlers.
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"time"

	"go.uber.org/zap"

	"noxa/internal/auth"
	"noxa/internal/netproto"
)

// authChallengeTTL is how long a pending challenge-response challenge remains
// valid before the client must request a new one.
const authChallengeTTL = 30 * time.Second

// handleAuthenticate starts authentication. Paths:
//   - Password: verified against the users table (registered users).
//   - No password: challenge-response handshake (registered users, and
//     guests proving an Ed25519 identity via AuthSignature.PublicKey).
//   - Anonymous without Username or Password: immediate guest login with an
//     ephemeral guest: unique ID and the requested nickname.
//
// The global server password and ban checks apply to all paths, guests
// included.
func (s *TCPServer) handleAuthenticate(ctx context.Context, client *Client, f *netproto.Frame) error {
	var msg netproto.Authenticate
	if err := netproto.Decode(f, &msg); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "malformed authenticate: "+err.Error())
	}
	if client.isAuthed() {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "already authenticated")
	}
	if !s.negotiateAuthorization(client, msg.AuthorizationModels) {
		return s.rejectAuthorizationModel(client)
	}
	if msg.Nickname != "" {
		name, err := netproto.NormalizeDisplayName(msg.Nickname)
		if err != nil {
			return s.writeMessage(client, netproto.MsgAuthResponse, netproto.AuthResponse{Reason: err.Error()})
		}
		msg.Nickname = name
	}
	// (133) the encryption key is captured before any auth path branches, so
	// finishAuth can seal the global generation and the MOTD into the reply
	// whichever path completes.
	if msg.X25519PublicKey != "" {
		client.setX25519(msg.X25519PublicKey)
	}
	if s.deps == nil || s.deps.Auth == nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "authentication backend unavailable")
	}

	// Reject banned clients before verifying any password. IP bans apply to
	// everyone, including guests, while account bans apply to the presented ID.
	ip := remoteIP(client.Conn)
	serverPasswordScope := auth.LoginFailureScope("tcp-server-password:"+ip, "")
	accountSourceScope := auth.LoginFailureScope("tcp:"+ip, "")
	principalScope := auth.LoginFailureScope("", msg.Username)
	if reason, err := s.banRejectReason(ctx, client, msg.Username, ip); err != nil {
		return s.writeMessage(client, netproto.MsgAuthResponse, netproto.AuthResponse{Reason: "internal error"})
	} else if reason != "" {
		s.metricsSink().IncAuthFailure("tcp", "banned")
		return s.writeMessage(client, netproto.MsgAuthResponse, netproto.AuthResponse{Reason: reason})
	}

	// Global server password: when set, the client must supply it.
	if s.deps.ServerPasswordHash != "" {
		attempt, allowed := s.loginLimiter.Reserve(serverPasswordScope)
		if !allowed {
			s.metricsSink().IncAuthFailure("tcp", "locked_out")
			return s.writeMessage(client, netproto.MsgAuthResponse, netproto.AuthResponse{Reason: "too many failed logins, try again later"})
		}
		defer attempt.Cancel()
		if err := s.verifyServerPassword(msg.ServerPassword, s.deps.ServerPasswordHash); err != nil {
			attempt.Fail()
			s.metricsSink().IncAuthFailure("tcp", "server_password")
			return s.writeMessage(client, netproto.MsgAuthResponse, netproto.AuthResponse{Reason: "invalid server password"})
		}
		attempt.Succeed(serverPasswordScope)
	}

	if msg.Anonymous && msg.Password != "" {
		s.loginLimiter.RecordFailure(accountSourceScope, principalScope)
		s.metricsSink().IncAuthFailure("tcp", "invalid_credentials")
		return s.writeMessage(client, netproto.MsgAuthResponse, netproto.AuthResponse{Reason: "anonymous login takes no password"})
	}

	// Immediate ephemeral guest login.
	if msg.Anonymous && msg.Username == "" && msg.Password == "" {
		return s.completeGuestAuth(ctx, client, newGuestUniqueID(), msg.Nickname)
	}

	// No password: challenge-response handshake.
	if msg.Password == "" {
		// Reserve and release immediately to atomically reject lockouts before
		// issuing a challenge. The signature verification below reserves the
		// same scopes for its entire expensive authentication step.
		attempt, allowed := s.loginLimiter.Reserve(accountSourceScope, principalScope)
		if !allowed {
			s.metricsSink().IncAuthFailure("tcp", "locked_out")
			return s.writeMessage(client, netproto.MsgAuthResponse, netproto.AuthResponse{Reason: "too many failed logins, try again later"})
		}
		attempt.Cancel()
		challenge, err := auth.GenerateChallenge()
		if err != nil {
			s.logger.Warn("challenge generation failed",
				zap.String("client_id", client.ID),
				zap.Error(err),
			)
			return s.writeMessage(client, netproto.MsgAuthResponse, netproto.AuthResponse{Reason: "internal error"})
		}
		client.setChallenge(msg.Username, msg.Nickname, challenge, time.Now().Add(authChallengeTTL))
		return s.writeMessage(client, netproto.MsgAuthChallenge, netproto.AuthChallenge{Challenge: challenge})
	}

	attempt, allowed := s.loginLimiter.Reserve(accountSourceScope, principalScope)
	if !allowed {
		s.metricsSink().IncAuthFailure("tcp", "locked_out")
		return s.writeMessage(client, netproto.MsgAuthResponse, netproto.AuthResponse{Reason: "too many failed logins, try again later"})
	}
	defer attempt.Cancel()

	user, err := s.deps.Auth.AuthenticateIdentifier(ctx, msg.Username, msg.Password)
	if err != nil {
		if errors.Is(err, auth.ErrUserNotFound) {
			attempt.Fail()
			s.metricsSink().IncAuthFailure("tcp", "invalid_credentials")
			return s.writeMessage(client, netproto.MsgAuthResponse, netproto.AuthResponse{Reason: "invalid credentials"})
		}
		s.logger.Warn("authenticate failed",
			zap.String("client_id", client.ID),
			zap.Error(err),
		)
		return s.writeMessage(client, netproto.MsgAuthResponse, netproto.AuthResponse{Reason: "internal error"})
	}
	if user == nil {
		attempt.Fail()
		s.metricsSink().IncAuthFailure("tcp", "invalid_credentials")
		return s.writeMessage(client, netproto.MsgAuthResponse, netproto.AuthResponse{Reason: "invalid credentials"})
	}

	// Bind the client's identity key so future challenge logins work.
	if msg.PublicKey != "" {
		if err := s.deps.Auth.BindPublicKey(ctx, user.ID, msg.PublicKey); err != nil {
			s.logger.Warn("public key binding failed",
				zap.String("client_id", client.ID),
				zap.Error(err),
			)
		}
	}
	attempt.Succeed(principalScope)
	return s.completeAuthForUser(ctx, client, user, msg.Nickname)
}

func (s *TCPServer) verifyServerPassword(password, encodedHash string) error {
	if s.deps != nil && s.deps.VerifyServerPassword != nil {
		return s.deps.VerifyServerPassword(password, encodedHash)
	}
	return auth.VerifyPassword(password, encodedHash)
}

// handleAuthSignature completes the challenge-response handshake: it verifies
// the client's Ed25519 signature over the pending challenge and, on success,
// finishes authentication.
func (s *TCPServer) handleAuthSignature(ctx context.Context, client *Client, f *netproto.Frame) error {
	var msg netproto.AuthSignature
	if err := netproto.Decode(f, &msg); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "malformed auth_signature: "+err.Error())
	}
	if client.isAuthed() {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "already authenticated")
	}
	if client.negotiatedAuthorizationModel() != s.requiredAuthorizationModel() {
		return s.rejectAuthorizationModel(client)
	}
	// The guest/challenge path leaves Authenticate.PublicKey empty and carries
	// its keys here instead, so the encryption key is captured here too (133).
	if msg.X25519PublicKey != "" {
		client.setX25519(msg.X25519PublicKey)
	}
	if s.deps == nil || s.deps.Auth == nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "authentication backend unavailable")
	}

	if msg.PublicKey != "" {
		return s.handleGuestSignature(ctx, client, msg)
	}

	ip := remoteIP(client.Conn)
	sourceScope := auth.LoginFailureScope("tcp:"+ip, "")
	principalScope := auth.LoginFailureScope("", msg.UniqueID)
	attempt, allowed := s.loginLimiter.Reserve(sourceScope, principalScope)
	if !allowed {
		s.metricsSink().IncAuthFailure("tcp", "locked_out")
		return s.writeMessage(client, netproto.MsgAuthResponse, netproto.AuthResponse{Reason: "too many failed logins, try again later"})
	}
	defer attempt.Cancel()

	challenge, nickname, ok := client.takeChallenge(msg.UniqueID)
	if !ok {
		attempt.Fail()
		s.metricsSink().IncAuthFailure("tcp", "invalid_credentials")
		return s.writeMessage(client, netproto.MsgAuthResponse, netproto.AuthResponse{Reason: "no pending challenge"})
	}

	verified, err := s.deps.Auth.AuthenticateChallenge(ctx, msg.UniqueID, challenge, msg.Signature)
	if err != nil {
		if errors.Is(err, auth.ErrUserNotFound) {
			attempt.Fail()
			s.metricsSink().IncAuthFailure("tcp", "invalid_credentials")
			return s.writeMessage(client, netproto.MsgAuthResponse, netproto.AuthResponse{Reason: "invalid credentials"})
		}
		s.logger.Warn("challenge verification failed",
			zap.String("client_id", client.ID),
			zap.Error(err),
		)
		return s.writeMessage(client, netproto.MsgAuthResponse, netproto.AuthResponse{Reason: "internal error"})
	}
	if !verified {
		attempt.Fail()
		s.metricsSink().IncAuthFailure("tcp", "invalid_credentials")
		return s.writeMessage(client, netproto.MsgAuthResponse, netproto.AuthResponse{Reason: "invalid credentials"})
	}

	attempt.Succeed(principalScope)
	return s.completeAuth(ctx, client, msg.UniqueID, nickname)
}

// handleGuestSignature verifies a signature against the presented public
// key and authenticates the client as a registered user (if the derived
// unique ID has a users row) or as a guest with the key-derived unique ID.
func (s *TCPServer) handleGuestSignature(ctx context.Context, client *Client, msg netproto.AuthSignature) error {
	ip := remoteIP(client.Conn)
	sourceScope := auth.LoginFailureScope("tcp:"+ip, "")
	principalScope := auth.LoginFailureScope("", msg.PublicKey)
	uniqueID, err := auth.UniqueIDFromPublicKey(msg.PublicKey)
	if err != nil {
		attempt, allowed := s.loginLimiter.Reserve(sourceScope, principalScope)
		if !allowed {
			s.metricsSink().IncAuthFailure("tcp", "locked_out")
			return s.writeMessage(client, netproto.MsgAuthResponse, netproto.AuthResponse{Reason: "too many failed logins, try again later"})
		}
		attempt.Fail()
		s.metricsSink().IncAuthFailure("tcp", "invalid_credentials")
		return s.writeMessage(client, netproto.MsgAuthResponse, netproto.AuthResponse{Reason: "invalid public key"})
	}
	principalScope = auth.LoginFailureScope("", uniqueID)

	// A guest could bypass the Authenticate-time unique-ID ban check by
	// withholding the ID until now; re-check with the derived ID.
	if reason, err := s.banRejectReason(ctx, client, uniqueID, ip); err != nil {
		return s.writeMessage(client, netproto.MsgAuthResponse, netproto.AuthResponse{Reason: "internal error"})
	} else if reason != "" {
		s.metricsSink().IncAuthFailure("tcp", "banned")
		return s.writeMessage(client, netproto.MsgAuthResponse, netproto.AuthResponse{Reason: reason})
	}
	attempt, allowed := s.loginLimiter.Reserve(sourceScope, principalScope)
	if !allowed {
		s.metricsSink().IncAuthFailure("tcp", "locked_out")
		return s.writeMessage(client, netproto.MsgAuthResponse, netproto.AuthResponse{Reason: "too many failed logins, try again later"})
	}
	defer attempt.Cancel()

	challenge, nickname, ok := client.takeChallenge("")
	if !ok {
		attempt.Fail()
		s.metricsSink().IncAuthFailure("tcp", "invalid_credentials")
		return s.writeMessage(client, netproto.MsgAuthResponse, netproto.AuthResponse{Reason: "no pending challenge"})
	}
	if err := auth.VerifyChallenge(msg.PublicKey, challenge, msg.Signature); err != nil {
		attempt.Fail()
		s.metricsSink().IncAuthFailure("tcp", "invalid_credentials")
		return s.writeMessage(client, netproto.MsgAuthResponse, netproto.AuthResponse{Reason: "invalid credentials"})
	}

	// Registered user with this identity? Then it is a normal login.
	if _, err := s.deps.Auth.LookupUser(ctx, uniqueID); err == nil {
		attempt.Succeed(principalScope)
		return s.completeAuth(ctx, client, uniqueID, nickname)
	} else if !errors.Is(err, auth.ErrUserNotFound) {
		s.logger.Warn("user lookup failed",
			zap.String("client_id", client.ID),
			zap.Error(err),
		)
		return s.writeMessage(client, netproto.MsgAuthResponse, netproto.AuthResponse{Reason: "internal error"})
	}

	// Account with this key bound via a nickname login? The account's unique
	// ID stays canonical even though it differs from the key-derived one.
	if user, err := s.deps.Auth.LookupUserByPublicKey(ctx, msg.PublicKey); err == nil {
		attempt.Succeed(principalScope)
		return s.completeAuthForUser(ctx, client, user, nickname)
	} else if !errors.Is(err, auth.ErrUserNotFound) {
		s.logger.Warn("user lookup by public key failed",
			zap.String("client_id", client.ID),
			zap.Error(err),
		)
		return s.writeMessage(client, netproto.MsgAuthResponse, netproto.AuthResponse{Reason: "internal error"})
	}

	attempt.Succeed(principalScope)
	client.mu.Lock()
	client.verifiedGuestKey = msg.PublicKey
	client.mu.Unlock()
	return s.completeGuestAuth(ctx, client, uniqueID, nickname)
}

// banRejectReason returns the rejection reason when the unique ID or IP is
// actively banned, "" when clear, or an error on lookup failure.
func (s *TCPServer) banRejectReason(ctx context.Context, client *Client, uniqueID, ip string) (string, error) {
	ban, err := s.deps.Auth.LookupActiveBan(ctx, uniqueID, ip)
	if err != nil {
		s.logger.Warn("ban lookup failed",
			zap.String("client_id", client.ID),
			zap.Error(err),
		)
		return "", err
	}
	if ban == nil {
		return "", nil
	}
	reason := "banned"
	if ban.Reason != "" {
		reason = "banned: " + ban.Reason
	}
	s.logger.Info("banned client rejected",
		zap.String("client_id", client.ID),
		zap.String("unique_id", uniqueID),
		zap.String("ip", ip),
		zap.Int64("ban_id", ban.ID),
	)
	return reason, nil
}

// dedupeNickname appends #2, #3, ... when the nickname is already taken by
// an online client.
func (s *TCPServer) dedupeNickname(nickname string) string {
	taken := make(map[string]bool)
	s.mu.RLock()
	for _, c := range s.clients {
		c.mu.RLock()
		if c.authed {
			taken[c.Username] = true
		}
		c.mu.RUnlock()
	}
	s.mu.RUnlock()

	if !taken[nickname] {
		return nickname
	}
	for i := 2; ; i++ {
		suffix := fmt.Sprintf("#%d", i)
		base := []rune(nickname)
		if limit := netproto.MaxDisplayNameLength - len(suffix); len(base) > limit {
			base = base[:limit]
		}
		candidate := string(base) + suffix
		if !taken[candidate] {
			return candidate
		}
	}
}

// newGuestUniqueID returns an ephemeral unique ID for a guest session,
// clearly distinguishable from key-derived (base64) unique IDs.
func newGuestUniqueID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("guest:%x", time.Now().UnixNano())
	}
	return "guest:" + hex.EncodeToString(b)
}

// remoteIP extracts the host part of the connection's remote address, used
// for IP ban lookups.
func remoteIP(conn net.Conn) string {
	host, _, err := net.SplitHostPort(conn.RemoteAddr().String())
	if err != nil {
		return conn.RemoteAddr().String()
	}
	return host
}
