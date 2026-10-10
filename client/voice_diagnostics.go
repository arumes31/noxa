package main

import (
	"errors"

	"noxa/internal/netproto"
)

// ReportVoiceDiagnosticsForTab sends only this voice session's measurements,
// and only after capability negotiation. It cannot retarget a replaced socket.
func (a *App) ReportVoiceDiagnosticsForTab(tabID string, report netproto.VoiceTelemetry) error {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return err
	}
	cm.mu.Lock()
	supported, conn := cm.supportsVoiceTelemetry, cm.conn
	cm.mu.Unlock()
	if !supported {
		return nil
	}
	if conn == nil {
		return errors.New("not connected")
	}
	report.ClientVersion = a.ClientVersionShort()
	if !report.Valid() {
		return errors.New("invalid voice diagnostics")
	}
	// Escaped JSON can exceed the budget even with 64 valid tracks. Keep a
	// bounded prefix without changing the caller's slice or measurements.
	for {
		frame, err := netproto.Encode(netproto.MsgVoiceTelemetry, report)
		if err != nil {
			return err
		}
		if len(frame.Payload) <= netproto.MaxVoiceTelemetryBytes {
			break
		}
		if len(report.Tracks) == 0 {
			return errors.New("voice diagnostics exceed payload limit")
		}
		report.Tracks = report.Tracks[:len(report.Tracks)-1]
		report.Truncated = true
	}
	return cm.writeConn(conn, netproto.MsgVoiceTelemetry, report)
}
