package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"strconv"
	"time"
)

type channelInviteStore interface {
	ChannelInvite(context.Context, netproto.ChannelInviteRequest, []byte) (netproto.ChannelInviteResult, error)
}

func (s *TCPServer) handleChannelInvite(ctx context.Context, client *Client, frame *netproto.Frame) error {
	var r netproto.ChannelInviteRequest
	if netproto.Decode(frame, &r) != nil || !r.Valid() {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid channel invitation")
	}
	return s.rolePolicyRead(ctx, client, func(ctx context.Context) error {
		capability := authorization.InviteOthers
		if r.Action == "resolve" {
			capability = authorization.Connect
		}
		if r.Action == "revoke" || r.Action == "regenerate" {
			capability = authorization.ManageChannelAccess
		}
		if !s.roleAllowed(ctx, client, r.ChannelID, capability) {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "channel invitation unavailable")
		}
		backend, ok := s.deps.Chat.(channelInviteStore)
		if !ok {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "channel invitations require a server update")
		}
		token := r.Token
		if r.Action == "regenerate" {
			secret := make([]byte, 32)
			if _, err := rand.Read(secret); err != nil {
				return err
			}
			token = base64.RawURLEncoding.EncodeToString(secret)
		}
		hash := sha256.Sum256([]byte(token))
		out, err := backend.ChannelInvite(ctx, r, hash[:])
		if err != nil {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "channel invitation unavailable")
		}
		if r.Action == "resolve" && (out.Access == "member" && client.userID() <= 0 || !out.Active) {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "this invitation requires an existing registered membership")
		}
		if r.Action == "regenerate" {
			out.Token = token
		}
		if r.Action == "regenerate" || r.Action == "revoke" {
			auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			s.auditInChannels(auditCtx, client.uniqueID(), "channel_invite_"+r.Action, strconv.FormatInt(r.ChannelID, 10), "", r.ChannelID)
		}
		return s.writeCommittedReply(client, netproto.MsgChannelInviteResult, out)
	})
}
