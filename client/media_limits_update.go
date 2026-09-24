package main

import (
	"bytes"
	"encoding/json"
	"errors"

	"noxa/internal/netproto"
)

func decodeMediaLimitsChanged(f *netproto.Frame) (netproto.MediaLimitsChanged, error) {
	if !hasCompleteMediaLimitFields(f.Payload) {
		return netproto.MediaLimitsChanged{}, errors.New("incomplete media limits update")
	}
	var update netproto.MediaLimitsChanged
	if err := netproto.Decode(f, &update); err != nil {
		return netproto.MediaLimitsChanged{}, err
	}
	if update.Revision == 0 || !update.Valid() {
		return netproto.MediaLimitsChanged{}, errors.New("invalid media limits update")
	}
	return update, nil
}

func hasCompleteAuthMediaLimits(f *netproto.Frame) bool {
	var envelope struct {
		Limits json.RawMessage `json:"media_limits"`
	}
	return netproto.Decode(f, &envelope) == nil && hasCompleteMediaLimitFields(envelope.Limits)
}

func hasCompleteMediaLimitFields(payload []byte) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(payload, &fields) != nil {
		return false
	}
	for _, key := range []string{"video_max_bitrate", "video_max_width", "video_max_height"} {
		value := bytes.TrimSpace(fields[key])
		if len(value) == 0 || bytes.Equal(value, []byte("null")) {
			return false
		}
	}
	return true
}
