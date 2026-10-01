package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

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
