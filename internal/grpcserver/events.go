// events.go implements Events.Subscribe on top of the shared event bus (232).
package grpcserver

import (
	"strconv"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"noxa/internal/auth"
	"noxa/internal/eventbus"
	"noxa/internal/query"
	noxav1 "noxa/v1"
)

// formatInt renders a channel id as the string the proto schema uses.
func formatInt(v int64) string {
	if v == 0 {
		return ""
	}
	return strconv.FormatInt(v, 10)
}

// eventsService streams bus events to gRPC subscribers.
type eventsService struct {
	noxav1.UnimplementedEventsServer
	bus     *eventbus.Bus
	logger  *zap.Logger
	backend query.Backend
}

// Subscribe streams server events until the client goes away or the bus drops
// the subscriber for not keeping up.
func (e *eventsService) Subscribe(req *noxav1.SubscribeEventsRequest, stream grpc.ServerStreamingServer[noxav1.Event]) error {
	principal, ok := stream.Context().Value(integrationPrincipalKey{}).(auth.IntegrationPrincipal)
	if !ok {
		return status.Error(codes.FailedPrecondition, "role integration required")
	}
	return e.subscribeRoleEvents(req, stream, principal)
}
