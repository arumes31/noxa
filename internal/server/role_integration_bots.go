package server

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

// bulkChatStore deletes several messages of one channel in one transaction.
// authorize sees the authors of the live messages found (id -> unique ID)
// before anything changes; an error from it aborts the whole delete.
type bulkChatStore interface {
	BulkDeleteChatMessages(ctx context.Context, channelID int64, ids []int64, authorize func(map[int64]string) error) ([]int64, error)
}

// integrationScopeStore reads and writes integration scope ceilings (807).
type integrationScopeStore interface {
	GetIntegrationScopes(context.Context, string) (auth.IntegrationScopeState, error)
	SetIntegrationScopes(context.Context, string, bool, []string) (auth.IntegrationScopeState, error)
}

// BulkDeleteIntegrationMessages tombstones up to 100 messages of one channel
// (838). Deleting another member's message needs ManageMessages in that
// channel and own messages need ViewChannel, exactly like a single delete;
// one message the caller may not delete rejects the whole request. Clients
// get the usual chat_deleted event for every deleted message.
func (s *TCPServer) BulkDeleteIntegrationMessages(ctx context.Context, principal auth.IntegrationPrincipal, request netproto.MessageBulkDelete) (netproto.MessageBulkDeleteResult, error) {
	if !request.Valid() {
		return netproto.MessageBulkDeleteResult{}, authorization.ErrRoleInvalid
	}
	result := netproto.MessageBulkDeleteResult{Deleted: []int64{}, NotFound: []int64{}}
	err := s.withIntegrationPolicy(ctx, principal, func(ctx context.Context, e *authorization.RoleEvaluator) error {
		if !e.Evaluate(principal.UserID(), request.ChannelID, authorization.ViewChannel).Allowed {
			return authorization.ErrRoleForbidden
		}
		chat, ok := s.chatStore().(bulkChatStore)
		if !ok {
			return authorization.ErrAuthorizationUnavailable
		}
		othersDeleted := 0
		deleted, err := chat.BulkDeleteChatMessages(ctx, request.ChannelID, request.MessageIDs, func(authors map[int64]string) error {
			othersDeleted = 0
			for _, author := range authors {
				if author != principal.UniqueID() {
					othersDeleted++
				}
			}
			if othersDeleted > 0 && !e.Evaluate(principal.UserID(), request.ChannelID, authorization.ManageMessages).Allowed {
				return authorization.ErrRoleForbidden
			}
			return nil
		})
		if err != nil {
			return err
		}
		for _, id := range request.MessageIDs {
			if slices.Contains(deleted, id) {
				result.Deleted = append(result.Deleted, id)
			} else {
				result.NotFound = append(result.NotFound, id)
			}
		}
		if othersDeleted > 0 {
			auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			ids := make([]string, len(result.Deleted))
			for i, id := range result.Deleted {
				ids[i] = strconv.FormatInt(id, 10)
			}
			s.auditInChannels(auditCtx, principal.UniqueID(), "message_bulk_delete", strconv.FormatInt(request.ChannelID, 10),
				fmt.Sprintf("count=%d ids=%s", len(result.Deleted), strings.Join(ids, ",")), request.ChannelID)
		}
		for _, id := range result.Deleted {
			s.broadcastScope(ctx, request.ChannelID, eventChatDeleted, map[string]any{
				"message_id": id,
				"channel_id": request.ChannelID,
				"deleted_by": principal.UniqueID(),
			})
		}
		return nil
	})
	if err != nil {
		return netproto.MessageBulkDeleteResult{}, err
	}
	return result, nil
}

func (s *TCPServer) chatStore() ChatStore {
	if s.deps == nil {
		return nil
	}
	return s.deps.Chat
}

func (s *TCPServer) integrationScopeStore() (integrationScopeStore, bool) {
	if s.deps == nil {
		return nil, false
	}
	scopes, ok := s.deps.Auth.(integrationScopeStore)
	return scopes, ok
}

// GetIntegrationScopes reports an integration account's scope ceiling. It
// needs ManageServer.
func (s *TCPServer) GetIntegrationScopes(ctx context.Context, principal auth.IntegrationPrincipal, request netproto.IntegrationScopesQuery) (netproto.IntegrationScopes, error) {
	if !request.Valid() {
		return netproto.IntegrationScopes{}, authorization.ErrRoleInvalid
	}
	var result netproto.IntegrationScopes
	err := s.withIntegrationPolicy(ctx, principal, func(ctx context.Context, e *authorization.RoleEvaluator) error {
		if !e.Evaluate(principal.UserID(), 0, authorization.ManageServer).Allowed {
			return authorization.ErrRoleForbidden
		}
		scopes, ok := s.integrationScopeStore()
		if !ok {
			return authorization.ErrAuthorizationUnavailable
		}
		state, err := scopes.GetIntegrationScopes(ctx, request.UniqueID)
		if err != nil {
			return integrationScopeError(err)
		}
		result = scopesResult(state)
		return nil
	})
	return result, err
}

// SetIntegrationScopes replaces an integration account's scope ceiling. It
// needs ManageServer and is audited. A scoped caller can only hand out scopes
// it holds itself and cannot clear a ceiling, so scopes never widen through
// an integration. The change takes the exclusive policy lease: operations
// already admitted finish first, and the account's sessions must sign in
// again because their stored ceiling no longer matches.
func (s *TCPServer) SetIntegrationScopes(ctx context.Context, principal auth.IntegrationPrincipal, request netproto.IntegrationScopesSet) (netproto.IntegrationScopes, error) {
	if !request.Valid() {
		return netproto.IntegrationScopes{}, authorization.ErrRoleInvalid
	}
	var scopes []string
	if !request.Full {
		var err error
		if scopes, err = auth.NormalizeIntegrationScopes(request.Scopes); err != nil {
			return netproto.IntegrationScopes{}, authorization.ErrRoleInvalid
		}
	}
	if own, scoped := principal.Scopes(); scoped {
		if request.Full {
			return netproto.IntegrationScopes{}, authorization.ErrRoleForbidden
		}
		for _, scope := range scopes {
			if !slices.Contains(own, scope) {
				return netproto.IntegrationScopes{}, authorization.ErrRoleForbidden
			}
		}
	}
	var result netproto.IntegrationScopes
	err := s.withExclusiveIntegrationPolicy(ctx, principal, func(ctx context.Context, e *authorization.RoleEvaluator) error {
		if !e.Evaluate(principal.UserID(), 0, authorization.ManageServer).Allowed {
			return authorization.ErrRoleForbidden
		}
		store, ok := s.integrationScopeStore()
		if !ok {
			return authorization.ErrAuthorizationUnavailable
		}
		state, err := store.SetIntegrationScopes(ctx, request.UniqueID, request.Full, scopes)
		if err != nil {
			return integrationScopeError(err)
		}
		auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		detail := "full"
		if !state.Full {
			detail = "scopes=" + strings.Join(state.Scopes, ",")
		}
		s.audit(auditCtx, principal.UniqueID(), "integration_scopes", state.UniqueID, detail)
		result = scopesResult(state)
		return nil
	})
	return result, err
}

func scopesResult(state auth.IntegrationScopeState) netproto.IntegrationScopes {
	scopes := state.Scopes
	if scopes == nil {
		scopes = []string{}
	}
	return netproto.IntegrationScopes{UniqueID: state.UniqueID, Full: state.Full, Scopes: scopes}
}

func integrationScopeError(err error) error {
	if errors.Is(err, auth.ErrIntegrationNotFound) || errors.Is(err, auth.ErrIntegrationScopeInvalid) {
		return authorization.ErrRoleInvalid
	}
	return err
}
