package channels

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"go.uber.org/zap"

	"noxa/internal/authorization"
	"noxa/internal/state"
	"noxa/internal/store"
)

var ErrRoleLifecycleRequired = errors.New("channel changes require the role lifecycle barrier")

func (m *ChannelManager) EditRoleChannel(ctx context.Context, actorID, channelID, revision int64, settings store.RoleChannelSettings) (authorization.RolePolicy, error) {
	m.treeMu.Lock()
	defer m.treeMu.Unlock()
	if !m.roleMode {
		return authorization.RolePolicy{}, ErrRoleLifecycleRequired
	}
	return m.store.EditRoleChannel(ctx, actorID, channelID, revision, settings)
}

// EnableRoleMode is one-way and must run before serving or loading timers.
// Legacy writers and cleanup must never mutate resources behind Authority.
func (m *ChannelManager) EnableRoleMode(authority *authorization.Authority) {
	m.treeMu.Lock()
	defer m.treeMu.Unlock()
	m.roleMode = true
	m.roleAuthority = authority
	m.mu.Lock()
	m.cancelAllCleanupLocked()
	m.mu.Unlock()
}

// ChangeRoleChannel is called only inside Authority.ChangeLifecyclePolicy.
// Release treeMu before returning: the authority's reconciliation calls back
// into this manager. Resource and policy writes share the store transaction.
func (m *ChannelManager) ChangeRoleChannel(ctx context.Context, actorID int64, change authorization.ChannelTreeChange, create *store.RoleChannelCreate) (authorization.RolePolicy, int64, error) {
	m.treeMu.Lock()
	defer m.treeMu.Unlock()
	if !m.roleMode {
		return authorization.RolePolicy{}, 0, ErrRoleLifecycleRequired
	}
	return m.store.ChangeRoleChannel(ctx, actorID, change, create)
}

// ReconcileRoleChannels repairs resources after a commit or explicit recovery.
// The caller holds Authority exclusively, then its metadata barrier. beforeRemove
// must synchronously detach deleted members from media and revoke file access;
// it must not re-enter Authority or this manager. A failure preserves the old
// membership for retry. Load and validate the whole tree before changing state.
func (m *ChannelManager) ReconcileRoleChannels(ctx context.Context, policy authorization.RolePolicy, beforeRemove func(DeleteResult) error) error {
	m.treeMu.Lock()
	defer m.treeMu.Unlock()
	if !m.roleMode || beforeRemove == nil {
		return authorization.ErrAuthorizationUnavailable
	}
	if _, err := authorization.NewRoleEvaluator(policy); err != nil {
		return err
	}
	var revision int64
	if err := m.store.DB().QueryRowContext(ctx, `SELECT revision FROM authorization_config WHERE singleton`).Scan(&revision); err != nil {
		return err
	}
	if revision != policy.Revision {
		return authorization.ErrAuthorizationUnavailable
	}
	persisted, err := m.loadChannelStates(ctx)
	if err != nil {
		return err
	}
	if len(persisted) != len(policy.Channels) {
		return authorization.ErrAuthorizationUnavailable
	}
	parents := make(map[int64]int64, len(policy.Channels))
	for _, access := range policy.Channels {
		parents[access.ChannelID] = access.ParentID
	}
	retained := make(map[int64]bool, len(policy.Channels))
	for _, ch := range persisted {
		if parent, exists := parents[ch.ChannelID]; !exists || ch.ParentID != parent {
			return fmt.Errorf("%w: channel %d parent mismatch", authorization.ErrAuthorizationUnavailable, ch.ChannelID)
		}
		retained[ch.ChannelID] = true
	}
	return m.reconcileRoleChannelState(ctx, persisted, retained, beforeRemove)
}

// Caller holds treeMu. Kept separate from loading so failure/retry ordering can
// be exercised without a database or a timer race.
func (m *ChannelManager) reconcileRoleChannelState(ctx context.Context, persisted []*state.Channel, retained map[int64]bool, beforeRemove func(DeleteResult) error) error {
	deleted := DeleteResult{}
	for _, ch := range m.state.ListChannels() {
		if retained[ch.ChannelID] {
			continue
		}
		deleted.ChannelIDs = append(deleted.ChannelIDs, ch.ChannelID)
		for _, member := range m.state.ChannelMembers(ch.ChannelID) {
			deleted.Members = append(deleted.Members, DeletedMember{ClientID: member.ClientID, ChannelID: ch.ChannelID})
		}
	}
	sort.Slice(deleted.ChannelIDs, func(i, j int) bool { return deleted.ChannelIDs[i] < deleted.ChannelIDs[j] })
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(deleted.ChannelIDs) > 0 {
		if err := beforeRemove(deleted); err != nil {
			return err
		}
	}
	// Everything below is in-memory and cannot fail. Do not check cancellation
	// between detaching media and clearing its corresponding membership.
	for _, ch := range persisted {
		m.mirrorPersistedChannel(ch)
	}
	m.state.RemoveChannels(deleted.ChannelIDs)
	for _, id := range deleted.ChannelIDs {
		m.cancelCleanupLocked(id)
	}
	for _, ch := range persisted {
		m.reconcileRoleCleanupWatcherLocked(ch.ChannelID)
	}
	return nil
}

// Unrelated permission changes must not restart the grace period of an empty
// leaf. Preserve its current token, while arming newly eligible parents after
// a child is removed and canceling watchers that are no longer eligible.
func (m *ChannelManager) reconcileRoleCleanupWatcherLocked(channelID int64) {
	ch, exists := m.state.GetChannel(channelID)
	if !exists || ch.ChannelType != int(ChannelTypeTemporary) || len(m.state.ChannelMembers(channelID)) != 0 || len(m.stateChannelSubtreeIDs(channelID)) != 1 {
		m.cancelCleanupLocked(channelID)
		return
	}
	m.mu.Lock()
	existing := m.timers[channelID]
	m.mu.Unlock()
	if existing == nil {
		m.startCleanupWatcherLocked(channelID)
	}
}

// Enter Authority before treeMu. A timer only proposes cleanup; its exact
// generation, occupancy, type and leaf status are rechecked inside the gate.
func (m *ChannelManager) cleanupRoleChannel(authority *authorization.Authority, channelID int64, expected *cleanupTimer) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p, err := authority.RolePolicy(ctx)
	if err == nil {
		_, err = authority.ChangeLifecyclePolicy(ctx, p.Revision, func(ctx context.Context) (authorization.RolePolicy, error) {
			m.treeMu.Lock()
			defer m.treeMu.Unlock()
			m.mu.Lock()
			active := m.timers[channelID]
			if m.cleanupStopped || expected == nil || active != expected || active.generation != expected.generation {
				m.mu.Unlock()
				return authorization.RolePolicy{}, authorization.ErrLifecycleUnchanged
			}
			delete(m.timers, channelID)
			m.mu.Unlock()
			ch, exists := m.state.GetChannel(channelID)
			if !exists || ch.ChannelType != int(ChannelTypeTemporary) || len(m.state.ChannelMembers(channelID)) != 0 || len(m.stateChannelSubtreeIDs(channelID)) != 1 {
				return authorization.RolePolicy{}, authorization.ErrLifecycleUnchanged
			}
			return m.store.PruneTemporaryRoleChannel(ctx, channelID, p.Revision)
		})
	}
	if err == nil || errors.Is(err, authorization.ErrLifecycleUnchanged) {
		return
	}
	m.logger.Warn("role channel cleanup pending", zap.Int64("channel_id", channelID), zap.Error(err))
	// A conflict or transient failure must not lose the cleanup opportunity.
	// Do not replace a newer timer installed by a concurrent lifecycle event.
	m.treeMu.Lock()
	defer m.treeMu.Unlock()
	m.mu.Lock()
	active := m.timers[channelID]
	retry := active == nil || active == expected
	m.mu.Unlock()
	if retry {
		m.reconcileCleanupWatcherLocked(channelID)
	}
}
