package main

// media_roster.go: the native view of the channel roster that media keys may
// be sent to (review task 4 P3-4).
//
// The frontend chooses media-key recipients from the roster it renders
// (media-keys.js recipientsOf: same channel, visible, announcing a media
// device). A compromised page could otherwise ask SendMediaKeyForTab to seal
// controls to any trusted device. The connection therefore keeps the same
// roster from the same frames (snapshots plus the legacy membership events,
// applied before they reach the page) and refuses a target that is not a
// current media device of the sender's own voice channel.

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"

	"noxa/internal/broadcast"
)

// mediaRosterEntry is what media-key targeting needs of one session.
type mediaRosterEntry struct {
	uniqueID string
	device   string
	status   string
	channel  int64
}

// mediaRosterLastChannelMax bounds the remembered channels of sessions that
// left (an invisible→visible return is announced as leave + join).
const mediaRosterLastChannelMax = 256

// noteRosterSnapshot replaces the roster from an authoritative snapshot.
func (m *connManager) noteRosterSnapshot(payload []byte) {
	var snap broadcast.TreeSnapshot
	if json.Unmarshal(payload, &snap) != nil {
		return
	}
	roster := map[string]mediaRosterEntry{}
	add := func(c *broadcast.ClientInfo) {
		if c != nil && c.ClientID != "" {
			roster[c.ClientID] = mediaRosterEntry{uniqueID: c.UniqueID, device: c.MediaDevice, status: c.Status, channel: c.ChannelID}
		}
	}
	var visit func([]*broadcast.ChannelNode)
	visit = func(nodes []*broadcast.ChannelNode) {
		for _, node := range nodes {
			if node == nil {
				continue
			}
			for _, c := range node.Clients {
				add(c)
			}
			visit(node.Children)
		}
	}
	visit(snap.RootChannels)
	for _, c := range snap.UnassignedClients {
		add(c)
	}
	m.mu.Lock()
	m.mediaRoster = roster
	m.mediaRosterLast = nil
	m.mu.Unlock()
}

// noteRosterEvent applies a membership event (non-role servers) the way the
// page applies it.
func (m *connManager) noteRosterEvent(payload string) {
	var env struct {
		Type string `json:"type"`
		Data struct {
			ClientID    string  `json:"client_id"`
			UniqueID    *string `json:"unique_id"`
			ChannelID   *int64  `json:"channel_id"`
			Status      string  `json:"status"`
			MediaDevice string  `json:"media_device"`
		} `json:"data"`
	}
	switch {
	case json.Unmarshal([]byte(payload), &env) != nil, env.Data.ClientID == "":
		return
	}
	d := env.Data
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.mediaRoster == nil {
		m.mediaRoster = map[string]mediaRosterEntry{}
	}
	entry, existing := m.mediaRoster[d.ClientID]
	switch env.Type {
	case "user_joined":
		channel := entry.channel
		if last, ok := m.mediaRosterLast[d.ClientID]; ok {
			channel = last
			delete(m.mediaRosterLast, d.ClientID)
		}
		if d.ChannelID != nil {
			channel = *d.ChannelID
		}
		if d.UniqueID != nil {
			entry.uniqueID = *d.UniqueID
		}
		entry.channel = channel
		m.mediaRoster[d.ClientID] = entry
	case "user_left":
		if existing {
			if entry.channel != 0 {
				if m.mediaRosterLast == nil {
					m.mediaRosterLast = map[string]int64{}
				}
				if len(m.mediaRosterLast) >= mediaRosterLastChannelMax {
					clear(m.mediaRosterLast)
				}
				m.mediaRosterLast[d.ClientID] = entry.channel
			}
			delete(m.mediaRoster, d.ClientID)
		}
	case "user_moved":
		if existing {
			entry.channel = 0
			if d.ChannelID != nil {
				entry.channel = *d.ChannelID
			}
			m.mediaRoster[d.ClientID] = entry
		}
	case "status_changed":
		if existing {
			entry.status = d.Status
			m.mediaRoster[d.ClientID] = entry
		}
	case "media_device_changed":
		if existing {
			entry.device = d.MediaDevice
			m.mediaRoster[d.ClientID] = entry
		}
	}
}

var (
	errMediaKeyScope  = errors.New("media key scope is not your current voice channel")
	errMediaKeyTarget = errors.New("media key target is not a media device in your voice channel")
)

// checkMediaKeyTarget allows a media-key control only for the sender's own
// current voice channel and only to a visible session in it that announces
// exactly that (principal, media device); never to this session itself.
func (m *connManager) checkMediaKeyTarget(req MediaKeyRequest) error {
	if req.Scope.Type != "channel-media" {
		return errMediaKeyScope
	}
	channel, err := strconv.ParseInt(req.Scope.ID, 10, 64)
	if err != nil || channel <= 0 || strconv.FormatInt(channel, 10) != req.Scope.ID {
		return errMediaKeyScope
	}
	if req.Principal == "" || req.Device == "" {
		return errMediaKeyTarget
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	self, ok := m.mediaRoster[m.clientID]
	if m.clientID == "" || !ok || self.channel != channel {
		return errMediaKeyScope
	}
	for clientID, e := range m.mediaRoster {
		if clientID != m.clientID && e.channel == channel && e.status != "invisible" &&
			e.uniqueID == req.Principal && e.device == req.Device && req.Device != m.deviceID {
			return nil
		}
	}
	return errMediaKeyTarget
}

// isRosterEvent cheaply pre-filters the membership events noteRosterEvent
// applies (every other event skips the second decode).
func isRosterEvent(payload []byte) bool {
	for _, kind := range rosterEventTypes {
		if bytes.Contains(payload, kind) {
			return true
		}
	}
	return false
}

var rosterEventTypes = [][]byte{[]byte(`"user_joined"`), []byte(`"user_left"`), []byte(`"user_moved"`), []byte(`"status_changed"`), []byte(`"media_device_changed"`)}
