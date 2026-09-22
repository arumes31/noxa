package server

import (
	"context"
	"errors"
	"time"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

func (s *TCPServer) handleMediaLimitsSet(ctx context.Context, client *Client, f *netproto.Frame) error {
	var request netproto.MediaLimitsSet
	if err := netproto.Decode(f, &request); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "malformed media_limits_set: "+err.Error())
	}
	limits, valid := request.Limits()
	if !valid {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "media limits require valid bitrate, width and height")
	}
	if s.deps == nil || s.deps.Authority == nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "media limit management requires roles-v1 authorization")
	}
	return s.roleAction(ctx, client, 0, authorization.ManageServer, func(ctx context.Context) error {
		return s.applyMediaLimits(ctx, client, limits)
	})
}

func (s *TCPServer) applyMediaLimits(ctx context.Context, client *Client, limits netproto.MediaLimits) error {
	result, err := s.saveMediaLimits(ctx, client.uniqueID(), limits)
	if errors.Is(err, authorization.ErrRoleInvalid) {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid media limits")
	}
	if err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "saving media limits failed")
	}
	replyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return s.writeMessageInContext(replyCtx, client, netproto.MsgMediaLimitsSaved, netproto.MediaLimitsSaved(result))
}
