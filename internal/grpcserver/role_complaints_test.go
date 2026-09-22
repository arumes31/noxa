package grpcserver

import (
	"context"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
	noxav1 "noxa/v1"
)

type complaintGRPCBackend struct {
	*roleGRPCBackend
	reads  atomic.Int32
	clears atomic.Int32
	denied atomic.Bool
}

func (b *complaintGRPCBackend) WithIntegrationComplaints(ctx context.Context, _ auth.IntegrationPrincipal, q netproto.ComplaintQuery, deliver func(context.Context, netproto.ComplaintPage) error) error {
	b.reads.Add(1)
	if b.denied.Load() {
		return auth.ErrIntegrationDenied
	}
	if q.AfterID != 10 || q.Limit != 1 {
		return authorization.ErrRoleInvalid
	}
	return deliver(ctx, netproto.ComplaintPage{Entries: []netproto.ComplaintPageEntry{{ID: 11, ComplaintEntry: netproto.ComplaintEntry{TargetUniqueID: "target", TargetNickname: "Target", FromUniqueID: "reporter", FromNickname: "Reporter", Reason: "Reason | with\nnewlines", CreatedAt: 100}}}, NextAfterID: 11})
}

func (b *complaintGRPCBackend) ClearIntegrationComplaints(_ context.Context, _ auth.IntegrationPrincipal, q netproto.ComplaintClear) (netproto.ComplaintClearResult, error) {
	n := b.clears.Add(1)
	if b.denied.Load() {
		return netproto.ComplaintClearResult{}, auth.ErrIntegrationDenied
	}
	if q.TargetUniqueID != "target" || q.FromUniqueID != "reporter" {
		return netproto.ComplaintClearResult{}, authorization.ErrRoleInvalid
	}
	if n == 1 {
		return netproto.ComplaintClearResult{Deleted: 2}, nil
	}
	return netproto.ComplaintClearResult{}, nil
}

func TestRoleGRPCComplaints(t *testing.T) {
	b := &complaintGRPCBackend{roleGRPCBackend: &roleGRPCBackend{authenticate: func(context.Context, string, string, string) (auth.IntegrationPrincipal, error) {
		return auth.IntegrationPrincipal{}, nil
	}}}
	c := noxav1.NewControlClient(dialGRPC(t, startGRPC(t, b, nil)))
	ctx := func() context.Context { return roleAuthCtx(t, "integration", "pw") }
	request := &noxav1.ListComplaintsRequest{AfterId: 10, Limit: 1}
	page, err := c.ListComplaints(ctx(), request)
	want := &noxav1.ListComplaintsResponse{Entries: []*noxav1.ComplaintRecord{{Id: 11, TargetUniqueId: "target", TargetNickname: "Target", FromUniqueId: "reporter", FromNickname: "Reporter", Reason: "Reason | with\nnewlines", CreatedAt: 100}}, NextAfterId: 11}
	if err != nil || !proto.Equal(page, want) {
		t.Fatalf("page: %v %v", page, err)
	}
	for _, invalid := range []*noxav1.ListComplaintsRequest{{AfterId: -1}, {Limit: -1}, {Limit: 101}} {
		if _, err := c.ListComplaints(ctx(), invalid); status.Code(err) != codes.InvalidArgument {
			t.Fatal(err)
		}
	}
	if _, err := c.ClearComplaints(ctx(), &noxav1.ClearComplaintsRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Fatal(err)
	}
	if b.reads.Load() != 1 || b.clears.Load() != 0 {
		t.Fatal("invalid input reached backend")
	}
	clear := &noxav1.ClearComplaintsRequest{TargetUniqueId: "target", FromUniqueId: "reporter"}
	for _, deleted := range []int64{2, 0} {
		result, err := c.ClearComplaints(ctx(), clear)
		if err != nil || result.GetDeleted() != deleted {
			t.Fatalf("clear acknowledgement: %v %v", result, err)
		}
	}
	if b.reads.Load() != 1 {
		t.Fatal("mutation performed a protected refresh")
	}
	b.denied.Store(true)
	if _, err := c.ListComplaints(ctx(), request); status.Code(err) != codes.Unauthenticated {
		t.Fatal(err)
	}
	if _, err := c.ClearComplaints(ctx(), clear); status.Code(err) != codes.Unauthenticated {
		t.Fatal(err)
	}
	legacy := noxav1.NewControlClient(dialGRPC(t, startGRPC(t, &stubBackend{}, nil)))
	if _, err := legacy.ListComplaints(roleAuthCtx(t, "admin-uid", "pw"), request); status.Code(err) != codes.FailedPrecondition {
		t.Fatal(err)
	}
	if _, err := legacy.ClearComplaints(roleAuthCtx(t, "admin-uid", "pw"), clear); status.Code(err) != codes.FailedPrecondition {
		t.Fatal(err)
	}
}
