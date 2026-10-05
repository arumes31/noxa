// handlers_auth_session.go publishes authenticated sessions and their initial state.
package server

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"

	"go.uber.org/zap"

	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"noxa/internal/state"
)

// completeAuth finishes a successful registered-user authentication (any
// method): it resolves the user record and calls completeAuthForUser.
func (s *TCPServer) completeAuth(ctx context.Context, client *Client, uniqueID, displayName string) error {
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
	return s.completeAuthForUser(ctx, client, user, displayName)
}

// completeAuthForUser finishes authentication for an already-resolved user
// record. A display name never changes the canonical account identity.
func (s *TCPServer) completeAuthForUser(ctx context.Context, client *Client, user *auth.User, displayName string) error {
	nickname := user.Nickname
	if displayName != "" {
		nickname = displayName
	}
	if nickname == "" {
		nickname = user.UniqueID
	}
	return s.finishAuth(ctx, client, authIdentity{
		uniqueID:   user.UniqueID,
		nickname:   nickname,
		userID:     user.ID,
		bot:        user.IsBot,
		customName: displayName != "",
	})
}

// completeGuestAuth finishes an anonymous login (ephemeral or key-derived).
// Guests are never admin and have no users row: their permission resolution
// yields an empty tiered set (all defaults), and they get no offline spool.
func (s *TCPServer) completeGuestAuth(ctx context.Context, client *Client, uniqueID, nickname string) error {
	if nickname == "" {
		nickname = "guest"
	}
	return s.finishAuth(ctx, client, authIdentity{
		uniqueID:   uniqueID,
		nickname:   nickname,
		guest:      true,
		customName: true,
	})
}

// authIdentity is the resolved identity of an authenticated client.
type authIdentity struct {
	uniqueID   string
	nickname   string
	userID     int64
	bot        bool
	guest      bool
	customName bool
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
		// A role manager may have enrolled this proven identity while its
		// handshake waited for the policy gate. Join with the committed member
		// ID instead of publishing a second session with guest permissions.
		if id.guest && id.userID == 0 && !strings.HasPrefix(id.uniqueID, "guest:") {
			user, err := s.deps.Auth.LookupUser(ctx, id.uniqueID)
			if err == nil {
				id.userID, id.bot, id.guest = user.ID, user.IsBot, false
			} else if !errors.Is(err, auth.ErrUserNotFound) {
				return err
			}
		}
		if id.customName {
			id.nickname = s.dedupeNickname(id.nickname)
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
		Capabilities:       []string{netproto.CapabilityAudioState, netproto.CapabilityVoiceTelemetry},
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
