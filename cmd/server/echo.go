package main

import (
	"context"

	"noxa/internal/config"
)

type echoChannelStore interface {
	EnsureEchoChannel(context.Context, string) (int64, error)
}

func ensureEchoChannel(ctx context.Context, db echoChannelStore, cfg *config.Config) (int64, error) {
	// Preserve the historical empty-name YAML opt-out as well as the explicit
	// boolean switch, which works reliably with Docker environment variables.
	if !cfg.EchoChannelEnabled || cfg.EchoChannelName == "" {
		return 0, nil
	}
	return db.EnsureEchoChannel(ctx, cfg.EchoChannelName)
}
