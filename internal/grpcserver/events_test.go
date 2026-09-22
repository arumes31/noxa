package grpcserver

import (
	"context"
	"encoding/base64"
	"io"
	"testing"
	"time"

	"google.golang.org/grpc/metadata"

	"noxa/internal/eventbus"
	noxav1 "noxa/v1"
)

type blockedEventStream struct {
	ctx         context.Context
	sendStarted chan struct{}
	release     chan struct{}
}

func newBlockedEventStream(t *testing.T) *blockedEventStream {
	t.Helper()
	return &blockedEventStream{
		ctx:         context.Background(),
		sendStarted: make(chan struct{}, 1),
		release:     make(chan struct{}),
	}
}

func (s *blockedEventStream) Context() context.Context     { return s.ctx }
func (s *blockedEventStream) SetHeader(metadata.MD) error  { return nil }
func (s *blockedEventStream) SendHeader(metadata.MD) error { return nil }
func (s *blockedEventStream) SetTrailer(metadata.MD)       {}
func (s *blockedEventStream) SendMsg(any) error            { return nil }
func (s *blockedEventStream) RecvMsg(any) error            { return io.EOF }
func (s *blockedEventStream) Send(*noxav1.Event) error {
	select {
	case s.sendStarted <- struct{}{}:
	default:
	}
	<-s.release
	return nil
}

type recordingEventStream struct {
	ctx  context.Context
	sent chan *noxav1.Event
}

func (s *recordingEventStream) Context() context.Context     { return s.ctx }
func (s *recordingEventStream) SetHeader(metadata.MD) error  { return nil }
func (s *recordingEventStream) SendHeader(metadata.MD) error { return nil }
func (s *recordingEventStream) SetTrailer(metadata.MD)       {}
func (s *recordingEventStream) SendMsg(any) error            { return nil }
func (s *recordingEventStream) RecvMsg(any) error            { return io.EOF }
func (s *recordingEventStream) Send(event *noxav1.Event) error {
	s.sent <- event
	return nil
}

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
