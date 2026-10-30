package main

import (
	"errors"
	"noxa/internal/netproto"
	"time"
)

// Apply the latest saved value to every live desktop tab, preserving presence
// status and its message. Only an explicit server acknowledgement marks it done.
func (a *App) applyLobbyPrivacy() error {
	return a.applyLobbyPrivacyFor(nil)
}

func (a *App) applyLobbyPrivacyFor(target *connManager) error {
	a.lobbyPrivacyMu.Lock()
	defer a.lobbyPrivacyMu.Unlock()
	var failures []error
	for _, tab := range a.tabsRegistry() {
		a.settingsMu.Lock()
		hidden := a.settings.HideLobbyPresence
		a.settingsMu.Unlock()
		cm := tab.cm
		if cm == nil || (target != nil && cm != target) {
			continue
		}
		cm.mu.Lock()
		connected, supported, applied, id, epoch := cm.conn != nil && !cm.closed, cm.supportsLobbyPrivacy, cm.lobbyPrivacyApplied, cm.clientID, cm.connEpoch
		cm.mu.Unlock()
		if !connected || hidden == applied {
			continue
		}
		if !supported {
			failures = append(failures, errors.New("lobby privacy saved, but this server must be upgraded to apply it"))
			continue
		}
		frame, err := cm.request(netproto.MsgSetStatus, netproto.MsgStatusSaved,
			netproto.SetStatus{AckRequested: true, PrivacyOnly: true, HideLobbyPresence: &hidden}, 10*time.Second)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		var reply netproto.StatusSaved
		if err = netproto.Decode(frame, &reply); err != nil || reply.ClientID != id || reply.HideLobbyPresence == nil || *reply.HideLobbyPresence != hidden {
			failures = append(failures, errors.New("server did not confirm lobby privacy"))
			continue
		}
		cm.mu.Lock()
		if cm.connEpoch == epoch && cm.clientID == id {
			cm.lobbyPrivacyApplied = hidden
		}
		cm.mu.Unlock()
	}
	return errors.Join(failures...)
}
