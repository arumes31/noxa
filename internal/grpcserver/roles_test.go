package grpcserver

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"noxa/internal/query"
	noxav1 "noxa/v1"
)

// Nil embedded interfaces make accidental legacy/read fallback panic. The
// adapter only authenticates and forwards mutations in this test backend.
type roleGRPCBackend struct {
	query.RoleIntegrationBackend
	query.RoleManagementBackend
	authenticate func(context.Context, string, string, string) (auth.IntegrationPrincipal, error)
	change       func(context.Context, auth.IntegrationPrincipal, authorization.RoleChange) (netproto.RoleChangeResult, error)
}

func (*roleGRPCBackend) RoleIntegrationsEnabled() bool { return true }
func (b *roleGRPCBackend) AuthenticateIntegration(ctx context.Context, id, password, ip string) (auth.IntegrationPrincipal, error) {
	return b.authenticate(ctx, id, password, ip)
}
func (b *roleGRPCBackend) ChangeIntegrationRoles(ctx context.Context, p auth.IntegrationPrincipal, change authorization.RoleChange) (netproto.RoleChangeResult, error) {
	return b.change(ctx, p, change)
}

func TestRoleGRPCMutationAndClosedLegacySurfaces(t *testing.T) {
	var authCalls, changes atomic.Int32
	var denied atomic.Bool
	want := authorization.RoleChange{
		Kind: authorization.MemberRolesSet, ExpectedRevision: 7, RoleID: 42, RoleIDs: []int64{42, 43}, UserID: 99,
		Role:    authorization.Role{ID: 42, Name: "Support", Position: 2, Color: "#123456", Icon: "S", Hoist: true, Permissions: []authorization.Capability{authorization.ViewChannel}},
		Channel: authorization.ChannelPolicy{ChannelID: 2, ParentID: 1, Synced: true, Overrides: []authorization.RoleOverride{{RoleID: 42, UserID: 99, Capability: authorization.ViewChannel, Effect: authorization.Deny}}},
	}
	b := &roleGRPCBackend{
		authenticate: func(ctx context.Context, id, password, ip string) (auth.IntegrationPrincipal, error) {
			authCalls.Add(1)
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > 10*time.Second || id != "integration" || password != "pw" || ip != "127.0.0.1" {
				return auth.IntegrationPrincipal{}, errors.New("incorrect authentication context")
			}
			if denied.Load() {
				return auth.IntegrationPrincipal{}, auth.ErrIntegrationDenied
			}
			return auth.IntegrationPrincipal{}, nil
		},
		change: func(ctx context.Context, _ auth.IntegrationPrincipal, got authorization.RoleChange) (netproto.RoleChangeResult, error) {
			changes.Add(1)
			if !reflect.DeepEqual(got, want) {
				return netproto.RoleChangeResult{}, errors.New("mutation fields changed")
			}
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > 30*time.Second {
				return netproto.RoleChangeResult{}, errors.New("unbounded mutation")
			}
			return netproto.RoleChangeResult{Revision: 8, CreatedRoleID: 50, EnforcementPending: true}, nil
		},
	}
	conn := dialGRPC(t, startGRPC(t, b, nil))
	c := noxav1.NewControlClient(conn)
	req := &noxav1.ChangeRolesRequest{
		Kind: string(want.Kind), ExpectedRevision: 7, RoleId: 42, RoleIds: []int64{42, 43}, UserId: 99,
		Role:    &noxav1.RoleDefinition{Id: 42, Name: "Support", Position: 2, Color: "#123456", Icon: "S", Hoist: true, Permissions: []string{string(authorization.ViewChannel)}},
		Channel: &noxav1.ChannelRoleAccess{ChannelId: 2, ParentId: 1, Synced: true, Overrides: []*noxav1.ChannelRoleOverride{{RoleId: 42, UserId: 99, Capability: string(authorization.ViewChannel), Effect: "deny"}}},
	}
	ctx := roleAuthCtx(t, "integration", "pw")
	result, err := c.ChangeRoles(ctx, req)
	if err != nil || result.GetRevision() != 8 || result.GetCreatedRoleId() != 50 || !result.GetEnforcementPending() {
		t.Fatalf("commit acknowledgement: %v %v", result, err)
	}
	// Current login eligibility is checked for every RPC on the same connection.
	denied.Store(true)
	if _, err := c.ChangeRoles(ctx, req); status.Code(err) != codes.Unauthenticated {
		t.Fatal(err)
	}
	denied.Store(false)
	req.ExpectedRevision = 0
	if _, err := c.ChangeRoles(ctx, req); status.Code(err) != codes.InvalidArgument {
		t.Fatal(err)
	}
	if changes.Load() != 1 {
		t.Fatal("invalid or denied request reached mutation")
	}
	before := authCalls.Load()
	for _, method := range []string{
		noxav1.Control_StartFileTransfer_FullMethodName,
	} {
		if err := conn.Invoke(ctx, method, &noxav1.ListChannelsRequest{}, &noxav1.ListChannelsResponse{}); status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("%s: %v", method, err)
		}
	}
	stream, err := noxav1.NewEventsClient(conn).Subscribe(ctx, &noxav1.SubscribeEventsRequest{})
	if err == nil {
		_, err = stream.Recv()
	}
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("raw events: %v", err)
	}
	if authCalls.Load() != before {
		t.Fatal("unsupported RPC attempted authentication")
	}
}

func TestRoleGRPCMutationErrors(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		code codes.Code
	}{
		{"forbidden", authorization.ErrRoleForbidden, codes.PermissionDenied},
		{"conflict", authorization.ErrRoleConflict, codes.Aborted},
		{"invalid", authorization.ErrRoleInvalid, codes.InvalidArgument},
		{"revalidated admission", auth.ErrIntegrationDenied, codes.Unauthenticated},
		{"unavailable", authorization.ErrAuthorizationUnavailable, codes.Unavailable},
		{"internal detail", errors.New("secret database error"), codes.Unavailable},
		{"canceled", context.Canceled, codes.Canceled},
		{"deadline", context.DeadlineExceeded, codes.DeadlineExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			b := &roleGRPCBackend{
				authenticate: func(context.Context, string, string, string) (auth.IntegrationPrincipal, error) {
					return auth.IntegrationPrincipal{}, nil
				},
				change: func(context.Context, auth.IntegrationPrincipal, authorization.RoleChange) (netproto.RoleChangeResult, error) {
					return netproto.RoleChangeResult{}, test.err
				},
			}
			c := noxav1.NewControlClient(dialGRPC(t, startGRPC(t, b, nil)))
			_, err := c.ChangeRoles(roleAuthCtx(t, "integration", "pw"), &noxav1.ChangeRolesRequest{Kind: "role_delete", ExpectedRevision: 1, RoleId: 42})
			if status.Code(err) != test.code || strings.Contains(err.Error(), "secret") {
				t.Fatalf("status: %v", err)
			}
		})
	}
}

func TestRoleGRPCLoginLimitsAndLegacyMutationRejection(t *testing.T) {
	var calls atomic.Int32
	b := &roleGRPCBackend{authenticate: func(_ context.Context, id, password, _ string) (auth.IntegrationPrincipal, error) {
		calls.Add(1)
		if password != "pw" {
			return auth.IntegrationPrincipal{}, auth.ErrIntegrationDenied
		}
		return auth.IntegrationPrincipal{}, nil
	}}
	c := noxav1.NewControlClient(dialGRPC(t, startGRPCWith(t, b, nil, func(s *Server) {
		limiter := query.New("", zap.NewNop(), b)
		limiter.MaxLoginFailures = 2
		s.limiter = limiter
	})))
	for range 2 {
		if _, err := c.Authenticate(roleAuthCtx(t, "integration", "pw"), &noxav1.AuthenticateRequest{Username: "locked", Password: "wrong"}); status.Code(err) != codes.Unauthenticated {
			t.Fatal(err)
		}
	}
	if _, err := c.Authenticate(roleAuthCtx(t, "integration", "pw"), &noxav1.AuthenticateRequest{Username: "locked", Password: "pw"}); status.Code(err) != codes.Unauthenticated {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatal("locked account reached password verification")
	}
	if _, err := c.Authenticate(roleAuthCtx(t, "integration", "pw"), &noxav1.AuthenticateRequest{Username: "other", Password: "pw"}); err != nil {
		t.Fatal(err)
	}
	legacy := noxav1.NewControlClient(dialGRPC(t, startGRPC(t, &stubBackend{}, nil)))
	if _, err := legacy.ChangeRoles(roleAuthCtx(t, "admin-uid", "pw"), &noxav1.ChangeRolesRequest{ExpectedRevision: 1}); status.Code(err) != codes.FailedPrecondition {
		t.Fatal(err)
	}
}
