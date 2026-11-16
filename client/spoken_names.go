package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"time"

	"noxa/internal/netproto"
)

// A name clip is at most four seconds of 24 kHz PCM16 plus its header.
const maxSpokenNameBytes = 44 + 4*24000*2

// GetSpokenNameForTab fetches the spoken name of a session present on the
// tab's server. The first request for a name waits for its synthesis.
func (a *App) GetSpokenNameForTab(tabID, clientID, language string) (netproto.SpokenNameData, error) {
	var data netproto.SpokenNameData
	if language != "en" && language != "de" {
		return data, fmt.Errorf("unsupported language")
	}
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return data, err
	}
	cm.mu.Lock()
	supported, conn := cm.supportsSpokenNames, cm.conn
	cm.mu.Unlock()
	if !supported {
		return data, fmt.Errorf("server spoken names unavailable")
	}
	// Above the server's 15 s synthesis bound.
	frame, err := cm.requestOn(conn, netproto.MsgSpokenNameGet, netproto.MsgSpokenNameData,
		netproto.SpokenNameGet{ClientID: clientID, Language: language}, 20*time.Second)
	if err != nil {
		return data, err
	}
	if err := netproto.Decode(frame, &data); err != nil {
		return data, err
	}
	raw, err := base64.StdEncoding.DecodeString(data.DataBase64)
	if err != nil || len(raw) > maxSpokenNameBytes || !bytes.HasPrefix(raw, []byte("RIFF")) ||
		data.ClientID != clientID || data.Language != language || len(data.Key) != 64 {
		return netproto.SpokenNameData{}, fmt.Errorf("invalid spoken name")
	}
	return data, nil
}
