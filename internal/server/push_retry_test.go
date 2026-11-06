package server

import (
	"context"
	"errors"
	"net"
	"os"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"go.uber.org/zap"

	"noxa/internal/config"
	"noxa/internal/metrics"
	"noxa/internal/webpush"
)

type scriptedSender struct {
	mu      sync.Mutex
	results []error
	calls   []time.Time
}

func (s *scriptedSender) PublicKey() string { return "k" }

func (s *scriptedSender) Send(context.Context, webpush.Subscription, webpush.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, time.Now())
	if len(s.results) == 0 {
		return nil
	}
	err := s.results[0]
	s.results = s.results[1:]
	return err
}

type pushRecorder struct {
	metrics.Noop
	mu      sync.Mutex
	results []string
}

func (r *pushRecorder) IncPushDelivery(result string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.results = append(r.results, result)
}

func retryServer(sender *scriptedSender, rec *pushRecorder) *TCPServer {
	return New(&config.Config{}, zap.NewNop(), &Deps{Push: sender, Metrics: rec})
}

func TestSendPushRetriesTemporaryFailures(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sender := &scriptedSender{results: []error{
			&webpush.StatusError{Status: 503},
			&webpush.StatusError{Status: 429, RetryAfter: 4 * time.Second},
			nil,
		}}
		rec := &pushRecorder{}
		s := retryServer(sender, rec)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		start := time.Now()
		if err := s.sendPushWithRetry(ctx, webpush.Subscription{}, webpush.Message{}); err != nil {
			t.Fatalf("sendPushWithRetry = %v", err)
		}
		if len(sender.calls) != 3 {
			t.Fatalf("attempts = %d, want 3", len(sender.calls))
		}
		// Default 2 s backoff, then the service's 4 s Retry-After.
		if d1, d2 := sender.calls[1].Sub(start), sender.calls[2].Sub(sender.calls[1]); d1 != 2*time.Second || d2 != 4*time.Second {
			t.Fatalf("waits = %s, %s; want 2s, 4s", d1, d2)
		}
		if len(rec.results) != 2 || rec.results[0] != "retry" || rec.results[1] != "retry" {
			t.Fatalf("metrics = %v", rec.results)
		}
	})
}

func TestSendPushDoesNotRetryPermanentFailures(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, failure := range []error{webpush.ErrGone, &webpush.StatusError{Status: 400}, errors.New("encrypt: bad key")} {
			sender := &scriptedSender{results: []error{failure}}
			s := retryServer(sender, &pushRecorder{})
			if err := s.sendPushWithRetry(context.Background(), webpush.Subscription{}, webpush.Message{}); !errors.Is(err, failure) {
				t.Fatalf("error = %v, want %v", err, failure)
			}
			if len(sender.calls) != 1 {
				t.Fatalf("%v retried %d times", failure, len(sender.calls)-1)
			}
		}
	})
}

func TestSendPushStopsWhenTheWaitExceedsTheDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		long := &webpush.StatusError{Status: 429, RetryAfter: time.Minute}
		sender := &scriptedSender{results: []error{long}}
		s := retryServer(sender, &pushRecorder{})
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		start := time.Now()
		if err := s.sendPushWithRetry(ctx, webpush.Subscription{}, webpush.Message{}); !errors.Is(err, long) {
			t.Fatalf("error = %v", err)
		}
		if time.Since(start) != 0 || len(sender.calls) != 1 {
			t.Fatalf("waited %s over %d attempts; must give up at once", time.Since(start), len(sender.calls))
		}
	})
}

func TestPushRetryableClassifiesErrors(t *testing.T) {
	cases := map[error]bool{
		&webpush.StatusError{Status: 500}:                     true,
		&webpush.StatusError{Status: 404}:                     false,
		&net.OpError{Op: "dial", Err: os.ErrDeadlineExceeded}: true,
		context.DeadlineExceeded:                              false,
		context.Canceled:                                      false,
		webpush.ErrGone:                                       false,
	}
	for err, want := range cases {
		if got := pushRetryable(err); got != want {
			t.Errorf("pushRetryable(%v) = %v, want %v", err, got, want)
		}
	}
}
