package query

import (
	"context"

	"noxa/internal/auth"
	"noxa/internal/broadcast"
	"noxa/internal/eventbus"
)

// RoleEventBackend projects queued events using current admission, visibility
// and activity state. A filtered event does not invoke the callback. Callers
// must finish bounded socket delivery inside the callback, without re-entry.
type RoleEventBackend interface {
	WithIntegrationEvent(context.Context, auth.IntegrationPrincipal, eventbus.Event, func(context.Context, broadcast.IntegrationEvent) error) error
}
