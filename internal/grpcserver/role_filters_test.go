package grpcserver

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
	noxav1 "noxa/v1"
)

func (b *metadataGRPCBackend) WithIntegrationChatFilters(ctx context.Context, _ auth.IntegrationPrincipal, deliver func(context.Context, netproto.ChatFilterResponse) error) error {
	if err := b.check(ctx); err != nil {
		return err
	}
	return deliver(ctx, netproto.ChatFilterResponse{WordFilter: "Words | with\nnewlines", LinkBlacklist: "blocked.example", FromConfig: true})
}

func (b *metadataGRPCBackend) SetIntegrationChatFilters(ctx context.Context, _ auth.IntegrationPrincipal, patch netproto.ChatFilterSet) (netproto.ChatFilterResponse, error) {
	if err := b.check(ctx); err != nil {
		return netproto.ChatFilterResponse{}, err
	}
	if patch.WordFilter == nil || *patch.WordFilter != "" || patch.LinkBlacklist != nil || patch.LinkWhitelist != nil {
		return netproto.ChatFilterResponse{}, authorization.ErrRoleInvalid
	}
	return netproto.ChatFilterResponse{LinkBlacklist: "blocked.example"}, nil
}

func TestRoleGRPCChatFilters(t *testing.T) {
	b := &metadataGRPCBackend{roleGRPCBackend: &roleGRPCBackend{authenticate: func(context.Context, string, string, string) (auth.IntegrationPrincipal, error) {
		return auth.IntegrationPrincipal{}, nil
	}}}
	c := noxav1.NewControlClient(dialGRPC(t, startGRPC(t, b, nil)))
	ctx := func() context.Context { return roleAuthCtx(t, "integration", "pw") }
	got, err := c.GetChatFilters(ctx(), &noxav1.GetChatFiltersRequest{})
	if err != nil || got.GetWordFilter() != "Words | with\nnewlines" || got.GetLinkBlacklist() != "blocked.example" || !got.GetFromConfig() {
		t.Fatalf("read: %v %v", got, err)
	}
	empty, oversized := "", strings.Repeat("x", netproto.MaxChatFilterListBytes+1)
	patch := &noxav1.SetChatFiltersRequest{WordFilter: &empty}
	saved, err := c.SetChatFilters(ctx(), patch)
	if err != nil || saved.GetWordFilter() != "" || saved.GetLinkBlacklist() != "blocked.example" || saved.GetFromConfig() {
		t.Fatalf("clear: %v %v", saved, err)
	}
	for _, invalid := range []*noxav1.SetChatFiltersRequest{{}, {WordFilter: &oversized}, {LinkBlacklist: &oversized}, {LinkWhitelist: &oversized}} {
		if _, err := c.SetChatFilters(ctx(), invalid); status.Code(err) != codes.InvalidArgument {
			t.Fatal(err)
		}
	}
	if b.calls.Load() != 2 {
		t.Fatal("invalid patch reached backend or save performed a protected refresh")
	}
	b.denied.Store(true)
	if _, err := c.GetChatFilters(ctx(), &noxav1.GetChatFiltersRequest{}); status.Code(err) != codes.PermissionDenied {
		t.Fatal(err)
	}
	if _, err := c.SetChatFilters(ctx(), patch); status.Code(err) != codes.PermissionDenied {
		t.Fatal(err)
	}
	legacy := noxav1.NewControlClient(dialGRPC(t, startGRPC(t, &stubBackend{}, nil)))
	if _, err := legacy.GetChatFilters(roleAuthCtx(t, "admin-uid", "pw"), &noxav1.GetChatFiltersRequest{}); status.Code(err) != codes.FailedPrecondition {
		t.Fatal(err)
	}
	if _, err := legacy.SetChatFilters(roleAuthCtx(t, "admin-uid", "pw"), patch); status.Code(err) != codes.FailedPrecondition {
		t.Fatal(err)
	}
}
