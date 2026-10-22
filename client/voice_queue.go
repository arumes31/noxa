package main

import (
	"fmt"
	"noxa/internal/netproto"
	"time"
)

func (a *App) VoiceQueueForTab(tabID string, request netproto.VoiceQueueRequest) (netproto.VoiceQueueResult, error) {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return netproto.VoiceQueueResult{}, err
	}
	f, err := cm.request(netproto.MsgVoiceQueueRequest, netproto.MsgVoiceQueueResult, request, 10*time.Second)
	if err != nil {
		return netproto.VoiceQueueResult{}, err
	}
	var result netproto.VoiceQueueResult
	if err := netproto.Decode(f, &result); err != nil {
		return result, err
	}
	if result.ChannelID != request.ChannelID || result.Action != request.Action {
		return result, fmt.Errorf("voice queue acknowledgement mismatch")
	}
	return result, nil
}
