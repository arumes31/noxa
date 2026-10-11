package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"noxa/internal/netproto"
	"slices"
	"time"
)

// VideoStreamControlForTab ties acknowledged stream operations to their server.
func (a *App) VideoStreamControlForTab(tabID string, msg netproto.VideoStreamControl) (netproto.VideoStreamResult, error) {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return netproto.VideoStreamResult{}, err
	}
	cm.mu.Lock()
	conn, clientID, sourceSupported, codecSupported, model := cm.conn, cm.clientID, cm.supportsStreamSourceQuality, cm.supportsStreamCodec, cm.authorizationModel
	recoverySupported := cm.supportsStreamReceiverRecovery
	codecRefreshSupported := cm.supportsStreamCodecRefresh
	cm.mu.Unlock()
	if conn == nil {
		return netproto.VideoStreamResult{}, errors.New("not connected")
	}
	if msg.Action == "receiver_reset" && (!recoverySupported || model != netproto.AuthorizationModelRolesV1 || clientID == "" ||
		msg.PublisherID == "" || (msg.Slot != "cam" && msg.Slot != "screen") || msg.Generation == 0 || msg.Session == 0 || msg.Revision == 0 || !msg.Active || len(msg.JPEG) != 0) {
		return netproto.VideoStreamResult{}, errors.New("invalid or unsupported receiver recovery")
	}
	if (msg.QualityMode != "" && msg.QualityMode != "source") || (msg.QualityMode != "" && msg.Action != "publish") {
		return netproto.VideoStreamResult{}, errors.New("invalid publication quality mode")
	}
	if msg.QualityMode == "source" && (!sourceSupported || model != netproto.AuthorizationModelRolesV1 || clientID == "") {
		return netproto.VideoStreamResult{}, errors.New("source quality publications are not supported by this server")
	}
	if len(msg.Codecs) != 0 {
		if msg.Action == "publish" && msg.Active && msg.Generation != 0 && (!codecRefreshSupported || msg.QualityMode != "source") {
			return netproto.VideoStreamResult{}, errors.New("in-place video codec refresh is not supported")
		}
		if !codecSupported || model != netproto.AuthorizationModelRolesV1 || clientID == "" ||
			(msg.Action != "publish" && msg.Action != "watch") || len(msg.Codecs) > 2 || !slices.Contains(msg.Codecs, "vp8") ||
			(len(msg.Codecs) == 2 && !slices.Contains(msg.Codecs, "h264")) {
			return netproto.VideoStreamResult{}, errors.New("invalid or unsupported video codec capabilities")
		}
	}
	if msg.Action == "publish" || msg.Action == "preview_upload" {
		msg.PublisherID = clientID
	}
	frame, err := cm.requestOn(conn, netproto.MsgVideoStreamControl, netproto.MsgVideoStreamResult, msg, 10*time.Second)
	if err != nil {
		return netproto.VideoStreamResult{}, err
	}
	cm.mu.Lock()
	current := cm.conn == conn && cm.clientID == clientID
	cm.mu.Unlock()
	if !current {
		return netproto.VideoStreamResult{}, errors.New("stream connection changed")
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
	if result.QualityMode != "" && result.QualityMode != "source" ||
		(msg.Action == "publish" && msg.Active && msg.QualityMode == "source" && (result.QualityMode != "source" || result.UploadActive == nil)) {
		return netproto.VideoStreamResult{}, errors.New("invalid source quality acknowledgement")
	}
	if (result.Codec != "" && (msg.Action != "publish" || (result.Codec != "vp8" && result.Codec != "h264"))) ||
		(msg.Action == "publish" && msg.Active && len(msg.Codecs) != 0 && !slices.Contains(msg.Codecs, result.Codec)) {
		return netproto.VideoStreamResult{}, errors.New("invalid publication codec acknowledgement")
	}
	for _, stream := range result.Streams {
		if stream.PublisherID == "" || stream.Generation == 0 || (stream.Slot != "cam" && stream.Slot != "screen") ||
			(stream.QualityMode != "" && stream.QualityMode != "source") || (stream.UploadActive != nil && stream.PublisherID != clientID) ||
			(stream.Codec != "" && (stream.PublisherID != clientID || (stream.Codec != "vp8" && stream.Codec != "h264"))) {
			return netproto.VideoStreamResult{}, errors.New("invalid video stream catalog")
		}
	}
	return result, nil
}
