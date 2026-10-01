package grpcserver

import (
	"context"
	"errors"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"noxa/internal/auth"
	"noxa/internal/broadcast"
	"noxa/internal/integrationevents"
	"noxa/internal/netproto"
	"noxa/internal/query"
	noxav1 "noxa/v1"
)

func (s *Server) roleStreamAuth(service any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler, backend query.RoleIntegrationBackend) error {
	if info.FullMethod != noxav1.Events_Subscribe_FullMethodName {
		return status.Error(codes.FailedPrecondition, "RPC unavailable with roles-v1 authorization")
	}
	if _, ok := backend.(query.RoleEventBackend); !ok {
		return status.Error(codes.FailedPrecondition, "role-aware event streaming is unavailable")
	}
	const modelHeader = "noxa-authorization-model"
	if err := stream.SetHeader(metadata.Pairs(modelHeader, netproto.AuthorizationModelRolesV1)); err != nil {
		return err
	}
	md, _ := metadata.FromIncomingContext(stream.Context())
	models := md.Get(modelHeader)
	if len(models) != 1 || models[0] != netproto.AuthorizationModelRolesV1 {
		return status.Error(codes.FailedPrecondition, "roles-v1 authorization required; send noxa-authorization-model: roles-v1")
	}
	identifier, password, err := s.basicCredentials(stream.Context())
	if err != nil {
		return err
	}
	p, err := s.authenticateRoleIntegration(stream.Context(), backend, identifier, password)
	if err != nil {
		return roleStatus(err)
	}
	select {
	case s.roleStreamSlots <- struct{}{}:
		defer func() { <-s.roleStreamSlots }()
	default:
		return status.Error(codes.ResourceExhausted, "role event subscription limit reached")
	}
	ctx, cancel := context.WithTimeout(stream.Context(), s.streamLifetime())
	defer cancel()
	ctx = context.WithValue(ctx, integrationPrincipalKey{}, p)
	err = handler(service, &contextServerStream{ServerStream: stream, ctx: ctx})
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return status.Error(codes.DeadlineExceeded, "stream lifetime expired; reconnect")
	}
	return err
}

var roleStructuralBusTypes = []string{"user_joined", "user_left", "user_moved", "channel_created", "channel_deleted", "channel_updated", "status_changed", "member_voice_changed", "kicked"}

func roleEventTypes(req *noxav1.SubscribeEventsRequest) (snapshot, speaking bool, err error) {
	if len(req.GetEventTypes()) == 0 {
		return true, true, nil
	}
	for _, kind := range req.GetEventTypes() {
		switch kind {
		case noxav1.EventType_EVENT_TYPE_ROLE_SNAPSHOT:
			snapshot = true
		case noxav1.EventType_EVENT_TYPE_USER_SPEAKING:
			speaking = true
		default:
			return false, false, status.Error(codes.InvalidArgument, "roles-v1 accepts ROLE_SNAPSHOT and USER_SPEAKING; raw structural events are replaced by snapshots")
		}
	}
	if !snapshot {
		return false, false, status.Error(codes.InvalidArgument, "roles-v1 requires ROLE_SNAPSHOT for current visibility and speaking state")
	}
	return snapshot, speaking, nil
}

func (e *eventsService) subscribeRoleEvents(req *noxav1.SubscribeEventsRequest, stream grpc.ServerStreamingServer[noxav1.Event], principal auth.IntegrationPrincipal) error {
	backend, ok := e.backend.(query.RoleEventBackend)
	reads, canRead := e.backend.(query.RoleIntegrationBackend)
	if !ok || !canRead || e.bus == nil {
		return status.Error(codes.Unavailable, "role event stream unavailable")
	}
	wantSnapshot, wantSpeaking, err := roleEventTypes(req)
	if err != nil {
		return err
	}
	delivery, err := newProtectedStreamDelivery(stream.Context(), stream.SetHeader)
	if err != nil {
		return err
	}
	defer delivery.close()
	var busTypes []string
	if wantSnapshot {
		busTypes = append(busTypes, roleStructuralBusTypes...)
	}
	if wantSpeaking {
		busTypes = append(busTypes, "speaking_changed")
	}
	sub := e.bus.Subscribe("role-grpc:"+principal.UniqueID(), busTypes, 0)
	if sub == nil {
		return status.Error(codes.Unavailable, "event stream unavailable")
	}
	defer sub.Unsubscribe()
	var state integrationevents.DeliveryState
	send := func(ctx context.Context, projection broadcast.IntegrationEvent) error {
		return state.Send(ctx, projection, wantSpeaking, func(ctx context.Context, message *noxav1.Event) error {
			return delivery.send(ctx, message, func() error { return stream.Send(message) })
		})
	}
	refresh := func() error {
		return reads.WithIntegrationSnapshot(stream.Context(), principal, func(ctx context.Context, snapshot *broadcast.TreeSnapshot) error {
			return send(ctx, broadcast.IntegrationEvent{Snapshot: snapshot})
		})
	}
	if err := refresh(); err != nil {
		return roleStatus(err)
	}
	// Policy and admission changes need not publish a structural bus event.
	// Refresh even during silence; every event itself still checks immediately.
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if sub.Dropped() != 0 {
			return status.Error(codes.ResourceExhausted, "event stream fell behind; resync required")
		}
		select {
		case <-stream.Context().Done():
			return roleStatus(stream.Context().Err())
		case <-ticker.C:
			if err := refresh(); err != nil {
				return roleStatus(err)
			}
		case event, open := <-sub.C:
			if !open {
				return status.Error(codes.Unavailable, "event stream unavailable; reconnect and resync")
			}
			if err := backend.WithIntegrationEvent(stream.Context(), principal, event, send); err != nil {
				return roleStatus(err)
			}
		}
	}
}
