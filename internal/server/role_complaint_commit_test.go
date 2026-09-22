package server

import (
	"context"
	"errors"
	"testing"

	"go.uber.org/zap"
	"noxa/internal/authorization"
	"noxa/internal/config"
	"noxa/internal/netproto"
)

type cancelComplaintDelete struct {
	*fakeComplaints
	cancel context.CancelFunc
}

func (b *cancelComplaintDelete) DeleteComplaintsAgainst(ctx context.Context, target, reporter string) (int64, error) {
	n, err := b.fakeComplaints.DeleteComplaintsAgainst(ctx, target, reporter)
	b.cancel()
	return n, err
}

func TestComplaintClearAuditsCommittedDeleteAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	b := &cancelComplaintDelete{fakeComplaints: &fakeComplaints{}, cancel: cancel}
	if err := b.AddComplaint(ctx, "reporter", "target", "Reason"); err != nil {
		t.Fatal(err)
	}
	audit := &disconnectAuditContext{}
	authority, err := authorization.NewAuthority(t.Context(), serverRoleFixture(), func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	srv := New(&config.Config{}, zap.NewNop(), &Deps{Authority: authority, Complaints: b, Groups: audit})
	n, err := srv.clearComplaints(ctx, "actor", netproto.ComplaintClear{TargetUniqueID: "target"})
	if err != nil || n != 1 || !errors.Is(ctx.Err(), context.Canceled) || audit.calls != 1 || audit.err != nil {
		t.Fatalf("committed delete lost audit or acknowledgement: n=%d err=%v audit=%+v", n, err, audit)
	}
	if _, err := srv.clearComplaints(ctx, "actor", netproto.ComplaintClear{TargetUniqueID: "target"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if audit.calls != 1 {
		t.Fatal("canceled operation attempted another audit")
	}
}
