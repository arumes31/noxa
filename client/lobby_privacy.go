package main

import (
	"errors"
	"noxa/internal/netproto"
	"sync"
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
	a.settingsMu.Lock()
	hidden := a.settings.HideLobbyPresence
	a.settingsMu.Unlock()
	tabs := a.tabsRegistry()
	failures := make([]error, len(tabs))
	var pending sync.WaitGroup
	index := 0
	for _, tab := range tabs {
		cm := tab.cm
		if cm == nil || (target != nil && cm != target) {
			continue
		}
		// Keep batches ordered, but let independent servers acknowledge in
		// parallel. A slow server must not add its timeout once per tab.
		pending.Add(1)
		go func(index int, cm *connManager) {
			defer pending.Done()
			failures[index] = applyConnectionLobbyPrivacy(cm, hidden)
		}(index, cm)
		index++
	}
	pending.Wait()
	return errors.Join(failures...)
}

func applyConnectionLobbyPrivacy(cm *connManager, hidden bool) error {
	cm.mu.Lock()
	connected, supported, applied, id, epoch := cm.conn != nil && !cm.closed, cm.supportsLobbyPrivacy, cm.lobbyPrivacyApplied, cm.clientID, cm.connEpoch
	cm.mu.Unlock()
	if !connected || hidden == applied {
		return nil
	}
	if !supported {
		return errors.New("lobby privacy saved, but this server must be upgraded to apply it")
	}
	frame, err := cm.request(netproto.MsgSetStatus, netproto.MsgStatusSaved,
		netproto.SetStatus{AckRequested: true, PrivacyOnly: true, HideLobbyPresence: &hidden}, 10*time.Second)
	if err != nil {
		return err
	}
	var reply netproto.StatusSaved
	if err = netproto.Decode(frame, &reply); err != nil || reply.ClientID != id || reply.HideLobbyPresence == nil || *reply.HideLobbyPresence != hidden {
		return errors.New("server did not confirm lobby privacy")
	}
	cm.mu.Lock()
	if cm.connEpoch == epoch && cm.clientID == id {
		cm.lobbyPrivacyApplied = hidden
	}
	cm.mu.Unlock()
	return nil
}
