package grpcserver

import (
	"slices"
	"testing"

	"noxa/internal/auth"
	noxav1 "noxa/v1"
)

// Every Control RPC an integration can reach needs a scope; the closed ones
// are listed explicitly so a new RPC fails this test until it is mapped.
func TestEveryIntegrationRPCHasAScope(t *testing.T) {
	closed := []string{"Authenticate", "QueryPermissions", "DeleteChannel", "CreateChannel",
		"StartFileTransfer", "GetFileTransferStatus", "CancelFileTransfer"}
	for _, method := range noxav1.Control_ServiceDesc.Methods {
		if slices.Contains(closed, method.MethodName) {
			continue
		}
		full := "/" + noxav1.Control_ServiceDesc.ServiceName + "/" + method.MethodName
		scope, ok := MethodScope(full)
		if !ok || !slices.Contains(auth.IntegrationScopes, scope) {
			t.Errorf("%s has no valid scope (%q)", full, scope)
		}
	}
	if scope, _ := MethodScope(noxav1.Events_Subscribe_FullMethodName); scope != auth.ScopeEventsRead {
		t.Errorf("event subscription scope = %q", scope)
	}
	for method := range methodScopes {
		if !methodAllowedByScope(auth.IntegrationPrincipal{}, method) {
			t.Fatalf("legacy integration lost %s", method)
		}
	}
}
