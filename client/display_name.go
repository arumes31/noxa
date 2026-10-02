package main

import (
	"time"

	"noxa/internal/netproto"
)

// SetDisplayNameForTab changes this session's public name after server confirmation.
func (a *App) SetDisplayNameForTab(tabID, name string) string {
	name, err := netproto.NormalizeDisplayName(name)
	if err != nil {
		return err.Error()
	}
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return err.Error()
	}
	cm.displayNameMu.Lock()
	defer cm.displayNameMu.Unlock()
	cm.mu.Lock()
	clientID, conn, epoch := cm.clientID, cm.conn, cm.connEpoch
	cm.mu.Unlock()
	if clientID == "" || conn == nil {
		return "not authenticated"
	}
	f, err := cm.request(netproto.MsgDisplayNameSet, netproto.MsgDisplayNameSaved,
		netproto.DisplayNameSet{Nickname: name}, 10*time.Second)
	if err != nil {
		return err.Error()
	}
	var saved netproto.DisplayNameSaved
	if err := netproto.Decode(f, &saved); err != nil {
		return err.Error()
	}
	if saved.ClientID != clientID || saved.Nickname != name {
		return "display name acknowledgement does not match the request"
	}
	cm.mu.Lock()
	if cm.conn != conn || cm.connEpoch != epoch {
		cm.mu.Unlock()
		return "connection changed while updating the display name"
	}
	cm.nickname, cm.displayName = saved.Nickname, saved.Nickname
	cm.mu.Unlock()
	a.emitTabsUpdate()
	return ""
}
