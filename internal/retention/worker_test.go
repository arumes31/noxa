package retention

import (
	"context"
	"errors"
	"go.uber.org/zap"
	"testing"
	"time"
)

func TestWorkerRunsAtStartupContinuesAfterFailureAndJoins(t *testing.T) {
	entered, exited := make(chan struct{}), make(chan struct{})
	stop := Start(context.Background(), zap.NewNop(),
		Task{"failure", func(context.Context, time.Time) error { return errors.New("unavailable") }},
		Task{"pending", func(ctx context.Context, _ time.Time) error {
			close(entered)
			<-ctx.Done()
			close(exited)
			return ctx.Err()
		}},
	)
	defer stop()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("startup tasks did not run")
	}
	stop()
	select {
	case <-exited:
	default:
		t.Fatal("stop returned before callback completed")
	}
}

func TestWorkerDoesNotRunWithCanceledParent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ran := false
	stop := Start(ctx, zap.NewNop(), Task{"must not run", func(context.Context, time.Time) error { ran = true; return nil }})
	stop()
	if ran {
		t.Fatal("ran with canceled context")
	}
}
