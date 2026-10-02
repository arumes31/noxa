package main

import (
	"time"

	"noxa/internal/netproto"
)

// SetAudioStateForTab announces self mute/deafen on supporting servers. Local
// controls still work immediately when connected to an older server.
func (a *App) SetAudioStateForTab(tabID string, muted, deafened bool) string {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return err.Error()
	}
	cm.audioStateMu.Lock()
	defer cm.audioStateMu.Unlock()
	cm.mu.Lock()
	supported, clientID, conn, epoch := cm.supportsAudioState, cm.clientID, cm.conn, cm.connEpoch
	cm.mu.Unlock()
	if !supported {
		return ""
	}
	if clientID == "" || conn == nil {
		return "not authenticated"
	}
	muted = muted || deafened
	f, err := cm.request(netproto.MsgAudioStateSet, netproto.MsgAudioStateSaved,
		netproto.AudioStateSet{Muted: muted, Deafened: deafened}, 10*time.Second)
	if err != nil {
		return err.Error()
	}
	var saved netproto.AudioStateSaved
	if err := netproto.Decode(f, &saved); err != nil {
		return err.Error()
	}
	if saved.ClientID != clientID || saved.Muted != muted || saved.Deafened != deafened {
		return "audio state acknowledgement does not match the request"
	}
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if cm.conn != conn || cm.connEpoch != epoch {
		return "connection changed while updating audio state"
	}
	return ""
}
