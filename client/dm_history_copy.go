// dm_history_copy.go copies this desktop's local direct-message history to a
// newly approved device (dm_devices_v1). The history is re-encrypted to the
// new device's X25519 key in chunks the server stores for 24 hours; the
// server never sees a message.
package main

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/nacl/box"

	"noxa/internal/netproto"
)

// historySealOverhead is what sealing adds to a chunk's JSON: the nonce and
// the box authenticator.
const historySealOverhead = 24 + box.Overhead

// historyCopyProgress is the "device_history" event payload.
type historyCopyProgress struct {
	DeviceID string `json:"device_id"`
	State    string `json:"state"` // progress, done, failed
	Sent     int    `json:"sent"`
	Total    int    `json:"total"`
	Messages int    `json:"messages"`
	Error    string `json:"error,omitempty"`
}

// sealHistoryChunk seals one chunk's JSON as raw nonce||box (HistoryChunk.Data).
func sealHistoryChunk(plain []byte, recipientPub, senderPriv [32]byte) ([]byte, error) {
	var nonce [24]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	return box.Seal(nonce[:], plain, &nonce, &recipientPub, &senderPriv), nil
}

// historyMessagesSince collects the stored conversations' messages sent at or
// after since (zero: everything), newest first, capped at MaxHistoryMessages.
func historyMessagesSince(store dmHistoryStore, since int64) []netproto.HistoryMessage {
	var out []netproto.HistoryMessage
	for _, peer := range store.peers() {
		entries, err := store.messages(peer.UniqueID)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.SentAt < since || !openedChatText(e.Body) {
				continue
			}
			m := netproto.HistoryMessage{
				Peer: peer.UniqueID, PeerNickname: peer.Nickname, From: e.FromUniqueID, FromNickname: e.FromNickname,
				Body: e.Body, SentAt: e.SentAt, Self: e.Self, ClientMsgID: e.ClientMsgID,
			}
			if e.Protocol != "" {
				// Only readable, authenticated archive records travel (keyed by
				// the sender's logical ID); never ratchet state, keys or trust.
				if !transferableRatchetEntry(e) {
					continue
				}
				m.Protocol, m.Provenance = e.Protocol, netproto.HistoryProvenanceAuthenticated
			}
			out = append(out, m)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].SentAt > out[j].SentAt })
	if len(out) > netproto.MaxHistoryMessages {
		out = out[:netproto.MaxHistoryMessages]
	}
	return out
}

// transferableRatchetEntry: a readable record of the authenticated archive,
// i.e. received, or an own message accepted by the server (a linked copy of
// an own send carries no status: it was accepted on the sending device).
// Pending, uncertain or failed sends, deleted records and placeholders stay.
func transferableRatchetEntry(e DMEntry) bool {
	if e.Protocol != ratchetProtocol || e.Deleted || !e.EncVerified || e.ClientMsgID == "" || e.FromUniqueID == "" || strings.TrimSpace(e.Body) == "" {
		return false
	}
	switch e.Status {
	case "received", "accepted":
		return true
	case "":
		return e.Self
	}
	return false
}

// buildHistoryChunks packs messages (newest first) into sealed chunks, each
// at most MaxHistoryChunk bytes sealed, at most MaxHistoryChunks chunks and
// MaxHistoryBytes in all; the oldest messages are left out when the limits
// are reached. Chunks come out oldest first, each holding its messages
// oldest first. It returns the chunks' sealed data and the messages kept.
func buildHistoryChunks(messages []netproto.HistoryMessage, recipientPub, senderPriv [32]byte) ([][]byte, int, error) {
	const maxPlain = netproto.MaxHistoryChunk - historySealOverhead
	var groups [][]json.RawMessage
	var current []json.RawMessage
	size, total, kept := 2, 0, 0 // "[]"
	flush := func() {
		if len(current) > 0 {
			groups = append(groups, current)
			total += size + historySealOverhead
		}
		current, size = nil, 2
	}
	for _, msg := range messages {
		raw, err := json.Marshal(msg)
		if err != nil {
			return nil, 0, err
		}
		if len(raw)+2 > maxPlain {
			continue // one message larger than a chunk; never the case for chat
		}
		add := len(raw)
		if len(current) > 0 {
			add++ // comma
		}
		if size+add > maxPlain {
			flush()
			add = len(raw)
		}
		if len(current) == 0 && len(groups) >= netproto.MaxHistoryChunks {
			break
		}
		if total+size+add+historySealOverhead > netproto.MaxHistoryBytes {
			break
		}
		current = append(current, raw)
		size += add
		kept++
	}
	flush()
	chunks := make([][]byte, 0, len(groups))
	for i := len(groups) - 1; i >= 0; i-- {
		group := groups[i]
		ordered := make([]json.RawMessage, len(group))
		for j := range group {
			ordered[len(group)-1-j] = group[j]
		}
		plain, err := json.Marshal(ordered)
		if err != nil {
			return nil, 0, err
		}
		sealed, err := sealHistoryChunk(plain, recipientPub, senderPriv)
		if err != nil {
			return nil, 0, err
		}
		chunks = append(chunks, sealed)
	}
	return chunks, kept, nil
}

// CopyHistoryToDeviceForTab copies the local direct-message history of the
// last days days (0: nothing, -1: everything) to a device of this account in
// the background. The device must be a verified device of the account (just
// approved by this desktop); progress arrives as "device_history" events.
func (a *App) CopyHistoryToDeviceForTab(tabID, deviceID string, days int) error {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return err
	}
	if days < -1 {
		return errors.New("invalid history range")
	}
	if days == 0 {
		return nil
	}
	cm.mu.Lock()
	ownUID, selfDevice := cm.uniqueID, cm.deviceID
	cm.mu.Unlock()
	if !cm.dmDevicesActive() {
		return errors.New("this server cannot copy history to other devices yet")
	}
	if deviceID == "" || deviceID == selfDevice {
		return errors.New("choose another device")
	}
	dir, err := a.dmHistoryDir()
	if err != nil {
		return err
	}
	id, err := cm.identity()
	if err != nil {
		return err
	}
	store, err := dmHistoryStoreForIdentity(dir, id)
	if err != nil {
		return err
	}
	_, priv, err := id.x25519()
	if err != nil {
		return err
	}
	// The new device's key comes from the verified device list, never from
	// the request: the copy is readable only by a device this account trusts.
	devices, err := cm.dmDeviceList(ownUID, true)
	if err != nil {
		return err
	}
	var target *dmTrustedDevice
	for i := range devices {
		if devices[i].DeviceID == deviceID {
			target = &devices[i]
		}
	}
	if target == nil {
		return errors.New("this device is not confirmed for direct messages")
	}
	recipient := target.X25519
	since := int64(0)
	if days > 0 {
		since = time.Now().Add(-time.Duration(days) * 24 * time.Hour).Unix()
	}
	go guardCrash("history copy", func() {
		progress := historyCopyProgress{DeviceID: deviceID}
		fail := func(err error) {
			log.Printf("history copy to device %s failed: %v", deviceID, err)
			progress.State, progress.Error = "failed", err.Error()
			cm.emit("device_history", progress)
		}
		chunks, kept, err := buildHistoryChunks(historyMessagesSince(store, since), recipient, priv)
		if err != nil {
			fail(err)
			return
		}
		progress.Total, progress.Messages = len(chunks), kept
		for seq, data := range chunks {
			_, err := cm.deviceRequest(netproto.DeviceRequest{
				Action: netproto.DeviceActionHistoryPut, DeviceID: deviceID,
				History: &netproto.HistoryChunk{Seq: seq, Total: len(chunks), Data: data},
			})
			if err != nil {
				fail(fmt.Errorf("chunk %d of %d: %w", seq+1, len(chunks), err))
				return
			}
			progress.Sent, progress.State = seq+1, "progress"
			cm.emit("device_history", progress)
		}
		progress.State = "done"
		cm.emit("device_history", progress)
	})
	return nil
}
