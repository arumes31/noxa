package main

import (
	"encoding/json"
	"log"
	"slices"

	"noxa/internal/broadcast"
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
	info, bookmark, old, channel := source.info, source.bookmark, source.cm, source.lastVoiceChannel
	a.tabsMu.Unlock()
	old.mu.Lock()
	displayName := old.displayName
	old.mu.Unlock()
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
	if failure := cm.connectNamed(info.Addr, info.Nickname, displayName, password, serverPassword); failure != "" {
		cm.mu.Lock()
		terminal := cm.reconnectTerminal
		cm.mu.Unlock()
		if failure == errFingerprintMismatch.Error() {
			failure = fingerprintMismatchMessage(cm)
		}
		return ConnectTabResult{Error: failure, Terminal: terminal}
	}
	// Cancellation during authentication cannot close a socket that connectWith
	// has not installed yet. Recheck ownership before sending any post-login work.
	a.settingsMu.Lock()
	enabled = a.settings.ReconnectOnLoss
	a.settingsMu.Unlock()
	a.tabsMu.Lock()
	owned := enabled && a.tabs[tabID] == source && source.replacement == candidate && source.reconnectAllowed && !candidate.reconnectSuppressed
	a.tabsMu.Unlock()
	if !owned {
		return ConnectTabResult{Error: "server tab changed during reconnect", Terminal: true}
	}
	// The normal join is permission-checked and acknowledged by the server.
	// Never reuse a channel password or force membership after policy changes.
	warning := ""
	if channel > 0 {
		if failure := cm.joinChannel(channel); failure != "" {
			warning = failure
		}
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
	if channel > 0 && warning == "" {
		// A membership broadcast may follow its acknowledgement and arrive after
		// replay. Tell the frontend which exact automatic move must retain audio
		// privacy settings even in that ordering.
		a.emitPlain("tab_voice_restored", map[string]any{"tab_id": tabID, "client_id": cm.clientIDSnapshot(), "channel_id": channel})
	}
	if selected {
		a.finishActivateSerialized(tabID, cm, journal, generation)
	} else {
		a.emitTabsUpdate()
	}
	return ConnectTabResult{TabID: tabID, Warning: warning}
}

func (m *connManager) markReconnectTerminal() {
	m.mu.Lock()
	m.reconnectTerminal = true
	m.mu.Unlock()
}

// Called with tabsMu held, including for inactive tabs and provisional managers.
func rememberReconnectChannel(tab *tabState, name, payload, clientID string) {
	if clientID == "" {
		return
	}
	if name == "snapshot" {
		var snapshot broadcast.TreeSnapshot
		if json.Unmarshal([]byte(payload), &snapshot) != nil {
			return
		}
		var visit func([]*broadcast.ChannelNode)
		visit = func(nodes []*broadcast.ChannelNode) {
			for _, node := range nodes {
				if node == nil {
					continue
				}
				for _, member := range node.Clients {
					if member != nil && member.ClientID == clientID {
						tab.lastVoiceChannel = member.ChannelID
					}
				}
				visit(node.Children)
			}
		}
		visit(snapshot.RootChannels)
		for _, member := range snapshot.UnassignedClients {
			if member != nil && member.ClientID == clientID {
				tab.lastVoiceChannel = 0
			}
		}
	} else if name == "event" {
		var event struct {
			Type string `json:"type"`
			Data struct {
				ClientID   string  `json:"client_id"`
				ChannelID  int64   `json:"channel_id"`
				ChannelIDs []int64 `json:"channel_ids"`
			} `json:"data"`
		}
		if json.Unmarshal([]byte(payload), &event) != nil {
			return
		}
		switch event.Type {
		case "user_moved":
			if event.Data.ClientID == clientID {
				tab.lastVoiceChannel = event.Data.ChannelID
			}
		case "kicked":
			if event.Data.ClientID == clientID {
				tab.lastVoiceChannel = 0
			}
		case "channel_deleted":
			if event.Data.ChannelID == tab.lastVoiceChannel || slices.Contains(event.Data.ChannelIDs, tab.lastVoiceChannel) {
				tab.lastVoiceChannel = 0
			}
		}
	}
}
