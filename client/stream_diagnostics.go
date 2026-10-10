package main

import (
	"errors"
	"strconv"
	"time"

	"noxa/internal/netproto"
)

// SupportsStreamDiagnosticsForTab reports support on this authenticated tab.
func (a *App) SupportsStreamDiagnosticsForTab(tabID string) bool {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return false
	}
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.conn != nil && cm.clientID != "" && cm.supportsStreamDiagnostics && cm.authorizationModel == netproto.AuthorizationModelRolesV1
}

// StreamDiagnosticsForTab reads an eligible publication without starting a
// watch. Decimal lifetimes retain all uint64 bits across the JavaScript bridge.
func (a *App) StreamDiagnosticsForTab(tabID, publisherID, slot, generation, session string) (*netproto.VideoStreamDiagnostics, error) {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return nil, err
	}
	cm.mu.Lock()
	supported, model, conn, clientID := cm.supportsStreamDiagnostics, cm.authorizationModel, cm.conn, cm.clientID
	cm.mu.Unlock()
	if !supported || model != netproto.AuthorizationModelRolesV1 {
		return nil, errors.New("stream diagnostics are not supported by this server")
	}
	if conn == nil || clientID == "" {
		return nil, errors.New("not authenticated")
	}
	gen, genErr := strconv.ParseUint(generation, 10, 64)
	epoch, sessionErr := strconv.ParseUint(session, 10, 64)
	if publisherID == "" || len(publisherID) > 128 || (slot != "cam" && slot != "screen") || genErr != nil || gen == 0 || sessionErr != nil || epoch == 0 {
		return nil, errors.New("invalid stream diagnostics scope")
	}
	request := netproto.VideoStreamControl{Action: "diagnostics", PublisherID: publisherID, Slot: slot, Generation: gen, Session: epoch}
	frame, err := cm.requestOn(conn, netproto.MsgVideoStreamControl, netproto.MsgVideoStreamResult, request, 10*time.Second)
	if err != nil {
		return nil, err
	}
	cm.mu.Lock()
	current := cm.conn == conn && cm.clientID == clientID
	cm.mu.Unlock()
	if !current {
		return nil, errors.New("stream connection changed")
	}
	var result netproto.VideoStreamResult
	if len(frame.Payload) > netproto.MaxVoiceTelemetryBytes || netproto.Decode(frame, &result) != nil ||
		result.Action != request.Action || result.PublisherID != publisherID || result.Slot != slot || result.Generation != gen || result.Session != epoch {
		return nil, errors.New("invalid stream diagnostics acknowledgement")
	}
	details := result.Diagnostics
	if details == nil || details.PublisherID != publisherID || details.Slot != slot || details.Generation != gen || details.Session != epoch || len(details.Layers) > 4 {
		return nil, errors.New("stream diagnostics unavailable for this publication")
	}
	if details.SenderReport != nil {
		if len(details.SenderReport.Rows) > 8 {
			return nil, errors.New("invalid stream sender diagnostics")
		}
		for _, row := range details.SenderReport.Rows {
			if !row.Valid() || row.Generation != gen || row.Slot != slot {
				return nil, errors.New("invalid stream sender diagnostics")
			}
		}
	}
	return details, nil
}

func isStreamDiagnosticsRead(kind netproto.MessageType, message any) bool {
	request, ok := message.(netproto.VideoStreamControl)
	return kind == netproto.MsgVideoStreamControl && ok && request.Action == "diagnostics"
}
