package channels

import (
	"context"
	"errors"
	"time"

	"go.uber.org/zap"

	"noxa/internal/authorization"
)

func (m *ChannelManager) requestPublicPool() {
	select {
	case m.publicPoolWake <- struct{}{}:
	default:
	}
}

// publicPoolMinInterval bounds global policy publications and database writes
// even when one member continuously joins and leaves a managed room.
const publicPoolMinInterval = 5 * time.Second

// MaintainPublicChannels coalesces membership changes. Idle ticks inspect RAM
// only; the database is touched solely when the pool actually needs resizing.
func (m *ChannelManager) MaintainPublicChannels(ctx context.Context, authority *authorization.Authority) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	recoverPolicy := false
	var nextReconcile time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.publicPoolWake:
		case <-ticker.C:
		}
		if !recoverPolicy && !m.publicPoolNeedsWork() {
			continue
		}
		if delay := time.Until(nextReconcile); delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
		// Churn that settles during the cooldown needs no storage work.
		if !recoverPolicy && !m.publicPoolNeedsWork() {
			continue
		}
		nextReconcile = time.Now().Add(publicPoolMinInterval)
		if recoverPolicy {
			recovery, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := authority.Reload(recovery)
			cancel()
			if err != nil {
				m.logger.Warn("public channel policy recovery pending", zap.Error(err))
				continue
			}
			recoverPolicy = false
		}
		if !m.publicPoolNeedsWork() {
			continue
		}
		work, cancel := context.WithTimeout(ctx, 10*time.Second)
		policy, err := authority.RolePolicy(work)
		if err == nil {
			_, err = authority.ChangeLifecyclePolicy(work, policy.Revision, func(ctx context.Context) (authorization.RolePolicy, error) {
				m.treeMu.Lock()
				defer m.treeMu.Unlock()
				if !m.publicPoolNeedsWork() {
					return authorization.RolePolicy{}, authorization.ErrLifecycleUnchanged
				}
				occupied := map[int64]bool{}
				for _, client := range m.state.ListClients() {
					if client.ChannelID > 0 {
						occupied[client.ChannelID] = true
					}
				}
				return m.store.ReconcilePublicChannels(ctx, policy.Revision, occupied)
			})
		}
		if err != nil && !errors.Is(err, authorization.ErrLifecycleUnchanged) {
			recoverPolicy = true
			m.logger.Warn("public channel maintenance pending", zap.Error(err))
		}
		cancel()
	}
}

func (m *ChannelManager) publicPoolNeedsWork() bool {
	empty := 0
	home := false
	for _, ch := range m.state.ListChannels() {
		if ch.SystemKind == "home" {
			home = true
		}
		if ch.SystemKind == "public" && ch.ClientCount == 0 {
			empty++
		}
	}
	return home && empty != 2
}
