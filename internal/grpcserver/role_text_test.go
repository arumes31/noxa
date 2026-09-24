package grpcserver

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"noxa/internal/auth"
	"noxa/internal/netproto"
	noxav1 "noxa/v1"
)

func (b *metadataGRPCBackend) SetIntegrationServerText(ctx context.Context, _ auth.IntegrationPrincipal, request netproto.ServerTextSet) (netproto.ServerTextResult, error) {
	if err := b.check(ctx); err != nil {
		return netproto.ServerTextResult{}, err
	}
	return netproto.ServerTextResult{Key: request.Key, ContentHash: fmt.Sprintf("%x", sha256.Sum256([]byte(*request.Value)))}, nil
}

func TestRoleGRPCServerText(t *testing.T) {
	b := &metadataGRPCBackend{roleGRPCBackend: &roleGRPCBackend{authenticate: func(context.Context, string, string, string) (auth.IntegrationPrincipal, error) {
		return auth.IntegrationPrincipal{}, nil
	}}}
	c := noxav1.NewControlClient(dialGRPC(t, startGRPC(t, b, nil)))
	ctx := func() context.Context { return roleAuthCtx(t, "integration", "pw") }
	text, empty, longName, longText, newline, nul := "Words | with\nnewlines", "", strings.Repeat("é", 129), strings.Repeat("é", 32769), "a\nb", "a\x00b"
	for _, key := range []string{"server_name", "motd", "announcement", "server_rules"} {
		for _, value := range []*string{&empty, &text} {
			if key == "server_name" && value == &text {
				continue
			}
			got, err := c.SetServerText(ctx(), &noxav1.SetServerTextRequest{Key: key, Value: value})
			if err != nil || got.GetKey() != key || got.GetContentHash() != fmt.Sprintf("%x", sha256.Sum256([]byte(*value))) {
				t.Fatalf("save %s: %v %v", key, got, err)
			}
		}
	}
	for _, invalid := range []*noxav1.SetServerTextRequest{{}, {Key: "motd"}, {Key: "owner_id", Value: &empty}, {Key: "server_name", Value: &longName}, {Key: "server_name", Value: &newline}, {Key: "motd", Value: &longText}, {Key: "motd", Value: &nul}} {
		if _, err := c.SetServerText(ctx(), invalid); status.Code(err) != codes.InvalidArgument {
			t.Fatal(err)
		}
	}
	if b.calls.Load() != 7 {
		t.Fatal("invalid text reached backend or save performed a protected refresh")
	}
	b.denied.Store(true)
	request := &noxav1.SetServerTextRequest{Key: "motd", Value: &empty}
	if _, err := c.SetServerText(ctx(), request); status.Code(err) != codes.PermissionDenied {
		t.Fatal(err)
	}
	legacy := noxav1.NewControlClient(dialGRPC(t, startGRPC(t, &stubBackend{}, nil)))
	if _, err := legacy.SetServerText(roleAuthCtx(t, "admin-uid", "pw"), request); status.Code(err) != codes.FailedPrecondition {
		t.Fatal(err)
	}
}
