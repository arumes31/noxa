package main

import "noxa/internal/netproto"

// SupportsStreamCodecRefreshForTab permits an in-place capability addition only
// when this authenticated server explicitly advertises the newer semantics.
func (a *App) SupportsStreamCodecRefreshForTab(tabID string) bool {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return false
	}
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.conn != nil && cm.clientID != "" && cm.supportsStreamCodecRefresh && cm.supportsStreamCodec && cm.supportsStreamSourceQuality && cm.authorizationModel == netproto.AuthorizationModelRolesV1
}

func (a *App) SupportsStreamReceiverRecoveryForTab(tabID string) bool {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return false
	}
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.conn != nil && cm.clientID != "" && cm.supportsStreamReceiverRecovery && cm.authorizationModel == netproto.AuthorizationModelRolesV1
}

func (a *App) SupportsStreamCodecForTab(tabID string) bool {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return false
	}
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.conn != nil && cm.clientID != "" && cm.supportsStreamCodec && cm.authorizationModel == netproto.AuthorizationModelRolesV1
}

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
