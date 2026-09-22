package main

import (
	"errors"
	"time"

	"noxa/internal/netproto"
)

// SetMediaLimitsForTab replaces video publishing ceilings on the displayed
// server. The server requires ManageServer (or legacy administrator access).
func (a *App) SetMediaLimitsForTab(tabID string, limits netproto.MediaLimits) (netproto.MediaLimitsSaved, error) {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return netproto.MediaLimitsSaved{}, err
	}
	return cm.setMediaLimits(limits)
}

func (m *connManager) setMediaLimits(limits netproto.MediaLimits) (netproto.MediaLimitsSaved, error) {
	if !limits.Valid() {
		return netproto.MediaLimitsSaved{}, errors.New("invalid media limits")
	}
	f, err := m.request(netproto.MsgMediaLimitsSet, netproto.MsgMediaLimitsSaved, limits, 10*time.Second)
	if err != nil {
		return netproto.MediaLimitsSaved{}, err
	}
	if !hasCompleteMediaLimitFields(f.Payload) {
		return netproto.MediaLimitsSaved{}, errors.New("incomplete media limits acknowledgement")
	}
	var saved netproto.MediaLimitsSaved
	if err := netproto.Decode(f, &saved); err != nil {
		return netproto.MediaLimitsSaved{}, err
	}
	if !saved.Valid() {
		return netproto.MediaLimitsSaved{}, errors.New("invalid media limits acknowledgement")
	}
	return saved, nil
}
