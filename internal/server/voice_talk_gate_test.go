package server

import (
	"fmt"
	"sync/atomic"
	"testing"

	"noxa/internal/authorization"
)

// canTalk must never read a missing or failed policy as permission; only a
// momentarily busy gate may reuse the speaker's last real decision.
func TestResolveTalkFailsClosed(t *testing.T) {
	var last atomic.Pointer[talkDecision]
	unavailable := fmt.Errorf("reload: %w", authorization.ErrAuthorizationUnavailable)
	if resolveTalk(&last, 1, false, unavailable) {
		t.Fatal("unavailable policy granted talk")
	}
	if resolveTalk(&last, 1, false, authorization.ErrAuthorizationBusy) {
		t.Fatal("busy gate granted talk without a previous decision")
	}
	if !resolveTalk(&last, 1, true, nil) {
		t.Fatal("allowed evaluation denied")
	}
	if !resolveTalk(&last, 1, false, authorization.ErrAuthorizationBusy) {
		t.Fatal("busy gate dropped an allowed speaker (flapping)")
	}
	if resolveTalk(&last, 2, false, authorization.ErrAuthorizationBusy) {
		t.Fatal("busy gate reused a decision from another channel")
	}
	if resolveTalk(&last, 1, false, authorization.ErrEnforcementPending) {
		t.Fatal("failed enforcement granted talk")
	}
	if resolveTalk(&last, 1, false, authorization.ErrAuthorizationBusy) {
		t.Fatal("a failed policy did not clear the remembered decision")
	}
	if !resolveTalk(&last, 1, true, nil) || resolveTalk(&last, 1, false, nil) {
		t.Fatal("evaluations must be authoritative")
	}
	if resolveTalk(&last, 1, false, authorization.ErrAuthorizationBusy) {
		t.Fatal("busy gate revived a revoked speaker")
	}
}
