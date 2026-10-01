package grpcserver

import (
	"context"
	"strconv"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"noxa/internal/auth"
	"noxa/internal/netproto"
	"noxa/internal/query"
	noxav1 "noxa/v1"
)

func mediaLimitsResponse(limits netproto.MediaLimits, revision uint64) *noxav1.GetMediaLimitsResponse {
	return &noxav1.GetMediaLimitsResponse{
		VideoMaxBitrate: int64(limits.VideoMaxBitrate),
		VideoMaxWidth:   int64(limits.VideoMaxWidth),
		VideoMaxHeight:  int64(limits.VideoMaxHeight),
		Revision:        strconv.FormatUint(revision, 10),
	}
}

func (c *controlService) GetMediaLimits(ctx context.Context, _ *noxav1.GetMediaLimitsRequest) (*noxav1.GetMediaLimitsResponse, error) {
	p, authenticated := ctx.Value(integrationPrincipalKey{}).(auth.IntegrationPrincipal)
	b, supported := c.backend.(query.RoleMediaLimitsBackend)
	if !authenticated || !supported {
		return nil, status.Error(codes.FailedPrecondition, "role integration required")
	}
	return protectedUnaryRead(ctx, c.logger, func(ctx context.Context, deliver func(*noxav1.GetMediaLimitsResponse) error) error {
		return b.WithIntegrationMediaLimits(ctx, p, func(_ context.Context, limits netproto.MediaLimitsChanged) error {
			return deliver(mediaLimitsResponse(limits.MediaLimits, limits.Revision))
		})
	})
}

func (c *controlService) SetMediaLimits(ctx context.Context, req *noxav1.SetMediaLimitsRequest) (*noxav1.SetMediaLimitsResponse, error) {
	p, authenticated := ctx.Value(integrationPrincipalKey{}).(auth.IntegrationPrincipal)
	b, supported := c.backend.(query.RoleMediaLimitsBackend)
	if !authenticated || !supported {
		return nil, status.Error(codes.FailedPrecondition, "role integration required")
	}
	if req.GetVideoMaxBitrate() < 0 || req.GetVideoMaxBitrate() > 100_000_000 ||
		req.GetVideoMaxWidth() < 0 || req.GetVideoMaxWidth() > 16383 ||
		req.GetVideoMaxHeight() < 0 || req.GetVideoMaxHeight() > 16383 {
		return nil, status.Error(codes.InvalidArgument, "invalid media limits")
	}
	limits := netproto.MediaLimits{
		VideoMaxBitrate: int(req.GetVideoMaxBitrate()),
		VideoMaxWidth:   int(req.GetVideoMaxWidth()),
		VideoMaxHeight:  int(req.GetVideoMaxHeight()),
	}
	if !limits.Valid() {
		return nil, status.Error(codes.InvalidArgument, "invalid media limits")
	}
	effectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	saved, err := b.SetIntegrationMediaLimits(effectCtx, p, limits)
	if err != nil {
		return nil, roleStatus(err)
	}
	return &noxav1.SetMediaLimitsResponse{
		VideoMaxBitrate: int64(saved.VideoMaxBitrate),
		VideoMaxWidth:   int64(saved.VideoMaxWidth),
		VideoMaxHeight:  int64(saved.VideoMaxHeight),
		Revision:        strconv.FormatUint(saved.Revision, 10),
	}, nil
}
