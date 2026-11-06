package main

import (
	"time"

	"noxa/internal/netproto"
)

// resumeProbeTimeout bounds how long a connection may stay silent after the
// computer wakes. A live server answers the probe ping within one round trip.
const resumeProbeTimeout = 5 * time.Second

// probeLiveness sends a ping and shortens the read deadline to timeout. A live
// server's pong (or any frame) arrives first, and the read loop restores its
// normal deadline; a transport that died during sleep fails within timeout
// instead of the regular 45-second read timeout, so reconnection starts early.
func (m *connManager) probeLiveness(timeout time.Duration) bool {
	if timeout <= 0 {
		return false
	}
	// deadlineMu orders this update with the read loop's, so an older read
	// deadline cannot replace the probe's shorter one.
	m.deadlineMu.Lock()
	m.mu.Lock()
	conn := m.conn
	if conn == nil {
		m.mu.Unlock()
		m.deadlineMu.Unlock()
		return false
	}
	until := time.Now().Add(timeout)
	m.probeUntil = until
	m.mu.Unlock()
	// The read loop also applies probeUntil before its next read; this covers
	// a read that is already blocked.
	err := conn.SetReadDeadline(until)
	m.deadlineMu.Unlock()
	if err != nil {
		return false
	}
	// A failed write already terminates the connection through writeConn.
	_ = m.writeConn(conn, netproto.MsgPing, netproto.Ping{})
	return true
}

// ProbeConnections checks every open server tab after the computer resumes
// from sleep. It returns how many connections were probed.
func (a *App) ProbeConnections() int {
	a.tabsMu.Lock()
	managers := make([]*connManager, 0, len(a.tabs))
	for _, ts := range a.tabs {
		if ts != nil && ts.cm != nil {
			managers = append(managers, ts.cm)
		}
	}
	a.tabsMu.Unlock()
	probed := 0
	for _, cm := range managers {
		if cm.probeLiveness(resumeProbeTimeout) {
			probed++
		}
	}
	return probed
}

// nextReadDeadline is the read loop's deadline: the regular read timeout, or
// an earlier pending liveness probe. Zero means no deadline.
func (m *connManager) nextReadDeadline() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	var deadline time.Time
	if m.readTimeout > 0 {
		deadline = time.Now().Add(m.readTimeout)
	}
	if !m.probeUntil.IsZero() && (deadline.IsZero() || m.probeUntil.Before(deadline)) {
		deadline = m.probeUntil
	}
	return deadline
}

// clearProbe ends a pending liveness probe once the server has sent a frame.
func (m *connManager) clearProbe() {
	m.mu.Lock()
	m.probeUntil = time.Time{}
	m.mu.Unlock()
}
