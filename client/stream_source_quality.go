package main

import "noxa/internal/netproto"

// SupportsStreamSourceQualityForTab reports whether this authenticated server
// supplies generation-scoped upload demand for source-quality publications.
func (a *App) SupportsStreamSourceQualityForTab(tabID string) bool {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return false
	}
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.conn != nil && cm.clientID != "" && cm.supportsStreamSourceQuality && cm.authorizationModel == netproto.AuthorizationModelRolesV1
}
