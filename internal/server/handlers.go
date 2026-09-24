// handlers.go implements the per-message-type handlers of the TCP control
// server. All handlers run on the client's connection goroutine and reply via
// s.writeMessage; asynchronous traffic (snapshots excluded) is delivered by
// the broadcaster through the client's broadcast writer goroutine as MsgEvent
// frames.
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"strconv"
	"sync"
	"time"

	"go.uber.org/zap"

	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/channels"
	"noxa/internal/netproto"
	"noxa/internal/recorder"
	"noxa/internal/state"
)

// Broadcast event types sent in MsgEvent envelopes.
const (
	eventUserJoined     = "user_joined"
	eventUserLeft       = "user_left"
	eventUserMoved      = "user_moved"
	eventChannelCreated = "channel_created"
	eventChannelDeleted = "channel_deleted"
	eventKicked         = "kicked"
	eventChat           = "chat"
	eventChannelUpdated = "channel_updated"
)

const maxConcurrentDeletedRecorderStops = 8

// userEvent is the payload of user_joined / user_left / user_moved events.
type userEvent struct {
	ClientID      string `json:"client_id"`
	UniqueID      string `json:"unique_id,omitempty"`
	Nickname      string `json:"nickname,omitempty"`
	ChannelID     int64  `json:"channel_id,omitempty"`
	FromChannelID int64  `json:"from_channel_id,omitempty"`
	ByClientID    string `json:"by_client_id,omitempty"`
}

// channelEvent is the payload of channel_created / channel_deleted events.
type channelEvent struct {
	ChannelID  int64   `json:"channel_id"`
	ChannelIDs []int64 `json:"channel_ids,omitempty"`
	Name       string  `json:"name,omitempty"`
	ParentID   int64   `json:"parent_id,omitempty"`
	Reason     string  `json:"reason,omitempty"`
}

// channelUpdatedEvent is the payload of channel_updated events, carrying the
// editable channel fields after a ChannelEdit.
type channelUpdatedEvent struct {
	ChannelID       int64  `json:"channel_id"`
	Topic           string `json:"topic"`
	MaxClients      int    `json:"max_clients"`
	OpusBitrate     int    `json:"opus_bitrate"`
	OpusFEC         bool   `json:"opus_fec"`
	OpusDTX         bool   `json:"opus_dtx"`
	OpusStereo      bool   `json:"opus_stereo"`
	SlowModeSeconds int    `json:"slow_mode_seconds"`
	Description     string `json:"description"`
	OrderIndex      int    `json:"order_index"`
	ParentID        int64  `json:"parent_id"`
}

// kickEvent is the payload of kicked events.
type kickEvent struct {
	ClientID   string `json:"client_id"`
	ChannelID  int64  `json:"channel_id,omitempty"`
	ByClientID string `json:"by_client_id"`
	Reason     string `json:"reason,omitempty"`
	FromServer bool   `json:"from_server"`
	Ban        bool   `json:"ban"`
	ExpiresAt  int64  `json:"expires_at,omitempty"`
}

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
	return s.completeAuthForUser(ctx, client, user)
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

	challenge, _, ok := client.takeChallenge(msg.UniqueID)
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
	return s.completeAuth(ctx, client, msg.UniqueID)
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
		return s.completeAuth(ctx, client, uniqueID)
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
		return s.completeAuthForUser(ctx, client, user)
	} else if !errors.Is(err, auth.ErrUserNotFound) {
		s.logger.Warn("user lookup by public key failed",
			zap.String("client_id", client.ID),
			zap.Error(err),
		)
		return s.writeMessage(client, netproto.MsgAuthResponse, netproto.AuthResponse{Reason: "internal error"})
	}

	attempt.Succeed(principalScope)
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

// completeAuth finishes a successful registered-user authentication (any
// method): it resolves the user record and calls completeAuthForUser.
func (s *TCPServer) completeAuth(ctx context.Context, client *Client, uniqueID string) error {
	user, err := s.deps.Auth.LookupUser(ctx, uniqueID)
	if err != nil {
		if errors.Is(err, auth.ErrUserNotFound) {
			return s.writeMessage(client, netproto.MsgAuthResponse, netproto.AuthResponse{Reason: "invalid credentials"})
		}
		s.logger.Warn("user lookup failed",
			zap.String("client_id", client.ID),
			zap.Error(err),
		)
		return s.writeMessage(client, netproto.MsgAuthResponse, netproto.AuthResponse{Reason: "internal error"})
	}
	return s.completeAuthForUser(ctx, client, user)
}

// completeAuthForUser finishes authentication for an already-resolved user
// record: the account's unique ID is the canonical identity, its nickname
// the display name.
func (s *TCPServer) completeAuthForUser(ctx context.Context, client *Client, user *auth.User) error {
	nickname := user.Nickname
	if nickname == "" {
		nickname = user.UniqueID
	}
	return s.finishAuth(ctx, client, authIdentity{
		uniqueID: user.UniqueID,
		nickname: nickname,
		userID:   user.ID,
		bot:      user.IsBot,
	})
}

// completeGuestAuth finishes an anonymous login (ephemeral or key-derived).
// Guests are never admin and have no users row: their permission resolution
// yields an empty tiered set (all defaults), and they get no offline spool.
func (s *TCPServer) completeGuestAuth(ctx context.Context, client *Client, uniqueID, nickname string) error {
	if nickname == "" {
		nickname = "guest"
	}
	nickname = s.dedupeNickname(nickname)
	return s.finishAuth(ctx, client, authIdentity{
		uniqueID: uniqueID,
		nickname: nickname,
		guest:    true,
	})
}

// authIdentity is the resolved identity of an authenticated client.
type authIdentity struct {
	uniqueID string
	nickname string
	userID   int64
	bot      bool
	guest    bool
}

// finishAuth is the shared tail of all auth paths: record the identity,
// register with state and the broadcaster, reply with an AuthResponse
// followed by a full tree snapshot, deliver any spooled offline messages
// (registered users only), and announce the join.
func (s *TCPServer) finishAuth(ctx context.Context, client *Client, id authIdentity) error {
	if client.negotiatedAuthorizationModel() != s.requiredAuthorizationModel() {
		return s.rejectAuthorizationModel(client)
	}
	if id.userID > 0 && s.deps != nil && s.deps.Authority != nil {
		if err := s.deps.Authority.RefreshIfChanged(ctx, id.userID, s.noLiveMemberSession); err != nil {
			return s.writeMessage(client, netproto.MsgAuthResponse, netproto.AuthResponse{Reason: "internal error"})
		}
	}
	var rejection string
	err := s.withRolePolicy(ctx, func(ctx context.Context) error {
		s.roleMetadataMu.Lock()
		defer s.roleMetadataMu.Unlock()
		if client.sessionRevoked() {
			return authorization.ErrRoleForbidden
		}
		if s.deps.Auth == nil {
			return authorization.ErrAuthorizationUnavailable
		}
		var err error
		rejection, err = s.banRejectReason(ctx, client, id.uniqueID, remoteIP(client.Conn))
		if err != nil {
			return err
		}
		if rejection != "" {
			return authorization.ErrRoleForbidden
		}
		client.setIdentity(id.uniqueID, id.nickname, id.userID, id.bot)
		s.publishAuthenticatedSession(ctx, client, id)
		return nil
	})
	if err != nil {
		if rejection == "" {
			rejection = "internal error"
		} else {
			s.metricsSink().IncAuthFailure("tcp", "banned")
		}
		return s.writeMessage(client, netproto.MsgAuthResponse, netproto.AuthResponse{Reason: rejection})
	}
	if err := s.withRoleSession(ctx, client, func(ctx context.Context) error {
		if !id.guest {
			if recorder, ok := s.deps.Auth.(interface {
				RecordLastIP(context.Context, int64, string) error
			}); ok {
				host, _, err := net.SplitHostPort(client.Conn.RemoteAddr().String())
				if err == nil {
					if err := recorder.RecordLastIP(ctx, id.userID, host); err != nil {
						s.logger.Warn("recording encrypted login IP failed", zap.Error(err))
					}
				}
			}
		}
		return nil
	}); err != nil {
		return err
	}

	s.logger.Info("client authenticated",
		zap.String("client_id", client.ID),
		zap.String("unique_id", id.uniqueID),
		zap.String("nickname", id.nickname),
		zap.Bool("guest", id.guest),
	)

	// Reply first, then send the snapshot, then announce the join.
	resp := netproto.AuthResponse{
		AuthorizationModel: s.requiredAuthorizationModel(),
		OK:                 true,
		ClientID:           client.ID,
		UniqueID:           id.uniqueID,
		Nickname:           id.nickname,
		TLSFingerprint:     s.tlsFingerprint,
	}
	if s.deps.ICEServers != nil {
		resp.ICEServers = s.deps.ICEServers(id.uniqueID)
	}
	if err := s.withRoleSession(ctx, client, func(ctx context.Context) error {
		e := ctx.Value(roleLeaseKey{}).(roleLease).evaluator
		if e.Evaluate(client.userID(), 0, authorization.ViewChannel).Allowed {
			s.attachChatKeysAndMOTD(ctx, client, &resp)
		}
		return s.writeAuthenticationResponse(ctx, client, resp)
	}); err != nil {
		return err
	}

	if err := s.sendSnapshot(client); err != nil {
		return err
	}
	// (312) Seed the client's channel-tab model with the authoritative set.
	// The current channel is implicit and therefore cannot be unsubscribed.
	if err := s.sendSubscriptionState(ctx, client); err != nil {
		return err
	}

	// (133) active announcement, if any — after the snapshot so clients
	// process it as the first live event. It stays here rather than moving
	// behind key publish: deliverScopeKey skips clients that never published
	// one, which would silently drop the announcement for all of them.
	if err := s.withRoleSession(ctx, client, func(ctx context.Context) error {
		if ann, gen, err := s.serverSettingSealed(ctx, "announcement"); err == nil && ann != "" {
			data := map[string]any{"text": ann, "enc": gen > 0, "key_id": gen}
			if payload, err := eventEnvelope(eventAnnouncement, data); err == nil {
				if err := s.writeRoleBroadcastInContext(ctx, client, payload); err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		return err
	}

	// Guests have no users row, so no offline spool (spool inserts require a
	// users.id).
	return s.withRoleSession(ctx, client, func(ctx context.Context) error {
		if !id.guest {
			s.deliverSpooled(ctx, client, id.userID)
		}

		// Arm the rules gate after identity and the tree reach the client.
		s.sendPendingRules(ctx, client, id.guest)
		s.broadcastEvent(eventUserJoined, userEvent{
			ClientID: client.ID,
			UniqueID: id.uniqueID,
			Nickname: id.nickname,
		})
		return nil
	})
}

// publishAuthenticatedSession installs identity-dependent live state once. In
// role mode final ban admission, identity and this publication share the policy
// and metadata barriers; slow handshake delivery happens after releasing them.
func (s *TCPServer) publishAuthenticatedSession(ctx context.Context, client *Client, id authIdentity) {
	if s.deps.State != nil {
		s.deps.State.AddClient(&state.Client{ClientID: client.ID, UserID: id.userID, UniqueID: id.uniqueID, Nickname: id.nickname, ConnectedAt: time.Now(), Conn: client.Conn, IsBot: s.ClientIsBot(ctx, client)})
		if x := client.x25519(); x != "" {
			s.deps.State.SetE2EPublicKey(client.ID, x)
		}
	}
	if s.deps.Broadcast != nil {
		out, err := s.deps.Broadcast.Register(client.ID)
		if err != nil {
			s.logger.Warn("broadcast register failed", zap.String("client_id", client.ID), zap.Error(err))
		} else {
			// #nosec G118 -- connection-owned pump ends on Unregister or socket failure; authentication request completion must not stop it.
			go s.broadcastWriter(client, out)
		}
	}
}

// attachChatKeysAndMOTD seals the global generation for the client's X25519
// key and hands over the MOTD sealed under that same generation, so the
// client can render it before Connect() returns — no extra frame, no
// ordering rule, no race (133).
//
// A client that published no encryption key gets neither: it could not open
// them. Under the plaintext escape hatch such a client still sees the MOTD,
// which is the only reason that branch exists.
func (s *TCPServer) attachChatKeysAndMOTD(ctx context.Context, client *Client, resp *netproto.AuthResponse) {
	pub := client.x25519()
	if pub == "" {
		if s.cfg != nil && s.cfg.ChatAllowPlaintext {
			resp.MOTD = s.serverSettingPlain(ctx, "motd")
		}
		return
	}
	if s.chatKeys == nil || !s.chatKeys.configured() {
		return
	}
	// Never mints: the global generation is created once at boot.
	gen, _, err := s.chatKeys.current(ctx, globalChatScope)
	if err != nil {
		s.logger.Warn("global chat key unavailable at auth",
			zap.String("client_id", client.ID),
			zap.Error(err),
		)
		return
	}
	ck, err := s.chatKeys.sealFor(ctx, globalChatScope, gen, pub)
	if err != nil {
		s.logger.Warn("sealing global chat key for auth response failed",
			zap.String("client_id", client.ID),
			zap.Error(err),
		)
		return
	}
	resp.ChatKeys = []netproto.ChannelKey{*ck}

	motd, motdGen, err := s.serverSettingSealed(ctx, "motd")
	if err != nil || motd == "" {
		return
	}
	resp.MOTD, resp.MOTDEnc, resp.MOTDKeyID = motd, true, motdGen
}

// setRulesPending arms or clears the server-rules gate on the connection
// (215).
func (c *Client) setRulesPending(pending bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rulesPending = pending
}

// rulesBlocked reports whether the session still owes the server rules an
// answer (215).
func (c *Client) rulesBlocked() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.rulesPending
}

// sendPendingRules delivers the operator's rules when this session still owes
// them an answer, and arms the gate that keeps the client out of channels and
// chat until it gives one (215).
//
// Guests are asked on EVERY connect and their answer lives only in this
// session: server_rules_acceptance.user_id references users.id, and a guest
// has no such row, so no acceptance can be recorded for them. Asking every
// time is the honest reading of an item that publishes the rules to everyone
// who joins — the alternative, not asking at all, would exempt exactly the
// users the operator knows least about.
func (s *TCPServer) sendPendingRules(ctx context.Context, client *Client, guest bool) {
	if s.deps == nil || s.deps.Rules == nil {
		return
	}
	var (
		text, hash string
		pending    bool
		err        error
	)
	if guest {
		text, hash, err = s.deps.Rules.Text(ctx)
		pending = hash != ""
	} else {
		text, hash, pending, err = s.deps.Rules.Pending(ctx, client.userID())
	}
	if err != nil {
		s.logger.Warn("reading the server rules failed",
			zap.String("client_id", client.ID),
			zap.Error(err),
		)
		return
	}
	if !pending {
		return
	}
	if err := s.setSessionRulesPending(ctx, client, true); err != nil {
		_ = client.Conn.Close()
		return
	}
	if err := s.writeMessage(client, netproto.MsgServerRules, netproto.ServerRules{Text: text, Hash: hash}); err != nil {
		s.logger.Warn("sending the server rules failed",
			zap.String("client_id", client.ID),
			zap.Error(err),
		)
	}
}

// handleServerRulesAccept records the caller's acceptance of the wording it
// was shown (215). A stale hash is refused with an error frame AND a fresh
// ServerRules frame carrying the text actually in force, so the client
// re-displays instead of silently consenting to words nobody read. An empty
// ServerRules frame is the acknowledgement of a successful accept: the gate
// state stays server-authoritative, so the dialog never has to guess.
func (s *TCPServer) handleServerRulesAccept(ctx context.Context, client *Client, f *netproto.Frame) error {
	return s.rolePolicyRead(ctx, client, func(ctx context.Context) error {
		return s.acceptSessionRules(ctx, client, f)
	})
}

func (s *TCPServer) acceptSessionRules(ctx context.Context, client *Client, f *netproto.Frame) error {
	var msg netproto.ServerRulesAccept
	if err := netproto.Decode(f, &msg); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "malformed server_rules_accept: "+err.Error())
	}
	if s.deps == nil || s.deps.Rules == nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "server rules unavailable")
	}
	text, hash, err := s.deps.Rules.Text(ctx)
	if err != nil {
		s.logger.Warn("reading the server rules failed",
			zap.String("client_id", client.ID),
			zap.Error(err),
		)
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "server rules unavailable")
	}
	if hash == "" || msg.Hash != hash {
		if err := s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "the server rules changed since they were shown"); err != nil {
			return err
		}
		if err := s.setSessionRulesPending(ctx, client, hash != ""); err != nil {
			return s.roleError(ctx, client, err)
		}
		return s.writeMessage(client, netproto.MsgServerRules, netproto.ServerRules{Text: text, Hash: hash})
	}
	// A guest has no users row to write the acceptance to, so it stays on the
	// connection (see sendPendingRules).
	if client.userID() != 0 {
		if err := s.deps.Rules.Accept(ctx, client.userID(), msg.Hash); err != nil {
			s.logger.Warn("recording the rules acceptance failed",
				zap.String("client_id", client.ID),
				zap.Error(err),
			)
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "recording the acceptance failed")
		}
	}
	if err := s.setSessionRulesPending(ctx, client, false); err != nil {
		return s.roleError(ctx, client, err)
	}
	return s.writeMessage(client, netproto.MsgServerRules, netproto.ServerRules{})
}

// Rules eligibility also participates in media metadata. A stale acceptance
// can re-block a connected session, so it uses the same barrier as movement.
func (s *TCPServer) setSessionRulesPending(ctx context.Context, client *Client, pending bool) error {
	return s.withRolePolicy(ctx, func(ctx context.Context) error {
		s.roleMetadataMu.Lock()
		defer s.roleMetadataMu.Unlock()
		client.roleActionMu.Lock()
		defer client.roleActionMu.Unlock()
		client.setRulesPending(pending)
		s.refreshRolePublishers(ctx.Value(roleLeaseKey{}).(roleLease).evaluator)
		return nil
	})
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
		candidate := fmt.Sprintf("%s#%d", nickname, i)
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

// ApplyChannelDeletion publishes every side effect shared by explicit and
// automatic channel-subtree deletion. ChannelManager releases its lifecycle
// lock before invoking this method as the temporary-cleanup sink.
func (s *TCPServer) ApplyChannelDeletion(result channels.DeleteResult, reason string) {
	if s == nil || s.deps == nil || len(result.ChannelIDs) == 0 {
		return
	}
	// Revoke every subtree capability before any potentially blocking voice,
	// recorder, filesystem, or notification work. This closes the authorization
	// boundary before the committed deletion becomes externally visible.
	if s.deps.FileTransfer != nil {
		for _, channelID := range result.ChannelIDs {
			if err := s.deps.FileTransfer.TombstoneChannelData(channelID); err != nil {
				s.logger.Warn("tombstoning deleted channel files failed",
					zap.Int64("channel_id", channelID),
					zap.Error(err),
				)
			}
		}
	}
	s.broadcastEvent(eventChannelDeleted, channelEvent{
		ChannelID:  result.RootID,
		ChannelIDs: result.ChannelIDs,
		Reason:     reason,
	})
	if s.deps.Voice != nil {
		for _, member := range result.Members {
			s.deps.Voice.LeaveChannel(member.ClientID, member.ChannelID)
		}
	}
	// State-facing consequences must not sit behind filesystem cleanup. A file
	// mutation can legitimately hold its lifecycle read lock while finishing a
	// database/blob move; clients, metrics, and recorders still need to observe
	// the committed deletion immediately.
	s.pushSubscriptionStateTo(context.Background(), result.SubscriberIDs)
	s.stopDeletedChannelRecordings(result.ChannelIDs)
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cleanupCancel()
	for _, channelID := range result.ChannelIDs {
		if s.deps.FileTransfer != nil {
			if err := s.deps.FileTransfer.DeleteChannelData(cleanupCtx, channelID); err != nil {
				s.logger.Warn("removing deleted channel files failed",
					zap.Int64("channel_id", channelID),
					zap.Error(err),
				)
			}
		}
		if _, err := s.assets().removeImage("icons", strconv.FormatInt(channelID, 10)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			s.logger.Warn("removing deleted channel icon failed",
				zap.Int64("channel_id", channelID),
				zap.Error(err),
			)
		}
	}
}

func (s *TCPServer) stopDeletedChannelRecordings(channelIDs []int64) {
	if s.deps.Recorder == nil || len(channelIDs) == 0 {
		return
	}
	workers := min(maxConcurrentDeletedRecorderStops, len(channelIDs))
	jobs := make(chan int64)
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for channelID := range jobs {
				if err := s.deps.Recorder.Stop(channelID); err != nil && !errors.Is(err, recorder.ErrNotRecording) {
					s.logger.Warn("stopping deleted channel recording failed",
						zap.Int64("channel_id", channelID),
						zap.Error(err),
					)
				}
			}
		}()
	}
	for _, channelID := range channelIDs {
		jobs <- channelID
	}
	close(jobs)
	wg.Wait()
}

// BroadcastChannelUpdated announces a channel's current editable fields to
// all clients (used by out-of-band edits, e.g. ServerQuery channeledit).
func (s *TCPServer) BroadcastChannelUpdated(channelID int64) {
	if s.deps == nil || s.deps.State == nil {
		return
	}
	ch, ok := s.deps.State.GetChannel(channelID)
	if !ok {
		return
	}
	s.broadcastEvent(eventChannelUpdated, channelUpdatedEventFor(ch))
}

// channelUpdatedEventFor snapshots a channel's editable fields for the
// channel_updated event.
func channelUpdatedEventFor(ch *state.Channel) channelUpdatedEvent {
	return channelUpdatedEvent{
		ChannelID:       ch.ChannelID,
		Topic:           ch.Topic,
		MaxClients:      ch.MaxClients,
		OpusBitrate:     ch.OpusBitrate,
		OpusFEC:         ch.OpusFEC,
		OpusDTX:         ch.OpusDTX,
		OpusStereo:      ch.OpusStereo,
		SlowModeSeconds: ch.SlowModeSeconds,
		Description:     ch.Description,
		OrderIndex:      ch.OrderIndex,
		ParentID:        ch.ParentID,
	}
}

// handleJoinChannel moves the calling client into the target channel. The
// role evaluator authorizes Connect, and any channel password is checked
// independently.
func (s *TCPServer) handleJoinChannel(ctx context.Context, client *Client, f *netproto.Frame) error {
	var msg netproto.JoinChannel
	if err := netproto.Decode(f, &msg); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "malformed join_channel: "+err.Error())
	}
	if msg.AckRequested && msg.ChannelID < 0 {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid channel")
	}
	if s.deps == nil || s.deps.State == nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "state backend unavailable")
	}
	// Channel zero is the connected lobby. Leaving your own channel requires
	// no moderation or join permission and never closes the server connection.
	if msg.ChannelID == 0 {
		return s.rolePolicyRead(ctx, client, func(ctx context.Context) error {
			s.roleMetadataMu.Lock()
			defer s.roleMetadataMu.Unlock()
			client.roleActionMu.Lock()
			defer client.roleActionMu.Unlock()
			if err := s.leaveOwnChannelInContext(ctx, client); err != nil {
				return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, err.Error())
			}
			return s.acknowledgeJoin(client, msg)
		})
	}
	return s.roleAction(ctx, client, msg.ChannelID, authorization.Connect, func(ctx context.Context) error {
		return s.joinChannelAllowed(ctx, client, msg)
	})
}

func (s *TCPServer) joinChannelAllowed(ctx context.Context, client *Client, msg netproto.JoinChannel) error {
	s.roleMetadataMu.Lock()
	defer s.roleMetadataMu.Unlock()
	client.roleActionMu.Lock()
	defer client.roleActionMu.Unlock()
	// (215) acceptance is a condition of entry, not a notice: an unanswered
	// rules prompt keeps the client in the lobby, where the only thing it can
	// still do is answer.
	if client.rulesBlocked() {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "accept the server rules before joining a channel")
	}
	ch, ok := s.deps.State.GetChannel(msg.ChannelID)
	if !ok {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeNotFound, "channel not found")
	}

	bypassPassword := s.roleAllowed(ctx, client, msg.ChannelID, authorization.BypassChannelPassword)
	if ch.PasswordHash != "" && !bypassPassword {
		if err := auth.VerifyPassword(msg.Password, ch.PasswordHash); err != nil {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "invalid channel password")
		}
	}

	if err := s.moveClient(ctx, client.ID, msg.ChannelID, client.ID); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeNotFound, err.Error())
	}
	return s.acknowledgeJoin(client, msg)
}

func (s *TCPServer) leaveOwnChannelInContext(ctx context.Context, client *Client) error {
	var previousChannelID int64
	if s.deps.Channels != nil {
		var err error
		previousChannelID, err = s.deps.Channels.LeaveClient(client.ID)
		if errors.Is(err, state.ErrNotInChannel) {
			return nil
		}
		if err != nil {
			return err
		}
	} else {
		snapshot, ok := s.deps.State.GetClient(client.ID)
		if !ok {
			return state.ErrClientNotFound
		}
		previousChannelID = snapshot.ChannelID
		if err := s.deps.State.LeaveChannel(client.ID); err != nil && !errors.Is(err, state.ErrNotInChannel) {
			return err
		}
	}
	s.deps.State.SetPrioritySpeaker(client.ID, false)
	s.deps.State.SetSharing(client.ID, false)
	if previousChannelID != 0 {
		if s.deps.Voice != nil {
			s.deps.Voice.LeaveChannel(client.ID, previousChannelID)
		}
		s.rotateScopeKey(ctx, previousChannelID)
	}
	_ = s.sendSubscriptionState(ctx, client)
	s.broadcastEvent(eventUserMoved, userEvent{
		ClientID: client.ID, FromChannelID: previousChannelID,
		ChannelID: 0, ByClientID: client.ID,
	})
	return nil
}

// handleMoveClient uses current role authority and hierarchy.
func (s *TCPServer) handleMoveClient(ctx context.Context, client *Client, f *netproto.Frame) error {
	var msg netproto.MoveClient
	if err := netproto.Decode(f, &msg); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "malformed move_client: "+err.Error())
	}
	err := s.withRolePolicy(ctx, func(ctx context.Context) error {
		s.roleMetadataMu.Lock()
		defer s.roleMetadataMu.Unlock()
		if client.sessionRevoked() || client.rulesBlocked() {
			return authorization.ErrRoleForbidden
		}
		e := ctx.Value(roleLeaseKey{}).(roleLease).evaluator
		return s.moveRoleMember(ctx, e, client.userID(), client.uniqueID(), client.ID, msg)
	})
	if err != nil {
		return s.roleError(ctx, client, err)
	}
	return s.acknowledgeMove(client, msg)
}

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

// maxChatBytes caps the chat body size (for encrypted messages this is the
// base64 ciphertext length).
const maxChatBytes = 16 * 1024

// handleChatSend routes a chat message: channel chat to the channel's
// members, a direct message to a user by unique ID (spooled when offline), a
// direct message to an online connection by client ID (echoed back to the
// sender), or a global message to all clients.
//
// Encryption (4b): plaintext is rejected unless chat_allow_plaintext is set.
// For encrypted messages the server validates the scope key id (channel and
// global scopes only; direct messages are true E2EE and unverifiable by
// design) and the ciphertext size — it cannot read the body.
func (s *TCPServer) handleChatSend(ctx context.Context, client *Client, f *netproto.Frame) error {
	return s.rolePolicyRead(ctx, client, func(ctx context.Context) error {
		return s.sendSessionChat(ctx, client, f)
	})
}

func (s *TCPServer) sendSessionChat(ctx context.Context, client *Client, f *netproto.Frame) error {
	var msg netproto.ChatSend
	if err := netproto.Decode(f, &msg); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "malformed chat_send: "+err.Error())
	}
	if msg.AckRequested && !validAcknowledgedChat(msg) {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid acknowledged chat destination or reference")
	}
	if s.deps == nil || s.deps.Broadcast == nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "broadcast backend unavailable")
	}
	// (215) same gate as the join: rules the user has not answered would
	// otherwise be advisory, and DMs would route around a channel-only check.
	if client.rulesBlocked() {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "accept the server rules before sending messages")
	}

	if len(msg.Text) > maxChatBytes {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "chat message too large")
	}
	if !msg.Enc && !s.cfg.ChatAllowPlaintext {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "plaintext chat is disabled on this server — update your client (chat encryption is mandatory)")
	}
	if msg.Enc && msg.KeyID == 0 && msg.ChannelID != "" {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "channel chat requires a scope key id")
	}

	isDM := msg.ToUniqueID != "" || msg.ToClientID != ""
	if s.chatRate != nil && !s.chatRate.allowLimit(client.UniqueID, time.Now(), s.chatActionLimit(ctx, client)) {
		s.metricsSink().IncChatMessage("rejected")
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "chat rate limit exceeded — slow down")
	}

	// Channel/global scopes run the moderation pipeline (wave 5a): rate
	// limit, slow mode, decrypt, filters, spam, mentions, store, relay.
	if !isDM {
		var channelID int64
		if msg.ChannelID != "" {
			id, err := strconv.ParseInt(msg.ChannelID, 10, 64)
			if err != nil {
				return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid channel_id: "+msg.ChannelID)
			}
			channelID = id
		}
		return s.roleAction(ctx, client, channelID, authorization.SendMessages, func(ctx context.Context) error {
			// Read entitlement FIRST, before anything touches chatKeys: members and
			// entitled subscribers may write the channel tab they can read, while
			// routeScopedChat may ensure a scope's first generation. Reaching it with an
			// attacker-supplied channel id is a disk-exhaustion DoS (91).
			if channelID != 0 {
				if s.deps.State == nil {
					return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "state backend unavailable")
				}
				if !s.scopeReadable(ctx, client, channelID) {
					return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "not a member or subscriber of this channel")
				}
			}
			if msg.Enc {
				if s.chatKeys == nil {
					return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "chat key manager unavailable")
				}
				// Non-minting lookup: an unknown scope is "rejoin", never a mint.
				currentID, _, err := s.chatKeys.current(ctx, channelID)
				if errors.Is(err, ErrNoScopeKey) {
					return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "no chat key for this channel yet — rejoin the channel")
				}
				if err != nil {
					return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "chat key unavailable")
				}
				if msg.KeyID != currentID {
					scope := "channel"
					if channelID == 0 {
						scope = "global scope"
					}
					return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "stale chat key for "+scope+" (key rotated; wait for re-key)")
				}
			}
			return s.routeScopedChat(ctx, client, msg, channelID)
		})
	}

	// Direct messages are true E2EE: relay/spool only, no moderation.
	chat := netproto.ChatBroadcast{
		Direct:       true,
		ToUniqueID:   msg.ToUniqueID,
		ChannelID:    msg.ChannelID,
		FromClientID: client.ID,
		FromUniqueID: client.UniqueID,
		From:         client.Username,
		Text:         msg.Text,
		Enc:          msg.Enc,
		KeyID:        msg.KeyID,
		E2E:          msg.Enc,
		ClientMsgID:  msg.ClientMsgID,
	}
	if chat.ToUniqueID == "" {
		if target, ok := s.clientByID(msg.ToClientID); ok {
			chat.ToUniqueID = target.uniqueID()
		}
	}
	payload, err := eventEnvelope(eventChat, chat)
	if err != nil {
		return err
	}

	switch {
	case msg.ToUniqueID != "":
		return s.sendDirectByUniqueID(ctx, client, msg, payload)
	default: // msg.ToClientID != ""
		if err := s.deps.Broadcast.BroadcastToClient(msg.ToClientID, payload); err != nil {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeNotFound, "target client not reachable")
		}
		// Echo the direct message back to the sender.
		if err := s.echoAcceptedDirect(client, msg, payload); err != nil {
			return err
		}
		s.metricsSink().IncChatMessage("direct")
	}
	return s.acknowledgeChat(client, msg, netproto.ChatRelayed, 0)
}

// sendDirectByUniqueID delivers a direct message to the user with the given
// unique ID. If the user is online the message is delivered immediately (and
// echoed to the sender); otherwise it is spooled into offline_messages for
// delivery at their next login.
func (s *TCPServer) sendDirectByUniqueID(ctx context.Context, client *Client, msg netproto.ChatSend, payload []byte) error {
	// Guests have authenticated live identities but no account row. Resolve
	// the online session before consulting account storage for offline spooling.
	if tc, ok := s.clientByUniqueID(msg.ToUniqueID); ok {
		if err := s.deps.Broadcast.BroadcastToClient(tc.ID, payload); err != nil {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeNotFound, "target client not reachable")
		}
		if err := s.echoAcceptedDirect(client, msg, payload); err != nil {
			return err
		}
		s.metricsSink().IncChatMessage("direct")
		return s.acknowledgeChat(client, msg, netproto.ChatRelayed, 0)
	}

	if s.deps.Auth == nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "authentication backend unavailable")
	}
	target, err := s.deps.Auth.LookupUser(ctx, msg.ToUniqueID)
	if err != nil {
		if errors.Is(err, auth.ErrUserNotFound) {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeNotFound, "target user not found")
		}
		s.logger.Warn("user lookup failed",
			zap.String("client_id", client.ID),
			zap.Error(err),
		)
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "user lookup failed")
	}

	if s.deps.Spool == nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeNotFound, "target user is offline")
	}
	// A DM has no scope key, so the server cannot seal one on the sender's
	// behalf: a plaintext DM to an offline user would land in the spool in
	// the clear. Relaying it live is the sender's choice; persisting it is
	// not, so the escape hatch stops at the spool (91).
	if !msg.Enc {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "target user is offline and plaintext direct messages are never spooled — encrypt the message")
	}
	// E2EE DMs are spooled as ciphertext the server cannot read; the sender's
	// unique ID travels along so the recipient can fetch the public key.
	if err := s.deps.Spool.SpoolMessage(ctx, client.userID(), target.ID, client.UniqueID, msg.Text); err != nil {
		s.logger.Warn("spooling message failed",
			zap.String("client_id", client.ID),
			zap.Error(err),
		)
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "spooling message failed")
	}
	s.logger.Info("message spooled for offline user",
		zap.String("client_id", client.ID),
		zap.String("to_unique_id", msg.ToUniqueID),
	)
	if msg.AckRequested {
		// Give the sender the same own-message echo after durable offline
		// acceptance as after live relay. This does not claim recipient delivery.
		if err := s.echoAcceptedDirect(client, msg, payload); err != nil {
			return err
		}
	}
	return s.acknowledgeChat(client, msg, netproto.ChatQueued, 0)
}

// deliverSpooled sends any spooled offline messages for the user to the
// client as offline chat events and marks them delivered.
func (s *TCPServer) deliverSpooled(ctx context.Context, client *Client, userID int64) {
	if s.deps.Spool == nil || s.deps.Broadcast == nil {
		return
	}
	msgs, err := s.deps.Spool.PendingMessages(ctx, userID)
	if err != nil {
		s.logger.Warn("loading spooled messages failed",
			zap.String("client_id", client.ID),
			zap.Error(err),
		)
		return
	}
	if len(msgs) == 0 {
		return
	}

	ids := make([]int64, 0, len(msgs))
	for _, m := range msgs {
		// Every spooled row is E2EE ciphertext: 012 deleted the undelivered
		// pre-4b plaintext rows and offline_messages_sealed stops new ones,
		// so there is no plaintext replay branch left to take (91).
		payload, err := eventEnvelope(eventChat, netproto.ChatBroadcast{
			FromClientID: strconv.FormatInt(m.FromUserID, 10),
			FromUniqueID: m.FromUniqueID,
			From:         m.FromName,
			ToUniqueID:   client.uniqueID(),
			Text:         m.Message,
			Direct:       true,
			Offline:      true,
			Enc:          true,
			E2E:          true,
		})
		if err != nil {
			continue
		}
		if err := s.deps.Broadcast.BroadcastToClient(client.ID, payload); err != nil {
			s.logger.Warn("delivering spooled message failed",
				zap.String("client_id", client.ID),
				zap.Int64("message_id", m.ID),
				zap.Error(err),
			)
			continue
		}
		ids = append(ids, m.ID)
	}

	if err := s.deps.Spool.MarkMessagesDelivered(ctx, ids); err != nil {
		s.logger.Warn("marking spooled messages delivered failed",
			zap.String("client_id", client.ID),
			zap.Error(err),
		)
	}
}

// handlePing replies with a Pong.
func (s *TCPServer) handlePing(ctx context.Context, client *Client, _ *netproto.Frame) error {
	s.logger.Debug("ping received", zap.String("client_id", client.ID))
	return s.writeMessage(client, netproto.MsgPong, netproto.Pong{})
}

// --- handler helpers -------------------------------------------------------

// remoteIP extracts the host part of the connection's remote address, used
// for IP ban lookups.
func remoteIP(conn net.Conn) string {
	host, _, err := net.SplitHostPort(conn.RemoteAddr().String())
	if err != nil {
		return conn.RemoteAddr().String()
	}
	return host
}

// moveClient moves a client into a channel in the state manager, performs the
// temp-channel cleanup bookkeeping for the source and target channels, keeps
// the voice router's membership in sync, and announces the move.
func (s *TCPServer) moveClient(ctx context.Context, clientID string, channelID int64, movedBy string) error {
	afterMove := func(previousChannelID int64) {
		if previousChannelID != channelID {
			// Moving invalidates publications even when the destination permits sharing.
			s.deps.State.SetSharing(clientID, false)
		}
		// The destination can revoke active controls even without a policy
		// edit. The caller retains its policy and membership action locks.
		if lease, ok := ctx.Value(roleLeaseKey{}).(roleLease); ok {
			if member, present := s.deps.State.GetClient(clientID); present {
				if !lease.evaluator.Evaluate(member.UserID, channelID, authorization.PrioritySpeaker).Allowed {
					s.deps.State.SetPrioritySpeaker(clientID, false)
				}
				if !lease.evaluator.Evaluate(member.UserID, channelID, authorization.ShareScreen).Allowed {
					s.deps.State.SetSharing(clientID, false)
				}
			}
		}
		if s.deps.Voice != nil {
			if lease, ok := ctx.Value(roleLeaseKey{}).(roleLease); ok {
				s.refreshRolePublishers(lease.evaluator)
			}
			if previousChannelID != 0 && previousChannelID != channelID {
				s.deps.Voice.LeaveChannel(clientID, previousChannelID)
			}
			s.deps.Voice.JoinChannel(clientID, channelID)
		}
		// Chat keys (4b): the client gets the new channel's key; the channel it
		// left rotates so ex-members cannot read new messages.
		if previousChannelID != 0 && previousChannelID != channelID {
			s.rotateScopeKey(ctx, previousChannelID)
		}
		if client, ok := s.clientByID(clientID); ok {
			// The move is already committed when this lifecycle callback runs.
			// A cancelled request or key backend failure must not undo it.
			if err := s.deliverScopeKey(ctx, client, channelID); err != nil {
				s.logger.Warn("delivering channel key after move failed",
					zap.String("client_id", client.ID),
					zap.Int64("channel_id", channelID),
					zap.Error(err),
				)
			}
			// (312) the channel a client stands in is implicitly subscribed, so a
			// move changes the authoritative set even though nothing was asked.
			_ = s.sendSubscriptionState(ctx, client)
		}
		s.broadcastEvent(eventUserMoved, userEvent{
			ClientID:      clientID,
			FromChannelID: previousChannelID,
			ChannelID:     channelID,
			ByClientID:    movedBy,
		})
	}
	if s.deps.Channels != nil {
		backend, ok := s.deps.Channels.(interface {
			MoveClientWithinCapacity(string, int64, func(int64)) (int64, error)
		})
		if !ok {
			return authorization.ErrAuthorizationUnavailable
		}
		_, err := backend.MoveClientWithinCapacity(clientID, channelID, afterMove)
		return err
	}
	var oldChannelID int64
	if sc, ok := s.deps.State.GetClient(clientID); ok {
		oldChannelID = sc.ChannelID
	}
	if err := s.deps.State.MoveClientWithinCapacity(clientID, channelID); err != nil {
		return err
	}
	afterMove(oldChannelID)
	return nil
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
// persistence is not wired; kicks still proceed.
func (s *TCPServer) insertBan(ctx context.Context, uniqueID, reason string, bannedBy, expiresAt any) error {
	if s.deps.Bans == nil {
		return nil // ban persistence not wired; kick still proceeds
	}
	const q = `INSERT INTO bans (ban_type, value, reason, banned_by, expires_at) VALUES (1, $1, $2, $3, $4)`
	if _, err := s.deps.Bans.DB().ExecContext(ctx, q, uniqueID, reason, bannedBy, expiresAt); err != nil {
		return err
	}
	return nil
}

// sendSnapshot builds the current channel-tree snapshot and sends it to the
// client as a MsgSnapshot frame.
func (s *TCPServer) sendSnapshot(client *Client) error {
	if s.deps == nil || s.deps.State == nil {
		return nil
	}
	if s.deps.Authority == nil {
		return authorization.ErrAuthorizationUnavailable
	}
	return s.deps.Authority.WithPolicy(context.Background(), func(e *authorization.RoleEvaluator) error {
		if client.sessionRevoked() {
			return authorization.ErrRoleForbidden
		}
		return s.writeMessage(client, netproto.MsgSnapshot, buildRoleSnapshot(s.deps.State, e, client.userID(), client.uniqueID()))
	})
}

// broadcastEvent marshals payload and broadcasts it to all registered clients
// as an event envelope. It is a no-op when no broadcaster is wired.
func (s *TCPServer) broadcastEvent(eventType string, payload any) {
	if s.deps == nil || s.deps.Broadcast == nil {
		return
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		s.logger.Error("failed to marshal event payload",
			zap.String("event_type", eventType),
			zap.Error(err),
		)
		return
	}
	s.deps.Broadcast.BroadcastEvent(eventType, raw)
}

// broadcastToAdmins sends an event to every online server admin (used by
// the invisible-status semantics, 381).
func (s *TCPServer) broadcastToAdmins(eventType string, payload any) {
	if s.deps == nil || s.deps.Broadcast == nil {
		return
	}
	s.broadcastEvent(eventType, payload)
}

// eventEnvelope wraps payload in the {"type": ..., "data": ...} envelope used
// for targeted broadcasts (BroadcastToChannel / BroadcastToClient), matching
// the shape Broadcaster.BroadcastEvent produces for server-wide events.
func eventEnvelope(eventType string, payload any) ([]byte, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	envelope := struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}{Type: eventType, Data: raw}
	return json.Marshal(envelope)
}

// broadcastWriter pumps outbound broadcast payloads to the client connection
// as MsgEvent frames until the broadcaster closes the channel (on Unregister)
// or a write fails.
func (s *TCPServer) broadcastWriter(client *Client, out <-chan []byte) {
	for payload := range out {
		if string(payload) == mediaLimitsNotification {
			if err := s.writeCurrentMediaLimits(context.Background(), client); err != nil {
				_ = client.Conn.Close()
				return
			}
			continue
		}
		if err := s.writeRoleBroadcast(client, payload); err != nil {
			_ = client.Conn.Close()
			return
		}
	}
}
