package grpcserver

import (
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type panicSecret struct{ value string }

func assertInternalPanicResponse(t *testing.T, err error) {
	t.Helper()
	if status.Code(err) != codes.Internal || status.Convert(err).Message() != "internal error" {
		t.Fatalf("panic response = %v, want Internal/internal error", err)
	}
}

func assertSafePanicLog(t *testing.T, observed *observer.ObservedLogs, kind, method, secret string) {
	t.Helper()
	entries := observed.FilterLevelExact(zap.ErrorLevel).All()
	if len(entries) != 1 {
		t.Fatalf("panic error logs = %d, want 1", len(entries))
	}
	entry := entries[0]
	if entry.Message != "grpc handler panic" || strings.Contains(entry.Message, secret) {
		t.Fatalf("panic log message = %q", entry.Message)
	}
	fields := entry.ContextMap()
	if len(fields) != 3 || fields["rpc_kind"] != kind || fields["full_method"] != method || fields["panic_type"] != "grpcserver.panicSecret" {
		t.Fatalf("panic log fields = %#v", fields)
	}
	if strings.Contains(entry.ContextMap()["panic_type"].(string), secret) {
		t.Fatalf("panic type leaked secret: %#v", fields)
	}
}
