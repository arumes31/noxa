package grpcserver

import (
	"context"
	"encoding/base64"
	"testing"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"noxa/internal/query"
	noxav1 "noxa/v1"
)

func TestRoleModeRejectsLegacyGRPCAuthenticationAndOperations(t *testing.T) {
	backend := &stubBackend{}
	logger := zap.NewNop()
	s := New("127.0.0.1:0", backend, nil, logger, query.New("", logger, backend))
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs("authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("owner:password"))))
	_, err := s.unaryAuth(ctx, nil, &grpc.UnaryServerInfo{FullMethod: noxav1.Control_ListChannels_FullMethodName}, func(context.Context, any) (any, error) {
		t.Fatal("incompatible caller reached handler")
		return nil, nil
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("interceptor returned %v", err)
	}
	_, err = s.unaryAuth(ctx, &noxav1.AuthenticateRequest{Username: "owner", Password: "password"}, &grpc.UnaryServerInfo{FullMethod: noxav1.Control_Authenticate_FullMethodName}, func(context.Context, any) (any, error) {
		t.Fatal("legacy authenticate reached handler")
		return nil, nil
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("Authenticate returned %v", err)
	}
}
