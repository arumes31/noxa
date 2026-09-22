package main

// SessionInfo is one connection's UI identity and transport status.
type SessionInfo struct {
	AuthorizationModel string `json:"authorization_model"`
	ClientID           string `json:"client_id"`
	IsGuest            bool   `json:"is_guest"`
	Connected          bool   `json:"connected"`
	Security           string `json:"security"`
}

// SessionInfoForTab captures the initiating tab before reading its session.
// It never combines identity or privilege flags from different connections.
func (a *App) SessionInfoForTab(tabID string) (SessionInfo, error) {
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return SessionInfo{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.conn == nil {
		return SessionInfo{IsGuest: true, Security: "offline"}, nil
	}
	return SessionInfo{
		AuthorizationModel: m.authorizationModel,
		ClientID:           m.clientID, IsGuest: m.isGuest,
		Connected: true, Security: connectionSecurity(m.tlsUsed, m.fingerprint, m.newServer),
	}, nil
}
