package channels

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"go.uber.org/zap"

	"noxa/internal/authorization"
	"noxa/internal/state"
	"noxa/internal/store"
)

// DefaultCleanupDelay is the grace period after a temporary channel becomes
// empty before it is automatically deleted. It can be overridden per-manager
// via the CleanupDelay field (e.g. set low in tests).
const DefaultCleanupDelay = 60 * time.Second

// ErrChannelNotFound is returned when an operation references an unknown
// channel.
var ErrChannelNotFound = errors.New("channel not found")

// ErrInvalidSpec is returned when a ChannelSpec fails validation.
var ErrInvalidSpec = errors.New("invalid channel spec")

// DeleteResult describes every client-visible consequence of a cascaded
// channel deletion. SubscriberIDs identifies clients whose explicit channel
// subscription snapshot must be refreshed.
type DeleteResult struct {
	RootID        int64
	ChannelIDs    []int64
	SubscriberIDs []string
	Members       []DeletedMember
}

// DeletedMember identifies a client whose voice/control membership was reset
// because its channel was part of a deleted subtree.
type DeletedMember struct {
	ClientID  string
	ChannelID int64
}

// cleanupTimer wraps a *time.Timer used to schedule the deletion of an empty
// temporary channel. The timer is stored so it can be cancelled (e.g. when a
// client joins the channel before the grace period elapses).
type cleanupTimer struct {
	timer      *time.Timer
	generation uint64
}

// keyedMutexPool provides bounded-lifetime locks for individual channel and
// client identities. Entries include waiters in their reference count, so an
// entry is removed only after no goroutine can still acquire its mutex.
type keyedMutexPool[K comparable] struct {
	mu      sync.Mutex
	entries map[K]*keyedMutexEntry
}

type keyedMutexEntry struct {
	mu   sync.Mutex
	refs int
}

func (p *keyedMutexPool[K]) lock(key K) func() {
	p.mu.Lock()
	if p.entries == nil {
		p.entries = make(map[K]*keyedMutexEntry)
	}
	entry := p.entries[key]
	if entry == nil {
		entry = new(keyedMutexEntry)
		p.entries[key] = entry
	}
	entry.refs++
	p.mu.Unlock()

	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		p.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(p.entries, key)
		}
		p.mu.Unlock()
	}
}

type channelManagerHooks struct {
	beforeLeaveClient   func(string)
	beforeRemoveClient  func(string)
}

// ChannelManager coordinates the lifecycle of channels across the database
// (store.Store) and the in-memory state (state.Manager). It owns the automatic
// cleanup of temporary channels: when a temporary channel becomes empty a
// timer is started; if it remains empty for the cleanup delay the role
// lifecycle prunes it.
type ChannelManager struct {
	publicPoolWake chan struct{}
	store          *store.Store
	state          *state.Manager
	logger         *zap.Logger

	mu                    sync.Mutex
	timers                map[int64]*cleanupTimer
	nextCleanupGeneration uint64
	cleanupStopped        bool

	// treeMu is shared by every supported channel lifecycle operation. Normal
	// per-channel work takes a read lock; role channel changes and reconciliation
	// take the write lock.
	treeMu        sync.RWMutex
	roleAuthority *authorization.Authority // protected by treeMu; cleanup only, never acquired with treeMu held
	channelLocks  keyedMutexPool[int64]
	clientLocks   keyedMutexPool[string]
	testHooks     channelManagerHooks

	// CleanupDelay is the grace period after a temporary channel becomes empty
	// before it is deleted. Defaults to DefaultCleanupDelay when zero. It is
	// exported so tests can set a short delay.
	CleanupDelay time.Duration
}

// New constructs a ChannelManager wired to the given store, state manager,
// and logger. The cleanup delay defaults to DefaultCleanupDelay.
func New(s *store.Store, sm *state.Manager, logger *zap.Logger) *ChannelManager {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &ChannelManager{
		store:          s,
		publicPoolWake: make(chan struct{}, 1),
		state:          sm,
		logger:         logger,
		timers:         make(map[int64]*cleanupTimer),
		CleanupDelay:   DefaultCleanupDelay,
	}
}

// cleanupDelayLocked returns the configured cleanup delay, defaulting to
// DefaultCleanupDelay when unset. The caller must hold m.mu.
func (m *ChannelManager) cleanupDelayLocked() time.Duration {
	if m.CleanupDelay <= 0 {
		return DefaultCleanupDelay
	}
	return m.CleanupDelay
}

// lockChannels acquires channel lifecycle locks in ascending ID order. A
// client move needs both its source and target locks; sorting and de-duplicating
// here gives every multi-channel operation the same deadlock-free order.
func (m *ChannelManager) lockChannels(channelIDs ...int64) func() {
	ids := make([]int64, 0, len(channelIDs))
	seen := make(map[int64]struct{}, len(channelIDs))
	for _, channelID := range channelIDs {
		if channelID <= 0 {
			continue
		}
		if _, ok := seen[channelID]; ok {
			continue
		}
		seen[channelID] = struct{}{}
		ids = append(ids, channelID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	unlocks := make([]func(), 0, len(ids))
	for _, channelID := range ids {
		unlocks = append(unlocks, m.channelLocks.lock(channelID))
	}
	return func() {
		for i := len(unlocks) - 1; i >= 0; i-- {
			unlocks[i]()
		}
	}
}

// SetCleanupDelay sets the grace period an empty temporary channel survives
// before deletion (165), so a brief gap between the last leave and the next
// join does not destroy the channel. A non-positive value restores the
// default. Timers already scheduled keep their old delay.
func (m *ChannelManager) SetCleanupDelay(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d <= 0 {
		d = DefaultCleanupDelay
	}
	m.CleanupDelay = d
}

// LoadIntoState reads all channels from the database and registers them in
// the state manager, returning the number of channels loaded. It is intended
// to be called once at startup before the server accepts clients, so the
// in-memory channel tree reflects the persisted one. Temporary channels are
// empty by definition at startup (no clients are connected yet), so each gets
// a cleanup watcher — matching the semantics of a temp channel that just
// became empty.
func (m *ChannelManager) LoadIntoState(ctx context.Context) (int, error) {
	m.treeMu.Lock()
	defer m.treeMu.Unlock()

	const q = `SELECT id, COALESCE(parent_id, 0), name, COALESCE(topic, ''),
	          order_index, channel_type, COALESCE(max_clients, 0), created_at,
	          COALESCE(password_hash, ''),
	          opus_bitrate, opus_fec, opus_dtx, opus_stereo, slow_mode_seconds,
	          COALESCE(description, ''), text_only, system_kind
	          FROM channels ORDER BY id`
	rows, err := m.store.DB().QueryContext(ctx, q)
	if err != nil {
		return 0, fmt.Errorf("querying channels: %w", err)
	}
	defer func() { _ = rows.Close() }()

	count := 0
	var temporaryIDs []int64
	for rows.Next() {
		var ch state.Channel
		var channelType int16
		if err := rows.Scan(&ch.ChannelID, &ch.ParentID, &ch.Name, &ch.Topic,
			&ch.OrderIndex, &channelType, &ch.MaxClients, &ch.CreatedAt,
			&ch.PasswordHash,
			&ch.OpusBitrate, &ch.OpusFEC, &ch.OpusDTX, &ch.OpusStereo, &ch.SlowModeSeconds,
			&ch.Description, &ch.TextOnly, &ch.SystemKind); err != nil {
			return count, fmt.Errorf("scanning channel row: %w", err)
		}
		parsedType, err := ParseChannelType(int(channelType))
		if err != nil {
			return count, fmt.Errorf("channel %d has invalid stored type %d: %w", ch.ChannelID, channelType, err)
		}
		ch.ChannelType = int(parsedType)
		m.state.AddChannel(&ch)
		count++

		if parsedType == ChannelTypeTemporary {
			temporaryIDs = append(temporaryIDs, ch.ChannelID)
		}
	}
	if err := rows.Err(); err != nil {
		return count, fmt.Errorf("iterating channel rows: %w", err)
	}
	// The full tree must be present before deciding whether a temporary channel
	// is an empty leaf. Starting timers while rows stream could schedule a
	// parent before its persisted descendants are loaded.
	for _, channelID := range temporaryIDs {
		unlock := m.lockChannels(channelID)
		m.reconcileCleanupWatcherLocked(channelID)
		unlock()
	}

	m.logger.Info("channels loaded into state", zap.Int("count", count))
	return count, nil
}

// MoveClient atomically moves a client in state and updates cleanup ownership
// for both the source and target channels. Holding the target lifecycle lock
// across the state mutation and timer cancellation makes a successful join
// linearizable with temporary-channel deletion: either cleanup deletes first
// and the move fails, or the move cancels cleanup before deletion can claim.
func (m *ChannelManager) MoveClient(clientID string, targetChannelID int64) (int64, error) {
	return m.MoveClientWithLifecycle(clientID, targetChannelID, nil)
}

// MoveClientWithLifecycle moves a client and invokes afterMove before
// releasing the client, channel, and tree lifecycle locks. The callback is
// for non-reentrant external consequences (voice membership, key delivery,
// and event publication) that must be ordered before a competing subtree
// deletion. It must not call back into ChannelManager.
func (m *ChannelManager) MoveClientWithLifecycle(clientID string, targetChannelID int64, afterMove func(oldChannelID int64)) (int64, error) {
	return m.moveClientWithLifecycle(clientID, targetChannelID, afterMove, false)
}

// MoveClientWithinCapacity preserves lifecycle ordering while enforcing the
// channel's resource limit for every role, including the owner.
func (m *ChannelManager) MoveClientWithinCapacity(clientID string, targetChannelID int64, afterMove func(int64)) (int64, error) {
	return m.moveClientWithLifecycle(clientID, targetChannelID, afterMove, true)
}

func (m *ChannelManager) moveClientWithLifecycle(clientID string, targetChannelID int64, afterMove func(int64), enforceCapacity bool) (int64, error) {
	defer m.requestPublicPool()
	m.treeMu.RLock()
	defer m.treeMu.RUnlock()
	unlockClient := m.clientLocks.lock(clientID)
	defer unlockClient()

	oldChannelID, _, ok := m.state.ClientChannelState(clientID)
	if !ok {
		return 0, state.ErrClientNotFound
	}
	unlockChannels := m.lockChannels(oldChannelID, targetChannelID)
	defer unlockChannels()

	move := m.state.MoveClient
	if enforceCapacity {
		move = m.state.MoveClientWithinCapacity
	}
	if err := move(clientID, targetChannelID); err != nil {
		return oldChannelID, err
	}
	m.cancelCleanupLocked(targetChannelID)
	if oldChannelID != 0 && oldChannelID != targetChannelID {
		m.reconcileCleanupWatcherLocked(oldChannelID)
	}
	if afterMove != nil {
		afterMove(oldChannelID)
	}
	return oldChannelID, nil
}

// LeaveClient atomically removes a client from its current channel and
// reconciles temporary-channel cleanup under the same lifecycle locks used by
// MoveClient.
func (m *ChannelManager) LeaveClient(clientID string) (int64, error) {
	defer m.requestPublicPool()
	m.treeMu.RLock()
	defer m.treeMu.RUnlock()
	unlockClient := m.clientLocks.lock(clientID)
	defer unlockClient()

	oldChannelID, _, ok := m.state.ClientChannelState(clientID)
	if !ok {
		return 0, state.ErrClientNotFound
	}
	if oldChannelID == 0 {
		return 0, state.ErrNotInChannel
	}
	unlockChannel := m.lockChannels(oldChannelID)
	defer unlockChannel()
	if hook := m.testHooks.beforeLeaveClient; hook != nil {
		hook(clientID)
	}
	if err := m.state.LeaveChannel(clientID); err != nil {
		return oldChannelID, err
	}
	m.reconcileCleanupWatcherLocked(oldChannelID)
	return oldChannelID, nil
}

// RemoveClient atomically removes a disconnected client from state and
// reconciles the actual channel it occupied at removal time.
func (m *ChannelManager) RemoveClient(clientID string) (*state.Client, error) {
	defer m.requestPublicPool()
	m.treeMu.RLock()
	defer m.treeMu.RUnlock()
	unlockClient := m.clientLocks.lock(clientID)
	defer unlockClient()

	snapshot, ok := m.state.GetClient(clientID)
	if !ok {
		return nil, state.ErrClientNotFound
	}
	unlockChannel := m.lockChannels(snapshot.ChannelID)
	defer unlockChannel()
	if hook := m.testHooks.beforeRemoveClient; hook != nil {
		hook(clientID)
	}
	removed, ok := m.state.RemoveClient(clientID)
	if !ok {
		return nil, state.ErrClientNotFound
	}
	if removed.ChannelID != 0 {
		m.reconcileCleanupWatcherLocked(removed.ChannelID)
	}
	return removed, nil
}

// WithChannelLifecycle runs a bounded channel-specific operation while
// deletion and same-channel lifecycle mutation are excluded. Asset handlers
// use this to keep filesystem writes/reads linearizable with subtree removal.
// The callback must not re-enter ChannelManager.
func (m *ChannelManager) WithChannelLifecycle(channelID int64, operation func() error) error {
	if operation == nil {
		return fmt.Errorf("%w: channel lifecycle operation is nil", ErrInvalidSpec)
	}
	m.treeMu.RLock()
	defer m.treeMu.RUnlock()
	unlock := m.lockChannels(channelID)
	defer unlock()
	if _, ok := m.state.GetChannel(channelID); !ok {
		return ErrChannelNotFound
	}
	return operation()
}

// reconcileCleanupWatcherLocked mirrors the current state into timer
// ownership. The caller must hold the channel lifecycle lock.
func (m *ChannelManager) reconcileCleanupWatcherLocked(channelID int64) {
	ch, ok := m.state.GetChannel(channelID)
	if ok && ch.ChannelType == int(ChannelTypeTemporary) &&
		len(m.state.ChannelMembers(channelID)) == 0 &&
		len(m.stateChannelSubtreeIDs(channelID)) == 1 {
		m.startCleanupWatcherLocked(channelID)
		return
	}
	m.cancelCleanupLocked(channelID)
}

// StartCleanupWatcher schedules a cleanup timer for the given temporary
// channel. When the timer fires, the manager re-checks (under the lock) that
// the channel is still temporary and still empty, and only then deletes it.
// If a timer is already running for the channel it is cancelled and replaced.
func (m *ChannelManager) StartCleanupWatcher(channelID int64) {
	m.treeMu.RLock()
	defer m.treeMu.RUnlock()
	unlock := m.lockChannels(channelID)
	defer unlock()
	m.reconcileCleanupWatcherLocked(channelID)
}

// startCleanupWatcherLocked replaces the active token for channelID. The
// caller must hold the channel lifecycle lock.
func (m *ChannelManager) startCleanupWatcherLocked(channelID int64) {
	if m.roleAuthority == nil {
		return // Cleanup needs the authority; it is installed before serving.
	}
	m.mu.Lock()
	if m.cleanupStopped {
		m.mu.Unlock()
		return
	}

	// Cancel any existing timer for this channel.
	if existing, ok := m.timers[channelID]; ok {
		existing.timer.Stop()
	}

	delay := m.cleanupDelayLocked()
	m.nextCleanupGeneration++
	entry := &cleanupTimer{generation: m.nextCleanupGeneration}
	m.timers[channelID] = entry
	entry.timer = time.AfterFunc(delay, func() {
		m.cleanupCallback(channelID, entry)
	})
	m.mu.Unlock()

	m.logger.Debug("cleanup watcher scheduled",
		zap.Int64("channel_id", channelID),
		zap.Duration("delay", delay),
		zap.Uint64("generation", entry.generation),
	)
}

// cancelCleanupLocked invalidates the exact active timer token. The caller
// must hold the channel lifecycle lock.
func (m *ChannelManager) cancelCleanupLocked(channelID int64) {
	m.mu.Lock()
	if existing, ok := m.timers[channelID]; ok {
		existing.timer.Stop()
		delete(m.timers, channelID)
		m.mu.Unlock()
		m.logger.Debug("cleanup watcher cancelled", zap.Int64("channel_id", channelID))
		return
	}
	m.mu.Unlock()
}

// cleanupCallback is invoked by a cleanup timer when the grace period elapses.
// The role lifecycle rechecks the timer token, occupancy and leaf status.
func (m *ChannelManager) cleanupCallback(channelID int64, expected *cleanupTimer) {
	m.treeMu.RLock()
	authority := m.roleAuthority
	m.treeMu.RUnlock()
	if authority != nil {
		m.cleanupRoleChannel(authority, channelID, expected)
	}
}

// Close permanently stops cleanup and cancels pending timers. In-flight
// callbacks cannot rearm watchers after server shutdown.
func (m *ChannelManager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupStopped = true
	m.cancelAllCleanupLocked()
}

func (m *ChannelManager) cancelAllCleanupLocked() {
	for id, t := range m.timers {
		t.timer.Stop()
		delete(m.timers, id)
	}
}

// CleanupTimersCount returns the number of currently pending cleanup timers,
// for observability.
func (m *ChannelManager) CleanupTimersCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.timers)
}

func (m *ChannelManager) stateChannelSubtreeIDs(channelID int64) []int64 {
	children := make(map[int64][]int64)
	present := make(map[int64]struct{})
	for _, channel := range m.state.ListChannels() {
		present[channel.ChannelID] = struct{}{}
		children[channel.ParentID] = append(children[channel.ParentID], channel.ChannelID)
	}
	ids := make([]int64, 0, 1)
	queue := []int64{channelID}
	seen := make(map[int64]struct{})
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		if _, ok := present[id]; ok {
			ids = append(ids, id)
		}
		queue = append(queue, children[id]...)
	}
	return ids
}

func (m *ChannelManager) mirrorPersistedChannel(channel *state.Channel) {
	channelType := channel.ChannelType
	hasPassword := channel.PasswordHash != ""
	if !m.state.UpdateChannel(channel.ChannelID, state.ChannelUpdate{
		ParentID:        &channel.ParentID,
		Name:            &channel.Name,
		Topic:           &channel.Topic,
		OrderIndex:      &channel.OrderIndex,
		ChannelType:     &channelType,
		MaxClients:      &channel.MaxClients,
		PasswordHash:    &channel.PasswordHash,
		HasPassword:     &hasPassword,
		OpusBitrate:     &channel.OpusBitrate,
		OpusFEC:         &channel.OpusFEC,
		OpusDTX:         &channel.OpusDTX,
		OpusStereo:      &channel.OpusStereo,
		SlowModeSeconds: &channel.SlowModeSeconds,
		Description:     &channel.Description,
		TextOnly:        &channel.TextOnly,
		SystemKind:      &channel.SystemKind,
	}) {
		m.state.AddChannel(channel)
	}
}
