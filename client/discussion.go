package main

import (
	"errors"
	"time"

	"noxa/internal/netproto"
)

// DiscussionForTab binds requests and encryption to the originating server.
func (a *App) DiscussionForTab(tabID string, r netproto.DiscussionRequest) (netproto.DiscussionResult, error) {
	var result netproto.DiscussionResult
	if err := r.Validate(); err != nil {
		return result, err
	}
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return result, err
	}
	if r.Action == "create" || r.Action == "send" {
		id, key, ok := cm.scopeKeys.current(r.ChannelID)
		if !ok {
			return result, errors.New("no chat key for this channel yet")
		}
		body, sealErr := sealScope(r.Text, key)
		if sealErr != nil {
			return result, sealErr
		}
		r.BodyEnc, r.KeyID, r.Text = body, id, ""
	}
	frame, err := cm.request(netproto.MsgDiscussionRequest, netproto.MsgDiscussionResult, r, 10*time.Second)
	if err != nil {
		return result, err
	}
	if err = netproto.Decode(frame, &result); err != nil {
		return result, err
	}
	if result.ChannelID != r.ChannelID || result.Action != r.Action || (r.ThreadID > 0 && result.ThreadID != r.ThreadID) || len(result.Threads) > 50 || len(result.Messages) > 50 {
		return netproto.DiscussionResult{}, errors.New("invalid discussion response")
	}
	refused := installPageKeys(cm, r.ChannelID, result.Keys, result.Refused)
	for i := range result.Messages {
		openChatEntry(cm, r.ChannelID, &result.Messages[i], refused)
	}
	result.Keys = nil
	return result, nil
}
