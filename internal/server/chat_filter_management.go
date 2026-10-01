package server

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

// loadManagedChatFilters never substitutes defaults for a failed/corrupt read:
// a partial save must not erase lists the operator did not change.
func (s *TCPServer) loadManagedChatFilters(ctx context.Context) (chatFilters, bool, error) {
	if s.deps == nil || s.deps.Chat == nil {
		return chatFilters{}, false, authorization.ErrAuthorizationUnavailable
	}
	if err := ctx.Err(); err != nil {
		return chatFilters{}, false, err
	}
	raw, _, err := s.deps.Chat.GetServerSetting(ctx, chatFiltersKey)
	if err != nil {
		return chatFilters{}, false, err
	}
	if raw == "" {
		return s.configFilters(), true, nil
	}
	var filters *chatFilters
	if err := json.Unmarshal([]byte(raw), &filters); err != nil {
		return chatFilters{}, false, fmt.Errorf("decoding chat filters: %w", err)
	}
	if filters == nil {
		return chatFilters{}, false, fmt.Errorf("stored chat filters must be an object")
	}
	return *filters, false, nil
}

func chatFilterResponse(filters chatFilters, fromConfig bool) netproto.ChatFilterResponse {
	return netproto.ChatFilterResponse{WordFilter: filters.WordFilter, LinkBlacklist: filters.LinkBlacklist, LinkWhitelist: filters.LinkWhitelist, FromConfig: fromConfig}
}

func (s *TCPServer) readManagedChatFilters(ctx context.Context) (netproto.ChatFilterResponse, error) {
	if s.chatFilters == nil {
		return netproto.ChatFilterResponse{}, authorization.ErrAuthorizationUnavailable
	}
	if err := s.chatFilters.writeMu.LockContext(ctx); err != nil {
		return netproto.ChatFilterResponse{}, err
	}
	defer s.chatFilters.writeMu.Unlock()
	filters, fromConfig, err := s.loadManagedChatFilters(ctx)
	if err != nil {
		return netproto.ChatFilterResponse{}, err
	}
	if err := ctx.Err(); err != nil {
		return netproto.ChatFilterResponse{}, err
	}
	return chatFilterResponse(filters, fromConfig), nil
}

// Callers retain their management lease. Storage, cache publication and audit
// share one write order; the acknowledgement is this saved result.
func (s *TCPServer) saveChatFilters(ctx context.Context, actor string, patch netproto.ChatFilterSet) (netproto.ChatFilterResponse, error) {
	if !patch.ValidLists() {
		return netproto.ChatFilterResponse{}, authorization.ErrRoleInvalid
	}
	if s.chatFilters == nil {
		return netproto.ChatFilterResponse{}, authorization.ErrAuthorizationUnavailable
	}
	if err := s.chatFilters.writeMu.LockContext(ctx); err != nil {
		return netproto.ChatFilterResponse{}, err
	}
	defer s.chatFilters.writeMu.Unlock()
	next, _, err := s.loadManagedChatFilters(ctx)
	if err != nil {
		return netproto.ChatFilterResponse{}, err
	}
	for _, update := range []struct {
		in  *string
		out *string
	}{{patch.WordFilter, &next.WordFilter}, {patch.LinkBlacklist, &next.LinkBlacklist}, {patch.LinkWhitelist, &next.LinkWhitelist}} {
		if update.in != nil {
			*update.out = normalizeList(*update.in)
		}
	}
	raw, err := json.Marshal(next)
	if err != nil {
		return netproto.ChatFilterResponse{}, err
	}
	if err := ctx.Err(); err != nil {
		return netproto.ChatFilterResponse{}, err
	}
	if err := s.deps.Chat.SetServerSetting(ctx, chatFiltersKey, string(raw), 0); err != nil {
		return netproto.ChatFilterResponse{}, err
	}
	// Publish known committed values without a cancellation-sensitive reload.
	s.chatFilters.mu.Lock()
	s.chatFilters.filters, s.chatFilters.fromConfig, s.chatFilters.loaded = next, false, true
	s.chatFilters.mu.Unlock()
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	s.audit(auditCtx, actor, "chat_filter_set", chatFiltersKey, fmt.Sprintf("words=%d blacklist=%d whitelist=%d", len(splitList(next.WordFilter)), len(splitList(next.LinkBlacklist)), len(splitList(next.LinkWhitelist))))
	return chatFilterResponse(next, false), nil
}
