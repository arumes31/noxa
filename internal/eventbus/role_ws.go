package eventbus

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	"go.uber.org/zap"
	"golang.org/x/net/websocket"
	"google.golang.org/protobuf/encoding/protojson"

	"noxa/internal/auth"
	"noxa/internal/broadcast"
	"noxa/internal/integrationevents"
	noxav1 "noxa/v1"
)

// RoleBackend retains fresh admission, policy and metadata through each
// delivery callback. Implementations must not enable legacy administrator paths.
type RoleBackend interface {
	RoleIntegrationsEnabled() bool
	AuthenticateIntegration(context.Context, string, string, string) (auth.IntegrationPrincipal, error)
	WithIntegrationSnapshot(context.Context, auth.IntegrationPrincipal, func(context.Context, *broadcast.TreeSnapshot) error) error
	WithIntegrationEvent(context.Context, auth.IntegrationPrincipal, Event, func(context.Context, broadcast.IntegrationEvent) error) error
}

// HandlerWithRoleBackend serves only roles-v1 integration event streams.
func HandlerWithRoleBackend(bus *Bus, backend RoleBackend, logger *zap.Logger, limiter *auth.LoginFailureLimiter, failures authFailureRecorder) http.Handler {
	if backend == nil || !backend.RoleIntegrationsEnabled() {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "roles-v1 integration backend unavailable", http.StatusServiceUnavailable)
		})
	}
	return newWSHandler(bus, logger, wsHandlerConfig{
		maxConnections: maxWSConnections, maxAuthAttempts: maxWSAuthAttempts,
		authWindow: wsAuthWindow, maxAuthBuckets: maxWSAuthBuckets, now: time.Now,
		loginLimiter: limiter, failures: failures, roleBackend: backend,
		streamLifetime: time.Hour,
	})
}

func roleWSTypes(types []string) (speaking bool, err error) {
	if len(types) == 0 {
		return true, nil
	}
	snapshot := false
	for _, kind := range types {
		switch kind {
		case "role_snapshot":
			snapshot = true
		case "speaking_changed":
			speaking = true
		default:
			return false, errInvalidEventFilter
		}
	}
	if !snapshot {
		return false, errInvalidEventFilter
	}
	return speaking, nil
}

// Capture the owned socket so cancellation can interrupt websocket's write
// mutex. Conn.Close alone first writes a close frame and can wait on that mutex.
type roleWSResponseWriter struct {
	http.ResponseWriter
	conn net.Conn
}

func (w *roleWSResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("websocket transport unavailable")
	}
	conn, rw, err := h.Hijack()
	if err != nil {
		return nil, nil, err
	}
	// Bound the upgrade flush too: the stream lifetime starts only after
	// websocket's handshake has completed, and HTTP timeouts are optional.
	if err := conn.SetDeadline(time.Now().Add(wsWriteTimeout)); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	w.conn = conn
	return conn, rw, err
}

func serveRoleStream(ctx context.Context, bus *Bus, conn *websocket.Conn, raw net.Conn, backend RoleBackend, principal auth.IntegrationPrincipal, speaking bool, lifetime time.Duration) {
	if raw == nil {
		return
	}
	if err := raw.SetDeadline(time.Time{}); err != nil {
		_ = raw.Close()
		return
	}
	if lifetime <= 0 {
		lifetime = time.Hour
	}
	ctx, cancel := context.WithTimeout(ctx, lifetime)
	defer cancel()
	closed := make(chan struct{})
	stopClose := context.AfterFunc(ctx, func() { _ = raw.Close(); close(closed) })
	peerGone := make(chan struct{})
	go func() {
		defer close(peerGone)
		defer cancel()
		var discard []byte
		for {
			if err := websocket.Message.Receive(conn, &discard); err != nil {
				return
			}
		}
	}()
	defer func() {
		_ = raw.Close()
		if !stopClose() {
			<-closed
		}
		<-peerGone
	}()
	types := []string{"user_joined", "user_left", "user_moved", "channel_created", "channel_deleted", "channel_updated", "status_changed", "member_voice_changed", "kicked"}
	if speaking {
		types = append(types, "speaking_changed")
	}
	sub := bus.Subscribe("role-ws:"+principal.UniqueID(), types, 0)
	if sub == nil {
		return
	}
	defer sub.Unsubscribe()
	var state integrationevents.DeliveryState
	send := func(ctx context.Context, projection broadcast.IntegrationEvent) error {
		return state.Send(ctx, projection, speaking, func(ctx context.Context, message *noxav1.Event) error {
			return writeRoleWSEvent(ctx, conn, raw, message)
		})
	}
	refresh := func() error {
		return backend.WithIntegrationSnapshot(ctx, principal, func(ctx context.Context, snapshot *broadcast.TreeSnapshot) error {
			return send(ctx, broadcast.IntegrationEvent{Snapshot: snapshot})
		})
	}
	if err := refresh(); err != nil {
		return
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if sub.Dropped() != 0 {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := refresh(); err != nil {
				return
			}
		case event, open := <-sub.C:
			if !open {
				return
			}
			if err := backend.WithIntegrationEvent(ctx, principal, event, send); err != nil {
				return
			}
		}
	}
}

func writeRoleWSEvent(ctx context.Context, conn *websocket.Conn, raw net.Conn, message *noxav1.Event) (err error) {
	data, err := (protojson.MarshalOptions{EmitUnpopulated: true}).Marshal(message)
	if err != nil {
		return err
	}
	if len(data) > integrationevents.MaxMessageBytes {
		return integrationevents.ErrResponseTooLarge
	}
	ctx, cancel := context.WithTimeout(ctx, wsWriteTimeout)
	defer cancel()
	closed := make(chan struct{})
	stopClose := context.AfterFunc(ctx, func() { _ = raw.Close(); close(closed) })
	complete := false
	defer func() {
		if !complete {
			_ = raw.Close()
		}
		if !stopClose() {
			<-closed
		}
	}()
	deadline, _ := ctx.Deadline()
	if err := raw.SetWriteDeadline(deadline); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Message.Send flushes the complete frame before returning. Retain the
	// backend callback until this synchronous write has finished or failed.
	if err := websocket.Message.Send(conn, string(data)); err != nil {
		return err
	}
	if err := raw.SetWriteDeadline(time.Time{}); err != nil {
		return err
	}
	complete = true
	return nil
}
