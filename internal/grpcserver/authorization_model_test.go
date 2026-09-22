package grpcserver

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
	noxav1 "noxa/v1"
)

func roleAuthCtx(t *testing.T, user, password string) context.Context {
	t.Helper()
	return roleModelCtx(authCtx(t, user, password))
}

func roleModelCtx(ctx context.Context) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "noxa-authorization-model", netproto.AuthorizationModelRolesV1)
}

func TestGRPCAuthorizationModelCheckedEveryCall(t *testing.T) {
	var authCalls, changes atomic.Int32
	b := &roleGRPCBackend{
		authenticate: func(context.Context, string, string, string) (auth.IntegrationPrincipal, error) {
			authCalls.Add(1)
			return auth.IntegrationPrincipal{}, nil
		},
		change: func(context.Context, auth.IntegrationPrincipal, authorization.RoleChange) (netproto.RoleChangeResult, error) {
			changes.Add(1)
			return netproto.RoleChangeResult{Revision: 2}, nil
		},
	}
	c := noxav1.NewControlClient(dialGRPC(t, startGRPC(t, b, nil)))
	for _, models := range [][]string{nil, {"roles-v2"}, {"roles-v1", "roles-v1"}, {"roles-v1,roles-v2"}, {"roles-v1"}, nil} {
		for _, operation := range []string{"authenticate", "mutation"} {
			ctx := authCtx(t, "integration", "pw")
			for _, model := range models {
				ctx = metadata.AppendToOutgoingContext(ctx, "noxa-authorization-model", model)
			}
			var header metadata.MD
			beforeAuth, beforeChanges := authCalls.Load(), changes.Load()
			var err error
			if operation == "authenticate" {
				_, err = c.Authenticate(ctx, &noxav1.AuthenticateRequest{Username: "integration", Password: "pw"}, grpc.Header(&header))
			} else {
				_, err = c.ChangeRoles(ctx, &noxav1.ChangeRolesRequest{Kind: "role_create", ExpectedRevision: 1}, grpc.Header(&header))
			}
			if values := header.Get("noxa-authorization-model"); len(values) != 1 || values[0] != "roles-v1" {
				t.Fatalf("model response: %v", header)
			}
			if len(models) == 1 && models[0] == "roles-v1" {
				if err != nil || authCalls.Load() != beforeAuth+1 {
					t.Fatalf("compatible %s: %v", operation, err)
				}
				if operation == "mutation" && changes.Load() != beforeChanges+1 {
					t.Fatal("compatible mutation not executed")
				}
			} else if status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "upgrade") || authCalls.Load() != beforeAuth || changes.Load() != beforeChanges {
				t.Fatalf("incompatible %s with %v reached protected operation: %v", operation, models, err)
			}
		}
	}
}
