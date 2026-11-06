package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

func (s *TCPServer) SetIntegrationServerText(ctx context.Context, principal auth.IntegrationPrincipal, request netproto.ServerTextSet) (netproto.ServerTextResult, error) {
	if !request.Valid() {
		return netproto.ServerTextResult{}, authorization.ErrRoleInvalid
	}
	var result netproto.ServerTextResult
	err := s.withIntegrationPolicy(ctx, principal, func(ctx context.Context, e *authorization.RoleEvaluator) error {
		if !e.Evaluate(principal.UserID(), 0, authorization.ManageServer).Allowed {
			return authorization.ErrRoleForbidden
		}
		if err := s.setServerSettingAndAnnounce(ctx, request.Key, *request.Value, principal.UniqueID()); err != nil {
			return err
		}
		hash := sha256.Sum256([]byte(*request.Value))
		result = netproto.ServerTextResult{Key: request.Key, ContentHash: hex.EncodeToString(hash[:])}
		return nil
	})
	return result, err
}

// BroadcastIntegrationAlert sends a one-time, sealed alert to every member
// allowed to see announcements. It is not stored: clients that connect later
// never receive it, and the stored announcement is left unchanged. The event
// reuses the announcement envelope with transient=true so existing decryption
// applies; older clients show it as the banner until they reconnect.
func (s *TCPServer) BroadcastIntegrationAlert(ctx context.Context, principal auth.IntegrationPrincipal, request netproto.ServerBroadcast) (netproto.ServerBroadcastResult, error) {
	if !request.Valid() {
		return netproto.ServerBroadcastResult{}, authorization.ErrRoleInvalid
	}
	var result netproto.ServerBroadcastResult
	err := s.withIntegrationPolicy(ctx, principal, func(ctx context.Context, e *authorization.RoleEvaluator) error {
		if !e.Evaluate(principal.UserID(), 0, authorization.ManageServer).Allowed {
			return authorization.ErrRoleForbidden
		}
		text, keyID, err := s.sealGlobalText(ctx, "broadcast", request.Text)
		if err != nil {
			return err
		}
		s.broadcastEvent(eventAnnouncement, map[string]any{"text": text, "enc": true, "key_id": keyID, "transient": true})
		auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		s.audit(auditCtx, principal.UniqueID(), "server_broadcast", "", fmt.Sprintf("bytes=%d", len(request.Text)))
		hash := sha256.Sum256([]byte(request.Text))
		result = netproto.ServerBroadcastResult{ContentHash: hex.EncodeToString(hash[:])}
		return nil
	})
	return result, err
}
