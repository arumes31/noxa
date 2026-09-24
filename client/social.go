// social.go defines the Wails-bound API for wave-8b presence and social
// features: presence status, pokes, and the public server-info query.
package main

import (
	"strings"
	"time"

	"noxa/internal/netproto"
)

// SetStatus sets presence and waits for confirmation in role mode.
func (a *App) SetStatus(status, message string) string {
	cm, err := a.requireCM()
	if err != nil {
		return err.Error()
	}
	return a.setStatus(cm, status, message)
}

// SetStatusForTab rejects presence changes from a different server's view.
func (a *App) SetStatusForTab(tabID, status, message string) string {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return err.Error()
	}
	return a.setStatus(cm, status, message)
}

func (a *App) setStatus(cm *connManager, status, message string) string {
	// (390) the idle timer publishes the autoAwaySentinel; the text other
	// clients actually see comes from the user's configured away message.
	if status == "away" && message == autoAwaySentinel {
		message = a.autoAwayMessage()
	}
	msg := netproto.SetStatus{Status: status, Message: message, AckRequested: cm.usesRoleAuthorization()}
	if !msg.AckRequested {
		if err := cm.write(netproto.MsgSetStatus, msg); err != nil {
			return err.Error()
		}
		return ""
	}
	clientID := cm.clientIDSnapshot()
	if clientID == "" {
		return "not authenticated"
	}
	normalized := strings.ToLower(strings.TrimSpace(status))
	if normalized == "online" {
		normalized = ""
	}
	f, err := cm.request(netproto.MsgSetStatus, netproto.MsgStatusSaved, msg, 10*time.Second)
	if err != nil {
		return err.Error()
	}
	var saved struct {
		ClientID string  `json:"client_id"`
		Status   *string `json:"status"`
		Message  *string `json:"message"`
	}
	if err := netproto.Decode(f, &saved); err != nil {
		return err.Error()
	}
	if saved.ClientID != clientID || saved.Status == nil || saved.Message == nil ||
		*saved.Status != normalized || *saved.Message != message {
		return "status acknowledgement does not match the request"
	}
	return ""
}

// Poke sends a poke to a client. Role-mode calls wait for queue acceptance;
// legacy errors arrive via servererror.
func (a *App) Poke(clientID, message string) string {
	cm, err := a.requireCM()
	if err != nil {
		return err.Error()
	}
	return cm.poke(clientID, message)
}

// PokeForTab rejects actions from another server's view.
func (a *App) PokeForTab(tabID string, clientID, message string) string {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return err.Error()
	}
	return cm.poke(clientID, message)
}

func (cm *connManager) poke(clientID, message string) string {
	acknowledge := cm.usesRoleAuthorization()
	msg := netproto.Poke{ClientID: clientID, Message: message, AckRequested: acknowledge}
	if !acknowledge {
		if err := cm.write(netproto.MsgPoke, msg); err != nil {
			return err.Error()
		}
		return ""
	}
	f, err := cm.request(netproto.MsgPoke, netproto.MsgPokeAccepted, msg, 5*time.Second)
	if err != nil {
		return err.Error()
	}
	var accepted netproto.PokeAccepted
	if err := netproto.Decode(f, &accepted); err != nil {
		return err.Error()
	}
	if clientID == "" || accepted.ClientID != clientID {
		return "poke acknowledgement does not match the target"
	}
	return ""
}

// ServerInfo returns the server's public information (313).
func (a *App) ServerInfo() (netproto.ServerInfoResponse, error) {
	cm, err := a.requireCM()
	if err != nil {
		return netproto.ServerInfoResponse{}, err
	}
	return cm.serverInfo()
}

// ServerInfoForTab keeps metadata queries on their originating server.
func (a *App) ServerInfoForTab(tabID string) (netproto.ServerInfoResponse, error) {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return netproto.ServerInfoResponse{}, err
	}
	return cm.serverInfo()
}

func (cm *connManager) serverInfo() (netproto.ServerInfoResponse, error) {
	f, err := cm.request(netproto.MsgServerInfoQuery, netproto.MsgServerInfoResponse,
		netproto.ServerInfoQuery{}, 5*time.Second)
	if err != nil {
		return netproto.ServerInfoResponse{}, err
	}
	var resp netproto.ServerInfoResponse
	if err := decodeJSON(f, &resp); err != nil {
		return netproto.ServerInfoResponse{}, err
	}
	return resp, nil
}
