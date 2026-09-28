package main

import (
	"errors"
	"noxa/internal/netproto"
	"time"
)

func (a *App) WebhookForTab(tabID string, request netproto.WebhookRequest) (netproto.WebhookResult, error) {
	var result netproto.WebhookResult
	if !request.Valid() {
		return result, errors.New("invalid webhook request")
	}
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return result, err
	}
	frame, err := cm.request(netproto.MsgWebhookRequest, netproto.MsgWebhookResult, request, 10*time.Second)
	if err != nil {
		return result, err
	}
	if err = netproto.Decode(frame, &result); err != nil {
		return result, err
	}
	if result.Action != request.Action || result.ChannelID != request.ChannelID || len(result.Hooks) > 20 || (request.Action != "create" && result.Token != "") {
		return netproto.WebhookResult{}, errors.New("invalid webhook response")
	}
	return result, nil
}
