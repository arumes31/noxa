// Package main exposes the roles-v1 desktop administration API.
package main

import (
	"time"

	"noxa/internal/netproto"
)

// IsGuest reports whether the active connection is an anonymous guest
// session. The frontend uses this to stop account-only actions before they
// reach the server.
func (a *App) IsGuest() bool {
	cm := a.cmLoad()
	return cm != nil && cm.isGuestSnapshot()
}

// --- audit log ---------------------------------------------------------------

// AuditLog returns a page of the audit log (beforeID 0 = latest page).
func (a *App) AuditLog(beforeID int64, limit int) (netproto.AuditLogResponse, error) {
	m, err := a.requireCM()
	if err != nil {
		return netproto.AuditLogResponse{}, err
	}
	return m.auditLog(beforeID, limit)
}

// AuditLogForTab keeps pagination bound to the server where the dialog opened.
func (a *App) AuditLogForTab(tabID string, beforeID int64, limit int) (netproto.AuditLogResponse, error) {
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return netproto.AuditLogResponse{}, err
	}
	return m.auditLog(beforeID, limit)
}

func (m *connManager) auditLog(beforeID int64, limit int) (netproto.AuditLogResponse, error) {
	f, err := m.request(netproto.MsgAuditLog, netproto.MsgAuditLogResponse,
		netproto.AuditLog{BeforeID: beforeID, Limit: limit}, 5*time.Second)
	if err != nil {
		return netproto.AuditLogResponse{}, err
	}
	var resp netproto.AuditLogResponse
	if err := decodeJSON(f, &resp); err != nil {
		return netproto.AuditLogResponse{}, err
	}
	return resp, nil
}

// --- bans --------------------------------------------------------------------

// BanList returns the ban list (gated server-side by ban power / admin).
func (a *App) BanList() (netproto.BanListResponse, error) {
	m, err := a.requireCM()
	if err != nil {
		return netproto.BanListResponse{}, err
	}
	return m.banList()
}

// BanListForTab rejects operations from a different server's dialog.
func (a *App) BanListForTab(tabID string) (netproto.BanListResponse, error) {
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return netproto.BanListResponse{}, err
	}
	return m.banList()
}

func (m *connManager) banList() (netproto.BanListResponse, error) {
	f, err := m.request(netproto.MsgBanList, netproto.MsgBanListResponse,
		netproto.BanList{}, 5*time.Second)
	if err != nil {
		return netproto.BanListResponse{}, err
	}
	var resp netproto.BanListResponse
	if err := decodeJSON(f, &resp); err != nil {
		return netproto.BanListResponse{}, err
	}
	return resp, nil
}

// KickClient kicks a client from its channel or the server (ban = record a
// ban and kick from the server; durationSeconds > 0 = temporary ban). Gated
// server-side by kick/ban powers.
func (a *App) KickClient(clientID string, fromServer, ban bool, reason string, durationSeconds int64) string {
	cm, err := a.requireCM()
	if err != nil {
		return err.Error()
	}
	return cm.kickClient(clientID, fromServer, ban, reason, durationSeconds)
}

// KickClientForTab rejects actions from another server's view.
func (a *App) KickClientForTab(tabID string, clientID string, fromServer, ban bool, reason string, durationSeconds int64) string {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return err.Error()
	}
	return cm.kickClient(clientID, fromServer, ban, reason, durationSeconds)
}

func (cm *connManager) kickClient(clientID string, fromServer, ban bool, reason string, durationSeconds int64) string {
	if err := cm.kickClientAcknowledged(netproto.KickClient{
		ClientID: clientID, FromServer: fromServer, Ban: ban, Reason: reason,
		DurationSeconds: durationSeconds,
	}); err != nil {
		return err.Error()
	}
	return ""
}

// DisconnectMemberForTab binds a role-mode disconnect to the displayed channel.
func (a *App) DisconnectMemberForTab(tabID, clientID string, expectedChannelID int64, reason string) string {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return err.Error()
	}
	if !cm.usesRoleAuthorization() {
		return "scoped member disconnect requires role authorization"
	}
	if clientID == "" || expectedChannelID <= 0 {
		return "choose a member's current channel before disconnecting"
	}
	if err := cm.kickClientAcknowledged(netproto.KickClient{ClientID: clientID, ExpectedChannelID: expectedChannelID, Reason: reason}); err != nil {
		return err.Error()
	}
	return ""
}
