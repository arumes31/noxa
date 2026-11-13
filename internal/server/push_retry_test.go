package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/url"
	"os"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"go.uber.org/zap"

	"noxa/internal/config"
	"noxa/internal/metrics"
	"noxa/internal/store"
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

// fixedPush prepares the same request for every attempt.
func fixedPush(sub webpush.Subscription) pushPrepare {
	return func(context.Context) (webpush.Subscription, webpush.Message, error) {
		return sub, webpush.Message{}, nil
	}
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
		if err := s.sendPushWithRetry(ctx, "u", fixedPush(webpush.Subscription{})); err != nil {
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
			if err := s.sendPushWithRetry(context.Background(), "u", fixedPush(webpush.Subscription{})); !errors.Is(err, failure) {
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
		if err := s.sendPushWithRetry(ctx, "u", fixedPush(webpush.Subscription{})); !errors.Is(err, long) {
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
		// Certificate failures are wrapped in *url.Error, itself a net.Error,
		// but a retry cannot make them verify.
		&url.Error{Op: "Post", URL: "https://push.example", Err: x509.UnknownAuthorityError{}}:                                                     false,
		&url.Error{Op: "Post", URL: "https://push.example", Err: &tls.CertificateVerificationError{Err: x509.HostnameError{Host: "push.example"}}}: false,
		&url.Error{Op: "Post", URL: "https://push.example", Err: &net.OpError{Op: "dial", Err: os.ErrDeadlineExceeded}}:                            true,
	}
	for err, want := range cases {
		if got := pushRetryable(err); got != want {
			t.Errorf("pushRetryable(%v) = %v, want %v", err, got, want)
		}
	}
}

func TestPushSlotWaitTimeoutRecordsNothing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pushes := &fakePushes{fakeDevices: &fakeDevices{}}
		sub := store.PushSubscription{DeviceID: "d1", Principal: "u", Endpoint: "https://push.example/1"}
		_ = pushes.SavePushSubscription(t.Context(), sub)
		sender := &scriptedSender{}
		s := New(&config.Config{}, zap.NewNop(), &Deps{Push: sender, Devices: pushes})
		for range pushConcurrency {
			s.push.sem <- struct{}{} // every global slot busy
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		s.deliverPush(ctx, sub, pushNotice{Kind: pushKindDM, Tag: "dm:ivy"})
		if len(sender.calls) != 0 || pushes.records != 0 {
			t.Fatalf("sends = %d, recorded results = %d; a send that never left must record nothing", len(sender.calls), pushes.records)
		}
	})
}

// gateSender holds every send until released and counts sends per endpoint.
type gateSender struct {
	mu      sync.Mutex
	started map[string]int
	release chan struct{}
}

func (g *gateSender) PublicKey() string { return "k" }

func (g *gateSender) Send(ctx context.Context, sub webpush.Subscription, _ webpush.Message) error {
	g.mu.Lock()
	g.started[sub.Endpoint]++
	g.mu.Unlock()
	select {
	case <-g.release:
	case <-ctx.Done():
	}
	return nil
}

func TestPushLimitsSendsInFlightPerUser(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := &gateSender{started: map[string]int{}, release: make(chan struct{})}
		s := New(&config.Config{}, zap.NewNop(), &Deps{Push: g})
		var wg sync.WaitGroup
		send := func(principal string) {
			wg.Go(func() {
				_ = s.sendPushOnce(context.Background(), principal, fixedPush(webpush.Subscription{Endpoint: principal}))
			})
		}
		for range pushPerUser + 1 {
			send("busy")
		}
		send("other")
		synctest.Wait()
		g.mu.Lock()
		busy, other := g.started["busy"], g.started["other"]
		g.mu.Unlock()
		close(g.release)
		wg.Wait()
		if busy != pushPerUser || other != 1 {
			t.Fatalf("in flight: busy = %d, other = %d; want %d and 1", busy, other, pushPerUser)
		}
		if g.started["busy"] != pushPerUser+1 || len(s.push.userSem) != 0 {
			t.Fatalf("busy sends = %d, user slots left = %d", g.started["busy"], len(s.push.userSem))
		}
	})
}

func TestPushRetryWithoutSlotKeepsTheDeliveryError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		unavailable := &webpush.StatusError{Status: 503}
		sender := &scriptedSender{results: []error{unavailable}}
		s := retryServer(sender, &pushRecorder{})
		fill := sync.OnceFunc(func() {
			// Other sends take every global slot during the retry backoff.
			go func() {
				for range pushConcurrency {
					s.push.sem <- struct{}{}
				}
			}()
		})
		prepare := func(context.Context) (webpush.Subscription, webpush.Message, error) {
			fill()
			return webpush.Subscription{}, webpush.Message{}, nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.sendPushWithRetry(ctx, "u", prepare); !errors.Is(err, unavailable) {
			t.Fatalf("error = %v, want the 503 from the attempt that was made", err)
		}
		if len(sender.calls) != 1 {
			t.Fatalf("attempts = %d, want 1", len(sender.calls))
		}
	})
}

func TestPushRebuildsPayloadAfterSlotWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pushes := &fakePushes{fakeDevices: &fakeDevices{}}
		sub := store.PushSubscription{DeviceID: "d1", Principal: "u", Endpoint: "https://push.example/1", Auth: []byte("auth")}
		_ = pushes.SavePushSubscription(t.Context(), sub)
		sender := &fakeSender{sent: make(chan sentPush, 1), gone: map[string]bool{}}
		s := New(&config.Config{}, zap.NewNop(), &Deps{Push: sender, Devices: pushes})
		for range pushConcurrency {
			s.push.sem <- struct{}{}
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			s.deliverPush(context.Background(), sub, pushNotice{Kind: pushKindDM, Tag: "dm:ivy", From: "Ivy"})
		}()
		synctest.Wait() // queued behind the busy slots
		if err := pushes.SetPushPreferences(t.Context(), "u", "d1", true, false); err != nil {
			t.Fatal(err)
		}
		for range pushConcurrency {
			<-s.push.sem
		}
		<-done
		got := <-sender.sent
		if got.payload.From != "" || got.payload.Kind != pushKindDM {
			t.Fatalf("payload = %+v; HideDetails set while queued must apply", got.payload)
		}
	})
}
