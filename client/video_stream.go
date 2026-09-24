package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"noxa/internal/netproto"
	"time"
)

// VideoStreamControlForTab ties acknowledged stream operations to their server.
func (a *App) VideoStreamControlForTab(tabID string, msg netproto.VideoStreamControl) (netproto.VideoStreamResult, error) {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return netproto.VideoStreamResult{}, err
	}
	if msg.Action == "publish" || msg.Action == "preview_upload" {
		msg.PublisherID = cm.clientIDSnapshot()
	}
	frame, err := cm.request(netproto.MsgVideoStreamControl, netproto.MsgVideoStreamResult, msg, 10*time.Second)
	if err != nil {
		return netproto.VideoStreamResult{}, err
	}
	var result netproto.VideoStreamResult
	var fields map[string]json.RawMessage
	if err := netproto.Decode(frame, &fields); err != nil {
		return result, err
	}
	for _, key := range []string{"action", "publisher_id", "slot", "generation", "revision", "session", "active", "streams", "preview_at"} {
		value := bytes.TrimSpace(fields[key])
		if len(value) == 0 || bytes.Equal(value, []byte("null")) {
			return result, errors.New("incomplete video stream acknowledgement")
		}
	}
	if err := netproto.Decode(frame, &result); err != nil {
		return result, err
	}
	newPublication := msg.Action == "publish" && msg.Active && msg.Generation == 0
	if result.Action != msg.Action || result.PublisherID != msg.PublisherID || result.Slot != msg.Slot || result.Revision != msg.Revision || result.Active != msg.Active || result.Streams == nil ||
		(msg.Action == "list" && result.Session == 0) || (msg.Action != "list" && result.Session != msg.Session) ||
		(msg.Action == "publish" && msg.Active && result.Generation == 0) ||
		(!newPublication && result.Generation != msg.Generation) {
		return netproto.VideoStreamResult{}, errors.New("video stream acknowledgement does not match request")
	}
	for _, stream := range result.Streams {
		if stream.PublisherID == "" || stream.Generation == 0 || (stream.Slot != "cam" && stream.Slot != "screen") {
			return netproto.VideoStreamResult{}, errors.New("invalid video stream catalog")
		}
	}
	return result, nil
}
