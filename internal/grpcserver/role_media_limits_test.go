package grpcserver

import (
	"context"
	"sync"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"noxa/internal/auth"
	"noxa/internal/netproto"
	noxav1 "noxa/v1"
)

type mediaLimitsGRPCBackend struct {
	*metadataGRPCBackend
	mu     sync.Mutex
	limits netproto.MediaLimitsChanged
}

func (b *mediaLimitsGRPCBackend) WithIntegrationMediaLimits(ctx context.Context, _ auth.IntegrationPrincipal, deliver func(context.Context, netproto.MediaLimitsChanged) error) error {
	if err := b.check(ctx); err != nil {
		return err
	}
	b.mu.Lock()
	limits := b.limits
	b.mu.Unlock()
	return deliver(ctx, limits)
}

func (b *mediaLimitsGRPCBackend) SetIntegrationMediaLimits(ctx context.Context, _ auth.IntegrationPrincipal, limits netproto.MediaLimits) (netproto.MediaLimitsSaved, error) {
	if err := b.check(ctx); err != nil {
		return netproto.MediaLimitsSaved{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.limits.Revision++
	b.limits.MediaLimits = limits
	return netproto.MediaLimitsSaved(b.limits), nil
}

func TestRoleGRPCMediaLimits(t *testing.T) {
	b := &mediaLimitsGRPCBackend{
		metadataGRPCBackend: &metadataGRPCBackend{roleGRPCBackend: &roleGRPCBackend{authenticate: func(context.Context, string, string, string) (auth.IntegrationPrincipal, error) {
			return auth.IntegrationPrincipal{}, nil
		}}},
		limits: netproto.MediaLimitsChanged{Revision: 3, MediaLimits: netproto.MediaLimits{VideoMaxBitrate: 800000, VideoMaxWidth: 1280, VideoMaxHeight: 720}},
	}
	client := noxav1.NewControlClient(dialGRPC(t, startGRPC(t, b, nil)))
	ctx := func() context.Context { return roleAuthCtx(t, "integration", "pw") }
	read, err := client.GetMediaLimits(ctx(), &noxav1.GetMediaLimitsRequest{})
	if err != nil || read.GetVideoMaxBitrate() != 800000 || read.GetVideoMaxWidth() != 1280 || read.GetVideoMaxHeight() != 720 || read.GetRevision() != "3" {
		t.Fatalf("read: %+v %v", read, err)
	}
	for index, request := range []*noxav1.SetMediaLimitsRequest{
		{VideoMaxBitrate: 400000, VideoMaxWidth: 640, VideoMaxHeight: 360},
		{},
	} {
		got, err := client.SetMediaLimits(ctx(), request)
		wantRevision := "4"
		if index == 1 {
			wantRevision = "5"
		}
		if err != nil || got.GetVideoMaxBitrate() != request.GetVideoMaxBitrate() || got.GetVideoMaxWidth() != request.GetVideoMaxWidth() || got.GetVideoMaxHeight() != request.GetVideoMaxHeight() || got.GetRevision() != wantRevision {
			t.Fatalf("save: %+v %v", got, err)
		}
	}
	before := b.calls.Load()
	for _, request := range []*noxav1.SetMediaLimitsRequest{
		{VideoMaxBitrate: -1},
		{VideoMaxBitrate: 100_000_001},
		{VideoMaxWidth: -1},
		{VideoMaxWidth: 16384, VideoMaxHeight: 1},
		{VideoMaxWidth: 1, VideoMaxHeight: 16384},
		{VideoMaxWidth: 640},
		{VideoMaxHeight: 360},
	} {
		if _, err := client.SetMediaLimits(ctx(), request); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid request: %+v %v", request, err)
		}
	}
	if b.calls.Load() != before {
		t.Fatal("invalid media limits reached backend")
	}
	b.denied.Store(true)
	if _, err := client.GetMediaLimits(ctx(), &noxav1.GetMediaLimitsRequest{}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("revoked read: %v", err)
	}
	if _, err := client.SetMediaLimits(ctx(), &noxav1.SetMediaLimitsRequest{}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("revoked write: %v", err)
	}
	if _, err := client.GetMediaLimits(roleModelCtx(t.Context()), &noxav1.GetMediaLimitsRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing credentials: %v", err)
	}
	legacy := noxav1.NewControlClient(dialGRPC(t, startGRPC(t, &stubBackend{}, nil)))
	if _, err := legacy.GetMediaLimits(roleAuthCtx(t, "admin-uid", "pw"), &noxav1.GetMediaLimitsRequest{}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("legacy read: %v", err)
	}
	if _, err := legacy.SetMediaLimits(roleAuthCtx(t, "admin-uid", "pw"), &noxav1.SetMediaLimitsRequest{}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("legacy write: %v", err)
	}
}
