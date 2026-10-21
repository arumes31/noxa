package main

import (
	"errors"
	"noxa/internal/netproto"
	"time"
)

func (a *App) ChannelInviteForTab(tabID string, request netproto.ChannelInviteRequest) (netproto.ChannelInviteResult, error) {
	var result netproto.ChannelInviteResult
	if !request.Valid() {
		return result, errors.New("invalid channel invitation")
	}
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return result, err
	}
	frame, err := cm.request(netproto.MsgChannelInviteRequest, netproto.MsgChannelInviteResult, request, 10*time.Second)
	if err != nil {
		return result, err
	}
	if err = netproto.Decode(frame, &result); err != nil {
		return result, err
	}
	if result.Action != request.Action || result.ChannelID != request.ChannelID || (result.Active && result.Access != "guest" && result.Access != "member") || (request.Action == "regenerate" && (!result.Active || !netproto.ValidInviteToken(result.Token))) || (request.Action != "regenerate" && result.Token != "") {
		return netproto.ChannelInviteResult{}, errors.New("invalid channel invitation response")
	}
	return result, nil
}
