// queryadmin.go implements the runtime max-clients override.
package server

import (
	"context"
	"strconv"

)

// EffectiveMaxClients returns the current connection cap. Runtime server UI
// changes are folded into cfg under configMu and persisted for restart.
func (s *TCPServer) EffectiveMaxClients(ctx context.Context) int {
	s.configMu.RLock()
	max := s.cfg.MaxClients
	s.configMu.RUnlock()
	if value := s.serverSetting(ctx, "max_clients_override"); value != "" {
		if override, err := strconv.Atoi(value); err == nil && override >= 0 {
			return override
		}
	}
	return max
}
