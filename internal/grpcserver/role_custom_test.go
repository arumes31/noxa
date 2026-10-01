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

func (b *metadataGRPCBackend) WithIntegrationCustomMetadata(ctx context.Context, _ auth.IntegrationPrincipal, request netproto.CustomMetadataQuery, deliver func(context.Context, netproto.CustomMetadataPage) error) error {
	if err := b.check(ctx); err != nil {
		return err
	}
	if request.AfterKey != "before" || request.Limit != 1 {
		return authorization.ErrRoleInvalid
	}
	return deliver(ctx, netproto.CustomMetadataPage{UniqueID: request.UniqueID, Entries: []netproto.CustomMetadataEntry{{Key: "key|ü", Value: "value | with\nlines"}}, NextAfterKey: "key|ü"})
}

func (b *metadataGRPCBackend) ChangeIntegrationCustomMetadata(ctx context.Context, _ auth.IntegrationPrincipal, request netproto.CustomMetadataChange) (netproto.CustomMetadataResult, error) {
	if err := b.check(ctx); err != nil {
		return netproto.CustomMetadataResult{}, err
	}
	if request.Key != "key|ü" || (!request.Delete && *request.Value != "" && *request.Value != "value | with\nlines") {
		return netproto.CustomMetadataResult{}, authorization.ErrRoleInvalid
	}
	return netproto.CustomMetadataResult{UniqueID: request.UniqueID, Key: request.Key, Deleted: request.Delete}, nil
}

func TestRoleGRPCCustomMetadata(t *testing.T) {
	b := &metadataGRPCBackend{roleGRPCBackend: &roleGRPCBackend{authenticate: func(context.Context, string, string, string) (auth.IntegrationPrincipal, error) {
		return auth.IntegrationPrincipal{}, nil
	}}}
	c := noxav1.NewControlClient(dialGRPC(t, startGRPC(t, b, nil)))
	ctx := func() context.Context { return roleAuthCtx(t, "integration", "pw") }
	query := &noxav1.ListCustomMetadataRequest{UniqueId: "target|uid", AfterKey: "before", Limit: 1}
	page, err := c.ListCustomMetadata(ctx(), query)
	if err != nil || page.GetUniqueId() != "target|uid" || len(page.GetEntries()) != 1 || page.Entries[0].Value != "value | with\nlines" || page.NextAfterKey != "key|ü" {
		t.Fatalf("custom page: %v %v", page, err)
	}
	value, empty := "value | with\nlines", ""
	for _, text := range []*string{&value, &empty, nil} {
		result, err := c.ChangeCustomMetadata(ctx(), &noxav1.ChangeCustomMetadataRequest{UniqueId: "target|uid", Key: "key|ü", Value: text, Delete: text == nil})
		if err != nil || result.GetUniqueId() != "target|uid" || result.GetKey() != "key|ü" || result.GetDeleted() != (text == nil) {
			t.Fatalf("custom commit: %v %v", result, err)
		}
	}
	for _, invalid := range []*noxav1.ListCustomMetadataRequest{{}, {UniqueId: "uid", Limit: 101}, {UniqueId: "uid", AfterKey: strings.Repeat("x", 129)}} {
		if _, err := c.ListCustomMetadata(ctx(), invalid); status.Code(err) != codes.InvalidArgument {
			t.Fatal(err)
		}
	}
	long := strings.Repeat("é", 2049)
	for _, invalid := range []*noxav1.ChangeCustomMetadataRequest{{}, {UniqueId: "uid", Key: "key"}, {UniqueId: "uid", Key: "key", Value: &empty, Delete: true}, {UniqueId: "uid", Key: "key", Value: &long}} {
		if _, err := c.ChangeCustomMetadata(ctx(), invalid); status.Code(err) != codes.InvalidArgument {
			t.Fatal(err)
		}
	}
	if b.calls.Load() != 4 {
		t.Fatal("invalid request reached backend or mutation refreshed data")
	}
	b.denied.Store(true)
	if _, err := c.ListCustomMetadata(ctx(), query); status.Code(err) != codes.PermissionDenied {
		t.Fatal(err)
	}
	if _, err := c.ChangeCustomMetadata(ctx(), &noxav1.ChangeCustomMetadataRequest{UniqueId: "uid", Key: "key", Delete: true}); status.Code(err) != codes.PermissionDenied {
		t.Fatal(err)
	}
	legacy := noxav1.NewControlClient(dialGRPC(t, startGRPC(t, &stubBackend{}, nil)))
	if _, err := legacy.ListCustomMetadata(roleAuthCtx(t, "admin-uid", "pw"), query); status.Code(err) != codes.FailedPrecondition {
		t.Fatal(err)
	}
}
