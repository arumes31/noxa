package authorization

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
)

var ErrAuthorizationUnavailable = errors.New("authorization unavailable")

// ErrLifecycleUnchanged is returned only by trusted maintenance callbacks
// that revalidated a timer and found no work. It never acknowledges a commit.
var ErrLifecycleUnchanged = errors.New("channel lifecycle unchanged")

// ErrEnforcementPending means the database committed the returned policy, but
// live revocation did not finish. Callers must acknowledge that revision rather
// than retry the mutation. Protected operations remain closed until Reload.
var ErrEnforcementPending = fmt.Errorf("policy saved; enforcement pending: %w", ErrAuthorizationUnavailable)

type PolicyBackend interface {
	RolePolicy(context.Context) (RolePolicy, error)
	ChangeRolePolicy(context.Context, int64, RoleChange) (RolePolicy, error)
}

// ReconcilePolicy revokes sessions/transfers and rotates keys using the supplied
// snapshots. It runs after database commit, before new protected operations.
// It must not re-enter Authority. Returning an error closes the authority until
// an explicit successful Reload; even the owner cannot bypass that failure.
type ReconcilePolicy func(context.Context, *RoleEvaluator, *RoleEvaluator) error

// Authority serializes policy changes against bounded protected operations.
// This gate is an authorization barrier, deliberately covering the protected
// effect (e.g. encrypting/relaying one message or transferring one file chunk).
// Do not hold a lease for an entire stream, session or unbounded network wait.
// All writers in a serving process must use this authority; offline maintenance
// requires stopping that process. No background refresh can publish stale state.
type Authority struct {
	gate      sync.RWMutex
	backend   PolicyBackend
	reconcile ReconcilePolicy
	policy    RolePolicy
	// revision includes a known commit even if reconciliation has not finished.
	// policy remains the last published snapshot for conservative revocation.
	revision  int64
	evaluator *RoleEvaluator
}

func NewAuthority(ctx context.Context, backend PolicyBackend, reconcile ReconcilePolicy) (*Authority, error) {
	if backend == nil || reconcile == nil {
		return nil, ErrAuthorizationUnavailable
	}
	p, err := backend.RolePolicy(ctx)
	if err != nil {
		return nil, err
	}
	e, err := NewRoleEvaluator(p)
	if err != nil {
		return nil, err
	}
	return &Authority{backend: backend, reconcile: reconcile, policy: cloneRolePolicy(p), revision: p.Revision, evaluator: e}, nil
}

// WithAccess checks a concrete scope and holds its revision through the effect.
// The callback can perform further checks on the same immutable evaluator (for
// example target hierarchy or a second channel), without reacquiring the gate.
// Neither the callback nor reconciliation may invoke Authority recursively.
func (a *Authority) WithAccess(ctx context.Context, actorID, channelID int64, capability Capability, effect func(*RoleEvaluator) error) error {
	return a.WithPolicy(ctx, func(e *RoleEvaluator) error {
		if !e.Evaluate(actorID, channelID, capability).Allowed {
			return ErrRoleForbidden
		}
		return effect(e)
	})
}

// WithPolicy pins a revision for internal fan-out, snapshots and maintenance.
// The callback must authorize each protected recipient/action with its evaluator.
// It has the same bounded-effect and non-reentrancy contract as WithAccess.
func (a *Authority) WithPolicy(ctx context.Context, effect func(*RoleEvaluator) error) error {
	a.gate.RLock()
	defer a.gate.RUnlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.evaluator == nil {
		return ErrAuthorizationUnavailable
	}
	return effect(a.evaluator)
}

// TryWithPolicy pins the current policy for a disposable realtime packet.
// A packet must not wait for reconciliation, which may be joining its worker.
// Contention drops the packet; it never bypasses the authorization check.
func (a *Authority) TryWithPolicy(effect func(*RoleEvaluator) error) error {
	if !a.gate.TryRLock() {
		return ErrAuthorizationUnavailable
	}
	defer a.gate.RUnlock()
	if a.evaluator == nil {
		return ErrAuthorizationUnavailable
	}
	return effect(a.evaluator)
}

// WithExclusivePolicy quiesces protected effects for trusted runtime session
// revocation without changing policy or its revision. The callback must check
// the actor's authority and must not re-enter Authority. Errors do not imply a
// policy commit and leave the published evaluator unchanged.
func (a *Authority) WithExclusivePolicy(ctx context.Context, effect func(*RoleEvaluator) error) error {
	if effect == nil {
		return ErrRoleInvalid
	}
	a.gate.Lock()
	defer a.gate.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.evaluator == nil {
		return ErrAuthorizationUnavailable
	}
	return effect(a.evaluator)
}

func (a *Authority) RolePolicy(ctx context.Context) (RolePolicy, error) {
	a.gate.RLock()
	defer a.gate.RUnlock()
	if err := ctx.Err(); err != nil {
		return RolePolicy{}, err
	}
	if a.evaluator == nil {
		return RolePolicy{}, ErrAuthorizationUnavailable
	}
	return cloneRolePolicy(a.policy), nil
}

func (a *Authority) ChangeRolePolicy(ctx context.Context, actorID int64, change RoleChange) (RolePolicy, error) {
	return a.ChangeRolePolicyValidated(ctx, actorID, change, nil)
}

// ChangeRolePolicyValidated rechecks a transport's admission under the exclusive
// policy barrier before invoking the normal transactional role mutation. The
// trusted validation callback must not mutate state or re-enter Authority; its
// error means no commit was attempted and leaves policy availability unchanged.
// It must release any additional locks before returning, for reconciliation.
func (a *Authority) ChangeRolePolicyValidated(ctx context.Context, actorID int64, change RoleChange, validate func(context.Context) error) (RolePolicy, error) {
	return a.changeLifecyclePolicy(ctx, change.ExpectedRevision, validate, func(ctx context.Context) (RolePolicy, error) {
		policy, err := a.backend.ChangeRolePolicy(ctx, actorID, change)
		if err != nil {
			// Ordinary storage may return its candidate alongside an ambiguous
			// commit error. It does not promise that candidate is durable.
			return RolePolicy{}, err
		}
		return policy, nil
	})
}

// ChangeLifecyclePolicy lets an internal lifecycle adapter commit channel
// resources and policy under the same exclusive gate as ordinary role changes.
// commit must authorize the authenticated actor in its storage transaction.
// It may acquire lifecycle locks, but must release them before returning:
// reconciliation can call the channel manager again. This callback is trusted
// server code and must never be supplied by a client or invoke Authority.
//
// If state mirroring fails after a known commit, return that committed policy
// with the error. Its revision remains acknowledged while access stays closed;
// Reload must reconcile both persisted resources and policy before reopening.
func (a *Authority) ChangeLifecyclePolicy(ctx context.Context, expectedRevision int64, commit func(context.Context) (RolePolicy, error)) (RolePolicy, error) {
	return a.changeLifecyclePolicy(ctx, expectedRevision, nil, commit)
}

// ChangeLifecyclePolicyValidated applies the admission preflight used by
// ChangeRolePolicyValidated to a resource lifecycle transaction. Validation
// runs under the writer barrier and must release other locks before returning.
func (a *Authority) ChangeLifecyclePolicyValidated(ctx context.Context, expectedRevision int64, validate func(context.Context) error, commit func(context.Context) (RolePolicy, error)) (RolePolicy, error) {
	return a.changeLifecyclePolicy(ctx, expectedRevision, validate, commit)
}

func (a *Authority) changeLifecyclePolicy(ctx context.Context, expectedRevision int64, validate func(context.Context) error, commit func(context.Context) (RolePolicy, error)) (RolePolicy, error) {
	if commit == nil {
		return RolePolicy{}, ErrRoleInvalid
	}
	a.gate.Lock()
	defer a.gate.Unlock()
	if err := ctx.Err(); err != nil {
		return RolePolicy{}, err
	}
	if a.evaluator == nil {
		return RolePolicy{}, ErrAuthorizationUnavailable
	}
	if validate != nil {
		if err := validate(ctx); err != nil {
			return RolePolicy{}, err
		}
	}
	if expectedRevision != a.policy.Revision {
		return RolePolicy{}, ErrRoleConflict
	}
	committed, err := commit(ctx)
	if err != nil {
		if errors.Is(err, ErrLifecycleUnchanged) && committed.Revision == 0 {
			return RolePolicy{}, err
		}
		if committed.Revision > a.revision {
			a.revision = committed.Revision
			a.evaluator = nil
			return cloneRolePolicy(committed), errors.Join(ErrEnforcementPending, err)
		}
		if !errors.Is(err, ErrRoleForbidden) && !errors.Is(err, ErrRoleInvalid) {
			// A commit error can be ambiguous, and an external revision conflict
			// means our snapshot is stale. Never keep serving its old grants.
			a.evaluator = nil
		}
		return RolePolicy{}, err
	}
	if committed.Revision <= a.revision {
		a.evaluator = nil
		return RolePolicy{}, ErrAuthorizationUnavailable
	}
	a.revision = committed.Revision
	if err := a.publish(ctx, committed); err != nil {
		return cloneRolePolicy(committed), errors.Join(ErrEnforcementPending, err)
	}
	return cloneRolePolicy(a.policy), nil
}

// Reload is explicit recovery after a failed/ambiguous commit or reconciliation.
// It repeats reconciliation before re-opening protected operations.
func (a *Authority) Reload(ctx context.Context) error {
	return a.reload(ctx, true, 0, nil)
}

// RefreshIfChanged imports an offline registration before admitting an account.
// A single default-role assignment can skip reconciliation only when the
// caller proves the admitted user has no live native session and no capability
// is revoked. The callback runs under the exclusive policy gate and must not
// re-enter Authority.
func (a *Authority) RefreshIfChanged(ctx context.Context, admittedUserID int64, noLiveSession func(int64) bool) error {
	return a.reload(ctx, false, admittedUserID, noLiveSession)
}

func (a *Authority) reload(ctx context.Context, force bool, admittedUserID int64, noLiveSession func(int64) bool) error {
	a.gate.Lock()
	defer a.gate.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	p, err := a.backend.RolePolicy(ctx)
	if err != nil {
		a.evaluator = nil
		return err
	}
	if p.Revision < a.revision {
		a.evaluator = nil
		return ErrAuthorizationUnavailable
	}
	if !force && a.evaluator != nil && reflect.DeepEqual(p, a.policy) {
		return nil
	}
	if !force && a.evaluator != nil && admittedUserID > 0 && noLiveSession != nil &&
		defaultRoleRegistrationOnly(a.policy, p, admittedUserID) && noLiveSession(admittedUserID) {
		next, err := NewRoleEvaluator(p)
		if err != nil {
			a.evaluator = nil
			return fmt.Errorf("%w: %v", ErrAuthorizationUnavailable, err)
		}
		if !revokesMemberAccess(a.evaluator, next, admittedUserID) {
			a.policy = cloneRolePolicy(p)
			a.evaluator = next
			return nil
		}
	}
	a.revision = p.Revision
	return a.publish(ctx, p)
}

// A role override can turn an @everyone grant into a deny, even when the only
// policy delta is a new default-role membership. Check all capabilities and
// scopes so an offline transfer or recording cannot retain revoked access.
func revokesMemberAccess(before, after *RoleEvaluator, userID int64) bool {
	scopes := make([]int64, 0, len(after.policy.Channels)+1)
	scopes = append(scopes, 0)
	for _, channel := range after.policy.Channels {
		scopes = append(scopes, channel.ChannelID)
	}
	capabilities := Capabilities()
	for _, scope := range scopes {
		for _, info := range capabilities {
			if before.Evaluate(userID, scope, info.Key).Allowed && !after.Evaluate(userID, scope, info.Key).Allowed {
				return true
			}
		}
	}
	return false
}

func defaultRoleRegistrationOnly(before, after RolePolicy, userID int64) bool {
	if before.Revision != after.Revision || before.OwnerID != after.OwnerID ||
		before.EveryoneID != after.EveryoneID || before.DefaultMemberRoleID <= 0 ||
		before.DefaultMemberRoleID != after.DefaultMemberRoleID ||
		!reflect.DeepEqual(before.Roles, after.Roles) || !reflect.DeepEqual(before.Channels, after.Channels) ||
		len(after.Members) != len(before.Members)+1 {
		return false
	}
	old := make(map[int64]RoleMember, len(before.Members))
	for _, member := range before.Members {
		old[member.UserID] = member
	}
	added := false
	for _, member := range after.Members {
		if previous, ok := old[member.UserID]; ok {
			if !reflect.DeepEqual(previous, member) {
				return false
			}
			delete(old, member.UserID)
			continue
		}
		if added || member.UserID != userID || len(member.RoleIDs) != 1 || member.RoleIDs[0] != after.DefaultMemberRoleID {
			return false
		}
		added = true
	}
	return added && len(old) == 0
}

func (a *Authority) publish(ctx context.Context, p RolePolicy) error {
	previous, err := NewRoleEvaluator(a.policy)
	a.evaluator = nil
	if err != nil {
		return fmt.Errorf("%w: %v", ErrAuthorizationUnavailable, err)
	}
	next, err := NewRoleEvaluator(p)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrAuthorizationUnavailable, err)
	}
	if err := a.reconcile(ctx, previous, next); err != nil {
		return fmt.Errorf("%w: %v", ErrAuthorizationUnavailable, err)
	}
	a.policy = cloneRolePolicy(p)
	a.evaluator = next
	return nil
}
