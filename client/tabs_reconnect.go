package main

import (
	"log"

	"noxa/internal/netproto"
)

// ReconnectTab recovers an unexpectedly disconnected tab in place. Its address,
// account name and local encryption identity belong to the original tab; only
// the in-memory credentials are supplied by the caller. A candidate connection
// cannot publish into the selected view until its ownership is committed.
func (a *App) ReconnectTab(tabID, password, serverPassword string) ConnectTabResult {
	a.tabsMu.Lock()
	source := a.tabs[tabID]
	if source == nil || !source.reconnectAllowed || source.replacement != nil {
		a.tabsMu.Unlock()
		return ConnectTabResult{Error: "server tab is not available for automatic reconnect"}
	}
	info, bookmark, old := source.info, source.bookmark, source.cm
	a.tabsMu.Unlock()
	a.settingsMu.Lock()
	enabled := a.settings.ReconnectOnLoss
	a.settingsMu.Unlock()
	if !enabled || old.connected() {
		return ConnectTabResult{Error: "automatic reconnect is no longer needed"}
	}
	identity, err := old.identity()
	if err != nil {
		return ConnectTabResult{Error: err.Error()}
	}
	if info.Addr == "" || info.Nickname == "" {
		return ConnectTabResult{Error: "the original server connection is unavailable"}
	}
	cm := a.newTabManager(tabID, identity)
	candidate := &tabState{cm: cm, info: info, bookmark: bookmark}
	candidate.info.Connected = false
	a.tabsMu.Lock()
	if a.tabs[tabID] != source || !source.reconnectAllowed || source.replacement != nil {
		a.tabsMu.Unlock()
		return ConnectTabResult{Error: "server tab changed before reconnect"}
	}
	source.replacement = candidate
	a.tabsMu.Unlock()
	committed := false
	defer func() {
		if committed {
			return
		}
		a.tabsMu.Lock()
		if a.tabs[tabID] == source && source.replacement == candidate {
			source.replacement = nil
		}
		a.tabsMu.Unlock()
		cm.disconnect()
	}()
	if failure := cm.connect(info.Addr, info.Nickname, password, serverPassword); failure != "" {
		if failure == errFingerprintMismatch.Error() {
			failure = fingerprintMismatchMessage(cm)
		}
		return ConnectTabResult{Error: failure}
	}
	// Reapply only this connection's avatar. A background recovery must never
	// replace the selected server's hotkey profile or selected local identity.
	if b := a.lookupBookmark(bookmark, info.Addr, info.Nickname); b != nil && b.AvatarOverrideB64 != "" {
		if err := cm.write(netproto.MsgAvatarSet, netproto.AvatarSet{DataBase64: b.AvatarOverrideB64}); err != nil {
			log.Printf("avatar override upload failed: %v", err)
		}
	}
	a.activationPublishMu.Lock()
	defer a.activationPublishMu.Unlock()
	a.settingsMu.Lock()
	enabled = a.settings.ReconnectOnLoss
	a.settingsMu.Unlock()
	connected := cm.connected()
	a.tabsMu.Lock()
	if !enabled || !connected || a.tabs[tabID] != source || source.replacement != candidate ||
		!source.reconnectAllowed || candidate.reconnectSuppressed || candidate.reconnectAllowed {
		a.tabsMu.Unlock()
		return ConnectTabResult{Error: "server tab changed during reconnect"}
	}
	// Published manager pointers remain immutable. Readers holding the old
	// tabState can finish safely; the sink rejects its subsequent events.
	candidate.inheritTransferHistory(source)
	a.tabs[tabID] = candidate
	source.replacement = nil
	candidate.info.Connected = true
	selected := a.activeID == tabID
	var journal []journalEntry
	var generation uint64
	if selected {
		_, journal, generation, _ = a.activateLocked(tabID)
	}
	a.tabsMu.Unlock()
	committed = true
	old.disconnect()
	if selected {
		a.finishActivateSerialized(tabID, cm, journal, generation)
	} else {
		a.emitTabsUpdate()
	}
	return ConnectTabResult{TabID: tabID}
}
