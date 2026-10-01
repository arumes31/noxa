package main

import (
	"errors"
	"slices"
	"time"

	"noxa/internal/netproto"
)

func isPollRead(kind netproto.MessageType, body any) bool {
	request, ok := body.(netproto.PollRequest)
	return ok && kind == netproto.MsgPollRequest && request.Action == "get"
}

func (a *App) CreatePollForTab(tabID, scope, target string, poll netproto.PollDefinition) string {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return err.Error()
	}
	if scope != "channel" && scope != "global" {
		return "polls require a channel or global chat"
	}
	if err := poll.Validate(time.Now()); err != nil {
		return err.Error()
	}
	body, err := netproto.EncodePoll(poll)
	if err != nil {
		return err.Error()
	}
	return sendChatWith(cm, scope, target, body, 0)
}

func (a *App) PollForTab(tabID string, msg netproto.PollRequest) (netproto.PollState, error) {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return netproto.PollState{}, err
	}
	frame, err := cm.request(netproto.MsgPollRequest, netproto.MsgPollState, msg, 10*time.Second)
	if err != nil {
		return netproto.PollState{}, err
	}
	var result netproto.PollState
	if err := netproto.Decode(frame, &result); err != nil {
		return result, err
	}
	if result.MessageID != msg.MessageID || result.Action != msg.Action || result.Version == 0 || len(result.Counts) < 2 || len(result.Counts) > 10 || result.Choices == nil || result.TotalVoters < 0 {
		return netproto.PollState{}, errors.New("invalid poll response")
	}
	for _, count := range result.Counts {
		if count < 0 || count > result.TotalVoters {
			return netproto.PollState{}, errors.New("invalid poll counts")
		}
	}
	choices := slices.Clone(result.Choices)
	slices.Sort(choices)
	for i, choice := range choices {
		if choice < 0 || choice >= len(result.Counts) || (i > 0 && choices[i-1] == choice) {
			return netproto.PollState{}, errors.New("invalid poll ballot")
		}
	}
	if msg.Action == "close" && !result.Closed {
		return netproto.PollState{}, errors.New("poll closure not acknowledged")
	}
	if msg.Action == "vote" {
		want := slices.Clone(msg.Choices)
		slices.Sort(want)
		if !slices.Equal(want, choices) {
			return netproto.PollState{}, errors.New("poll ballot not acknowledged")
		}
	}
	return result, nil
}
