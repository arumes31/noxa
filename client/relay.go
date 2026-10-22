package main

import (
	"errors"
	"net"
	"time"

	"noxa/internal/netproto"
)

func (m *connManager) relayRenewalLoop(conn net.Conn) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		m.mu.Lock()
		current := m.conn == conn && m.managedRelay
		m.mu.Unlock()
		if !current {
			return
		}
		_ = m.refreshRelay(conn)
		<-ticker.C
	}
}
func (m *connManager) refreshRelay(conn net.Conn) error {
	// Serialize cache publication as well as request/reply ordering. Otherwise a
	// delayed caller could overwrite a newer generation after its gate releases.
	gate := m.requestGate(netproto.MessageType(65530))
	gate.Lock()
	defer gate.Unlock()
	m.mu.Lock()
	current := m.conn == conn && m.managedRelay
	due := !time.Now().Before(m.relayRefreshAt)
	m.mu.Unlock()
	if !current || !due {
		return nil
	}
	frame, err := m.requestOn(conn, netproto.MsgRelayRequest, netproto.MsgRelayResponse, netproto.RelayRequest{Action: "refresh"}, 10*time.Second)
	if err != nil {
		m.mu.Lock()
		if m.conn == conn {
			m.relayRefreshAt = time.Now().Add(15 * time.Second)
		}
		m.mu.Unlock()
		return err
	}
	var lease netproto.RelayResponse
	if err := netproto.Decode(frame, &lease); err != nil {
		return err
	}
	if lease.Action != "refresh" || lease.Generation == 0 || lease.ExpiresAt <= lease.ServerTime || lease.ExpiresAt-lease.IssuedAt > int64(time.Hour/time.Millisecond) || lease.RefreshAt >= lease.ExpiresAt || len(lease.ICEServers) == 0 {
		return errors.New("invalid relay lease")
	}
	m.mu.Lock()
	if m.conn != conn || lease.SessionID != m.clientID || lease.Generation < m.relayLease.Generation {
		m.mu.Unlock()
		return errors.New("relay session changed")
	}
	changed := lease.Generation != m.relayLease.Generation
	m.relayLease = lease
	m.iceServers = lease.ICEServers
	// Relative server time tolerates client clock skew. Retry expired refresh
	// deadlines soon without a busy loop (server expiry is always authoritative).
	m.relayRefreshAt = time.Now().Add(max(5*time.Second, time.Duration(lease.RefreshAt-lease.ServerTime)*time.Millisecond))
	m.mu.Unlock()
	if changed {
		m.emit("relay_changed", "")
	}
	return nil
}
func (a *App) GetRelayLeaseForTab(tabID string) (netproto.RelayResponse, error) {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return netproto.RelayResponse{}, err
	}
	cm.mu.Lock()
	conn := cm.conn
	cm.mu.Unlock()
	if err := cm.refreshRelay(conn); err != nil {
		return netproto.RelayResponse{}, err
	}
	cm.mu.Lock()
	defer cm.mu.Unlock()
	lease := cm.relayLease
	lease.ICEServers = nil // credentials use existing ICE getter only
	return lease, nil
}
func (a *App) RevokeRelayForTab(tabID, sessionID string, allSessions bool) error {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return err
	}
	_, err = cm.request(netproto.MsgRelayRequest, netproto.MsgRelayResponse, netproto.RelayRequest{Action: "revoke", SessionID: sessionID, AllSessions: allSessions}, 10*time.Second)
	return err
}
