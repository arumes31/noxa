package grpcserver

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"noxa/internal/auth"
	"noxa/internal/broadcast"
	"noxa/internal/query"
	noxav1 "noxa/v1"
)

const maxProtectedGRPCResponseBytes = 8 << 20

// protectedUnaryRead keeps the backend callback's policy lease alive after the
// unary handler returns, until the owned socket observes this response's final
// frame. gRPC cancels the RPC context when it queues trailers, before writing
// them, so delivery uses its own bounded lifetime with the original deadline.
func protectedUnaryRead[T proto.Message](ctx context.Context, logger *zap.Logger, operation func(context.Context, func(T) error) error) (T, error) {
	var zero T
	conn, ok := ctx.Value(roleDeliveryConnKey{}).(*roleDeliveryConn)
	if !ok || conn.tracker == nil {
		return zero, status.Error(codes.FailedPrecondition, "protected read transport required")
	}
	if err := ctx.Err(); err != nil {
		return zero, roleStatus(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	if original, ok := ctx.Deadline(); ok && original.Before(deadline) {
		deadline = original
	}
	readCtx, cancel := context.WithDeadline(context.WithoutCancel(ctx), deadline)
	fence, err := conn.register()
	if err != nil {
		cancel()
		return zero, status.Error(codes.Unavailable, "protected read transport unavailable")
	}
	type outcome struct {
		value T
		err   error
	}
	ready := make(chan outcome, 1)
	expired := make(chan struct{})
	var publicationMu sync.Mutex
	publishing := false
	stop := context.AfterFunc(readCtx, func() {
		publicationMu.Lock()
		if publishing {
			conn.expire(fence.token)
		} else {
			// No protected bytes have entered gRPC. Cancel the backend read
			// without closing unrelated RPCs on this HTTP/2 connection.
			conn.finish(fence.token)
		}
		publicationMu.Unlock()
		close(expired)
	})
	conn.tracker.workers.Add(1)
	go func() {
		defer conn.tracker.workers.Done()
		sent := false
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.Error("protected grpc read panicked")
				if !sent {
					ready <- outcome{err: status.Error(codes.Internal, "internal error")}
				}
			}
			conn.finish(fence.token)
			if !stop() {
				<-expired
			}
			cancel()
		}()
		err := operation(readCtx, func(value T) error {
			publicationMu.Lock()
			if err := readCtx.Err(); err != nil {
				publicationMu.Unlock()
				return err
			}
			if sent || proto.Size(value) > maxProtectedGRPCResponseBytes {
				publicationMu.Unlock()
				return status.Error(codes.ResourceExhausted, "protected response exceeds limit")
			}
			if err := grpc.SetHeader(ctx, metadata.Pairs(roleDeliveryHeader, fence.token)); err != nil {
				publicationMu.Unlock()
				return err
			}
			publishing = true
			sent = true
			ready <- outcome{value: value}
			publicationMu.Unlock()
			<-fence.done
			return nil
		})
		if !sent {
			if err == nil {
				err = status.Error(codes.Unavailable, "protected response unavailable")
			}
			ready <- outcome{err: roleStatus(err)}
		}
	}()
	select {
	case result := <-ready:
		return result.value, result.err
	case <-ctx.Done():
		cancel()
		return zero, roleStatus(ctx.Err())
	case <-readCtx.Done():
		// Worker cleanup cancels this private context after publishing its
		// outcome. Preserve that result if both cases became ready together.
		select {
		case result := <-ready:
			return result.value, result.err
		default:
			return zero, roleStatus(readCtx.Err())
		}
	}
}

func (c *controlService) listRoleChannels(ctx context.Context, root int64) (*noxav1.ListChannelsResponse, error) {
	p, authenticated := ctx.Value(integrationPrincipalKey{}).(auth.IntegrationPrincipal)
	b, supported := c.backend.(query.RoleIntegrationBackend)
	if !authenticated || !supported {
		return nil, status.Error(codes.FailedPrecondition, "role integration required")
	}
	return protectedUnaryRead(ctx, c.logger, func(ctx context.Context, deliver func(*noxav1.ListChannelsResponse) error) error {
		return b.WithIntegrationSnapshot(ctx, p, func(_ context.Context, snapshot *broadcast.TreeSnapshot) error {
			channels := make([]query.ChannelInfo, 0, snapshot.TotalChannels)
			var visit func([]*broadcast.ChannelNode)
			visit = func(nodes []*broadcast.ChannelNode) {
				for _, node := range nodes {
					channels = append(channels, query.ChannelInfo{ChannelID: node.ChannelID, ParentID: node.ParentID, Name: node.Name, Type: node.ChannelType, MaxClients: node.MaxClients, ClientCount: node.ClientCount})
					visit(node.Children)
				}
			}
			visit(snapshot.RootChannels)
			return deliver(c.channelListResponse(channels, root))
		})
	})
}
