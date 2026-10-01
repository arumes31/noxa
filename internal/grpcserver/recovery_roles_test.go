package grpcserver

import (
	"context"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"google.golang.org/grpc"

	"noxa/internal/auth"
	"noxa/internal/query"
	noxtest "noxa/v1"
)

func TestRoleUnaryRecoveryContainsHandlerAndAuthenticationPanics(t *testing.T) {
	const secret = "do-not-log-unary-secret"
	core, observed := observer.New(zap.ErrorLevel)
	backend := &roleGRPCBackend{authenticate: func(context.Context, string, string, string) (auth.IntegrationPrincipal, error) {
		panic(panicSecret{value: secret})
	}}
	srv := New("127.0.0.1:0", backend, nil, zap.New(core), query.New("", zap.NewNop(), backend))
	info := &grpc.UnaryServerInfo{FullMethod: noxtest.Control_Authenticate_FullMethodName}
	_, err := srv.unaryRecovery(t.Context(), nil, info, func(context.Context, any) (any, error) {
		panic(panicSecret{value: secret})
	})
	assertInternalPanicResponse(t, err)
	assertSafePanicLog(t, observed, "unary", info.FullMethod, secret)

	observed.TakeAll()
	_, err = srv.unaryRecovery(t.Context(), nil, info, func(ctx context.Context, _ any) (any, error) {
		_, err := srv.authenticateRoleIntegration(ctx, backend, "integration", "pw")
		return nil, err
	})
	assertInternalPanicResponse(t, err)
	assertSafePanicLog(t, observed, "unary", info.FullMethod, secret)
	if _, err := srv.unaryRecovery(t.Context(), nil, info, func(context.Context, any) (any, error) {
		return "ok", nil
	}); err != nil {
		t.Fatalf("recovery poisoned later unary call: %v", err)
	}
}

func TestRoleStreamRecoveryContainsHandlerPanic(t *testing.T) {
	const secret = "do-not-log-stream-secret"
	core, observed := observer.New(zap.ErrorLevel)
	srv := New("127.0.0.1:0", nil, nil, zap.New(core), nil)
	info := &grpc.StreamServerInfo{FullMethod: noxtest.Events_Subscribe_FullMethodName}
	err := srv.streamRecovery(nil, nil, info, func(any, grpc.ServerStream) error {
		panic(panicSecret{value: secret})
	})
	assertInternalPanicResponse(t, err)
	assertSafePanicLog(t, observed, "stream", info.FullMethod, secret)
	if err := srv.streamRecovery(nil, nil, info, func(any, grpc.ServerStream) error { return nil }); err != nil {
		t.Fatalf("recovery poisoned later stream call: %v", err)
	}
}
