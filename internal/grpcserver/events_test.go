package grpcserver

import (
	"context"
	"encoding/base64"
	"testing"
	"time"

	"google.golang.org/grpc/metadata"

	"noxa/internal/eventbus"
)

// withAuth attaches admin credentials to an existing context.
func withAuth(ctx context.Context) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "authorization",
		"Basic "+base64.StdEncoding.EncodeToString([]byte("admin-uid:pw")))
}

// waitForSubscribers waits until the bus reports n subscribers.
func waitForSubscribers(t *testing.T, bus *eventbus.Bus, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if bus.Stats().Subscribers == n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("subscriber count = %d, want %d", bus.Stats().Subscribers, n)
}
