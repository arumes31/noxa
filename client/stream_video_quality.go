package main

import (
	"strconv"
	"time"

	"noxa/internal/netproto"
)

// SupportsStreamVideoQualityForTab reports capability without changing legacy
// global quality. A missing/disconnected tab is unsupported.
func (a *App) SupportsStreamVideoQualityForTab(tabID string) bool {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return false
	}
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.conn != nil && cm.supportsStreamVideoQuality && cm.authorizationModel == netproto.AuthorizationModelRolesV1
}

// SetStreamVideoQualityForTab tunes only the identified active publication.
// Decimal lifetime strings avoid loss of uint64 precision in JavaScript.
func (a *App) SetStreamVideoQualityForTab(tabID, publisherID, slot, generation, session, quality string) string {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return err.Error()
	}
	cm.mu.Lock()
	supported, model, conn, clientID := cm.supportsStreamVideoQuality, cm.authorizationModel, cm.conn, cm.clientID
	cm.mu.Unlock()
	if !supported || model != netproto.AuthorizationModelRolesV1 {
		return "independent stream quality is not supported by this server"
	}
	if conn == nil || clientID == "" {
		return "not authenticated"
	}
	if publisherID == "" || (slot != "cam" && slot != "screen") ||
		(quality != "high" && quality != "mid" && quality != "low" && quality != "default") {
		return "invalid stream quality selection"
	}
	gen, err := strconv.ParseUint(generation, 10, 64)
	if err != nil || gen == 0 {
		return "invalid stream publication"
	}
	epoch, err := strconv.ParseUint(session, 10, 64)
	if err != nil || epoch == 0 {
		return "invalid stream session"
	}
	// Pin the capability check to this transport: a reconnect to an older
	// server must never interpret this scoped request as a global preference.
	frame, err := cm.requestOn(conn, netproto.MsgVideoQuality, netproto.MsgMediaControlSaved,
		netproto.VideoQuality{Quality: quality, AckRequested: true, PublisherID: publisherID,
			Slot: slot, Generation: gen, Session: epoch}, 10*time.Second)
	if err != nil {
		return err.Error()
	}
	if err := validateMediaControlReply(frame, netproto.MediaControlSaved{Operation: netproto.MsgVideoQuality, ClientID: clientID, Quality: quality,
		PublisherID: publisherID, Slot: slot, Generation: gen, Session: epoch}); err != nil {
		return err.Error()
	}
	return ""
}
