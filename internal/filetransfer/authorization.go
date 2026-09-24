package filetransfer

import (
	"context"
	"errors"
	"net"

	"noxa/internal/store"
)

var ErrAccessRevoked = errors.New("file transfer access revoked")

// Principal is supplied by the authenticated control connection, never the data
// port. Tokens carry a copy; request contexts and authorization leases do not.
type Principal struct {
	UserID    int64
	SessionID string
}

type principalKey struct{}

type mutationRightsKey struct{}
type mutationRights struct {
	scope        int64
	uploader     string
	manageOthers bool
}

// WithMutationRights carries the caller's current ownership rights through a
// synchronous operation. Derive it under the policy lease; never store it on a
// transfer token, since rights may change before the upload commits.
func WithMutationRights(ctx context.Context, scope int64, uploader string, manageOthers bool) context.Context {
	return context.WithValue(ctx, mutationRightsKey{}, mutationRights{scope, uploader, manageOthers})
}

// checkFileOwner runs while fileOpsMu excludes other file mutations. New names
// are permitted; replacing an existing row requires current ownership rights.
func (s *Server) checkFileOwner(ctx context.Context, scope int64, folder, name string) error {
	s.mu.Lock()
	guarded := s.accessGuard != nil
	s.mu.Unlock()
	if !guarded {
		return nil
	}
	rec, err := s.store.GetFile(ctx, scope, folder, name)
	if errors.Is(err, store.ErrFileNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	rights, ok := ctx.Value(mutationRightsKey{}).(mutationRights)
	if !ok || rights.scope != scope || (!rights.manageOthers && (rights.uploader == "" || rights.uploader != rec.Uploader)) {
		return ErrAccessRevoked
	}
	return nil
}

func WithPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, principal)
}

func transferPrincipal(ctx context.Context) *Principal {
	principal, ok := ctx.Value(principalKey{}).(Principal)
	if !ok {
		return nil
	}
	return &principal
}

// AccessGuard synchronously holds current authorization through one bounded
// effect. It must return an error when denied, and never retain the callback.
// Install before accepting traffic. A non-nil guard refuses unbound tokens.
type AccessGuard func(context.Context, Principal, int64, string, func(context.Context) error) error

func (s *Server) SetAccessGuard(guard AccessGuard) {
	s.mu.Lock()
	s.accessGuard = guard
	s.mu.Unlock()
	s.links.mu.Lock()
	s.links.accessGuard = guard
	s.links.mu.Unlock()
}

func (s *Server) withTransferAccess(ctx context.Context, tr *transfer, effect func(context.Context) error) error {
	if tr.revoked.Load() {
		return ErrAccessRevoked
	}
	s.mu.Lock()
	guard := s.accessGuard
	s.mu.Unlock()
	if guard == nil {
		return effect(ctx)
	}
	if tr.Principal == nil {
		return ErrAccessRevoked
	}
	return guard(ctx, *tr.Principal, tr.ChannelID, tr.Direction, func(ctx context.Context) error {
		if tr.revoked.Load() {
			return ErrAccessRevoked
		}
		return effect(ctx)
	})
}

// beginTransfer keeps token consumption and active registration within the same
// policy lease. Revocation cannot miss a token between the two registries.
func (s *Server) beginTransfer(ctx context.Context, token, id string, conn net.Conn) (*transfer, *activeTransfer, error) {
	s.mu.Lock()
	tr := s.transfers[tokenDigest(token)]
	s.mu.Unlock()
	if tr == nil {
		return nil, nil, errors.New("invalid transfer token")
	}
	var active *activeTransfer
	err := s.withTransferAccess(ctx, tr, func(context.Context) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		consumed, err := s.consumeLocked(token, id)
		if err != nil {
			return err
		}
		active, err = s.activateTransferLocked(consumed, conn)
		return err
	})
	return tr, active, err
}

// RevokeTransfers is called under the policy writer's exclusive gate. The
// predicate must use its supplied immutable policy, never reacquire the guard.
// Closing active sockets interrupts idle uploads as well as ongoing downloads.
func (s *Server) RevokeTransfers(allowed func(Principal, int64, string) bool) {
	// Disconnect revocation also uses this method, without a policy writer.
	// Serialize it with commits so none can finish after revocation returns.
	s.fileOpsMu.Lock()
	s.mu.Lock()
	for digest, tr := range s.transfers {
		if tr.Principal == nil || !allowed(*tr.Principal, tr.ChannelID, tr.Direction) {
			tr.revoked.Store(true)
			delete(s.transfers, digest)
		}
	}
	var connections []net.Conn
	for _, active := range s.activeTransfers {
		tr := active.authorization
		if tr == nil || tr.Principal == nil || !allowed(*tr.Principal, tr.ChannelID, tr.Direction) {
			if tr != nil {
				tr.revoked.Store(true)
			}
			connections = append(connections, active.conn)
		}
	}
	s.mu.Unlock()
	s.fileOpsMu.Unlock()
	for _, conn := range connections {
		_ = conn.Close()
	}
	s.links.revokeAccess(allowed)
}
