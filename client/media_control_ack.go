package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"noxa/internal/netproto"
)

func (cm *connManager) mediaControl(want netproto.MediaControlSaved) string {
	ack := cm.usesRoleAuthorization()
	var msg any
	switch want.Operation {
	case netproto.MsgPrioritySpeaker:
		msg = netproto.PrioritySpeaker{Active: want.Active, AckRequested: ack}
	case netproto.MsgWhisperSet:
		msg = netproto.WhisperSet{UniqueIDs: want.UniqueIDs, ChannelIDs: want.ChannelIDs, Active: want.Active, AckRequested: ack}
	case netproto.MsgScreenShare:
		msg = netproto.ScreenShare{Active: want.Active, MaxHeight: want.MaxHeight, AckRequested: ack}
	case netproto.MsgVideoQuality:
		msg = netproto.VideoQuality{Quality: want.Quality, AckRequested: ack}
	default:
		return "unsupported media control"
	}
	if !ack {
		if err := cm.write(want.Operation, msg); err != nil {
			return err.Error()
		}
		return ""
	}
	want.ClientID = cm.clientIDSnapshot()
	if want.ClientID == "" {
		return "not authenticated"
	}
	f, err := cm.request(want.Operation, netproto.MsgMediaControlSaved, msg, 10*time.Second)
	if err != nil {
		return err.Error()
	}
	if err := validateMediaControlReply(f, want); err != nil {
		return err.Error()
	}
	return ""
}

func validateMediaControlReply(f *netproto.Frame, want netproto.MediaControlSaved) error {
	var fields map[string]json.RawMessage
	if err := netproto.Decode(f, &fields); err != nil {
		return err
	}
	for _, key := range []string{"operation", "client_id", "active", "max_height", "quality", "unique_ids", "channel_ids"} {
		value := bytes.TrimSpace(fields[key])
		if len(value) == 0 || bytes.Equal(value, []byte("null")) {
			return errors.New("incomplete media control acknowledgement")
		}
	}
	var saved netproto.MediaControlSaved
	if err := netproto.Decode(f, &saved); err != nil {
		return err
	}
	if saved.Operation != want.Operation || saved.ClientID != want.ClientID || saved.Active != want.Active ||
		saved.MaxHeight != want.MaxHeight || saved.Quality != want.Quality ||
		!slices.Equal(saved.UniqueIDs, want.UniqueIDs) || !slices.Equal(saved.ChannelIDs, want.ChannelIDs) {
		return errors.New("media control acknowledgement does not match the request")
	}
	return nil
}
