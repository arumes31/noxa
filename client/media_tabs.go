package main

import "noxa/internal/netproto"

// SetPrioritySpeakerForTab changes priority on the initiating connection.
func (a *App) SetPrioritySpeakerForTab(tabID string, active bool) string {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return err.Error()
	}
	return cm.mediaControl(netproto.MediaControlSaved{Operation: netproto.MsgPrioritySpeaker, Active: active})
}

// WhisperSetForTab configures whisper routing on the initiating connection.
func (a *App) WhisperSetForTab(tabID string, uniqueIDs []string, channelIDs []int64, active bool) string {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return err.Error()
	}
	return cm.mediaControl(netproto.MediaControlSaved{Operation: netproto.MsgWhisperSet, UniqueIDs: uniqueIDs, ChannelIDs: channelIDs, Active: active})
}

// SetScreenShareForTab stops or declares sharing on the initiating connection.
func (a *App) SetScreenShareForTab(tabID string, active bool) string {
	return a.SetScreenShareQualityForTab(tabID, active, 0)
}

// SetScreenShareQualityForTab declares sharing and capture height for that tab.
func (a *App) SetScreenShareQualityForTab(tabID string, active bool, maxHeight int) string {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return err.Error()
	}
	return cm.mediaControl(netproto.MediaControlSaved{Operation: netproto.MsgScreenShare, Active: active, MaxHeight: maxHeight})
}

// SetVideoQualityForTab requests the received video layer for that tab.
func (a *App) SetVideoQualityForTab(tabID, quality string) string {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return err.Error()
	}
	return cm.mediaControl(netproto.MediaControlSaved{Operation: netproto.MsgVideoQuality, Quality: quality})
}

// GetICEServersForTab reads ICE configuration from the displayed server only.
func (a *App) GetICEServersForTab(tabID string) ([]netproto.ICEServer, error) {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return nil, err
	}
	return cm.iceServersSnapshot(), nil
}

// GetMediaLimitsForTab reads publishing limits from the displayed server only.
func (a *App) GetMediaLimitsForTab(tabID string) (netproto.MediaLimits, error) {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return netproto.MediaLimits{}, err
	}
	return cm.mediaLimitsSnapshot(), nil
}

// WebRTCOfferForTab keeps a negotiation on its originating control connection.
func (a *App) WebRTCOfferForTab(tabID, sdp string, tracks []netproto.TrackSlot) (string, error) {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return "", err
	}
	return cm.webRTCOffer(sdp, tracks)
}

// WebRTCAnswerForTab rejects answers belonging to another server's peer.
func (a *App) WebRTCAnswerForTab(tabID, sdp string) error {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return err
	}
	return cm.write(netproto.MsgWebRTCAnswer, netproto.WebRTCAnswer{SDP: sdp})
}

// SendICECandidateForTab rejects candidates belonging to another server's peer.
func (a *App) SendICECandidateForTab(tabID, candidate, sdpMid string, sdpMLineIndex uint16) error {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return err
	}
	return cm.write(netproto.MsgICECandidate, netproto.ICECandidate{
		Candidate: candidate, SDPMid: sdpMid, SDPMLineIndex: sdpMLineIndex,
	})
}
