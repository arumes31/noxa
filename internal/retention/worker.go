// Package retention runs bounded housekeeping away from request paths.
package retention

import (
	"context"
	"time"

	"go.uber.org/zap"
)

type Task struct {
	Name string
	Run  func(context.Context, time.Time) error
}

// Start runs once at startup and then every five minutes. Stop cancels and joins
// the worker, so callers can safely close the database and recorder afterwards.
func Start(parent context.Context, logger *zap.Logger, tasks ...Task) (stop func()) {
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			for _, task := range tasks {
				if ctx.Err() != nil {
					return
				}
				batch, release := context.WithTimeout(ctx, 10*time.Second)
				err := task.Run(batch, time.Now().UTC())
				release()
				if err != nil && ctx.Err() == nil {
					logger.Warn("retention batch failed", zap.String("dataset", task.Name), zap.Error(err))
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() { cancel(); <-done }
}
