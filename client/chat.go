// chat.go exposes channel history and message mutations. Related chat features
// live in chat_*.go, and file-transfer framing lives in file_transfer_*.go.
// History and pins are decrypted in Go before plaintext reaches the webview;
// ciphertext and key identifiers are removed at this boundary.
package main

import (
	"time"

	"noxa/internal/netproto"
)

// ChatHistory fetches a page of channel/global history (beforeID 0 = latest)
// and decrypts it. The page carries the generations it references, so no
// nested key round trip is needed (110).
func (a *App) ChatHistory(channelID, beforeID int64, limit int) (netproto.ChatHistoryResponse, error) {
	m, err := a.requireCM()
	if err != nil {
		return netproto.ChatHistoryResponse{}, err
	}
	return chatHistoryWith(m, channelID, beforeID, limit)
}

// ChatHistoryForTab binds the request and decryption keys to the displayed server.
func (a *App) ChatHistoryForTab(tabID string, channelID, beforeID int64, limit int) (netproto.ChatHistoryResponse, error) {
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return netproto.ChatHistoryResponse{}, err
	}
	return chatHistoryWith(m, channelID, beforeID, limit)
}

// chatHistoryWith performs one page through the manager selected at the
// compound operation's start. Search/export must not re-resolve App.cm for
// every page: an active-tab switch midway through a scan would otherwise mix
// two servers and their unrelated key generations.
func chatHistoryWith(m *connManager, channelID, beforeID int64, limit int) (netproto.ChatHistoryResponse, error) {
	f, err := m.request(netproto.MsgChatHistory, netproto.MsgChatHistoryResponse,
		netproto.ChatHistory{ChannelID: channelID, BeforeID: beforeID, Limit: limit}, 10*time.Second)
	if err != nil {
		return netproto.ChatHistoryResponse{}, err
	}
	var resp netproto.ChatHistoryResponse
	if err := decodeJSON(f, &resp); err != nil {
		return netproto.ChatHistoryResponse{}, err
	}
	refused := installPageKeys(m, channelID, resp.Keys, resp.Refused)
	for i := range resp.Messages {
		openChatEntry(m, channelID, &resp.Messages[i], refused)
	}
	resp.Keys = nil // sealed key material has no business in the webview
	return resp, nil
}

// installPageKeys unseals the generations bundled with a history/pins page,
// records the permanently withheld ones, and returns the refusal set for this
// page. The keys are ARCHIVAL: they must never advance the send generation.
func installPageKeys(m *connManager, scope int64, keys []netproto.ChannelKey, refusedIDs []uint32) map[uint32]bool {
	m.installKeys(scope, keys)
	m.scopeKeys.markRefused(scope, refusedIDs)
	refused := make(map[uint32]bool, len(refusedIDs))
	for _, g := range refusedIDs {
		refused[g] = true
	}
	return refused
}

// openChatEntry decrypts one stored entry IN PLACE and always strips BodyEnc
// and KeyID, so no code path can hand the webview ciphertext or a key id.
// EncVerified is the provenance flag the renderer draws its shield from: it is
// set only when this client actually opened the body itself.
func openChatEntry(m *connManager, scope int64, e *netproto.ChatHistoryEntry, refused map[uint32]bool) {
	defer func() { e.BodyEnc, e.KeyID = "", 0 }()
	e.EncVerified = false
	if e.Deleted {
		e.Body = ""
		return
	}
	if e.BodyEnc == "" && e.Body != "" {
		// The downgrade hole closed from this side: no server config makes
		// this client render a plaintext history body (91-135).
		e.Body = refusedPlainText
		return
	}
	key, ok := m.scopeKeys.get(scope, e.KeyID)
	switch {
	case refused[e.KeyID]:
		e.Body = refusedKeyText
	case !ok:
		e.Body = missingKeyText
	default:
		plain, err := openScope(e.BodyEnc, key)
		if err != nil {
			// The key IS held and it still did not open: tampered or corrupt,
			// which no retry fixes. Saying "key unavailable" here would park a
			// permanent failure behind a pull that can never succeed (103).
			e.Body = decryptFailedText
			return
		}
		e.Body, e.EncVerified = plain, true
	}
}

// ChatEditMessage edits one of the caller's own messages. The new body is
// sealed with the channel's current scope key (the server decrypts it for
// storage and re-seals for the broadcast).
func (a *App) ChatEditMessage(channelID, messageID int64, newText string, expectedVersion uint64) string {
	m, err := a.requireCM()
	if err != nil {
		return err.Error()
	}
	return chatEditMessageWith(m, channelID, messageID, newText, expectedVersion)
}

// ChatEditMessageForTab binds encryption and the write to the displayed server.
func (a *App) ChatEditMessageForTab(tabID string, channelID, messageID int64, newText string, expectedVersion uint64) string {
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return err.Error()
	}
	return chatEditMessageWith(m, channelID, messageID, newText, expectedVersion)
}

func chatEditMessageWith(m *connManager, channelID, messageID int64, newText string, expectedVersion uint64) string {
	keyID, key, ok := m.scopeKeys.current(channelID)
	if !ok {
		return "no chat key for this channel yet"
	}
	blob, err := sealScope(newText, key)
	if err != nil {
		return err.Error()
	}
	ack := m.usesRoleAuthorization()
	if err := m.writeChatMutation(netproto.MsgChatEdit, messageID, netproto.ChatEdit{
		AckRequested: ack,
		MessageID:    messageID, NewText: blob, Enc: true, KeyID: keyID, ExpectedVersion: expectedVersion,
	}, ack); err != nil {
		return err.Error()
	}
	return ""
}

// ChatDeleteMessage deletes (tombstones) a message.
func (a *App) ChatDeleteMessage(messageID int64) string {
	m, err := a.requireCM()
	if err != nil {
		return err.Error()
	}
	return chatDeleteMessageWith(m, messageID)
}

// ChatDeleteMessageForTab binds the message ID to its original server.
func (a *App) ChatDeleteMessageForTab(tabID string, messageID int64) string {
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return err.Error()
	}
	return chatDeleteMessageWith(m, messageID)
}

func chatDeleteMessageWith(m *connManager, messageID int64) string {
	ack := m.usesRoleAuthorization()
	if err := m.writeChatMutation(netproto.MsgChatDelete, messageID, netproto.ChatDelete{AckRequested: ack, MessageID: messageID}, ack); err != nil {
		return err.Error()
	}
	return ""
}

// ChatPinMessage pins or unpins a message.
func (a *App) ChatPinMessage(channelID, messageID int64, pinned bool) string {
	m, err := a.requireCM()
	if err != nil {
		return err.Error()
	}
	return chatPinMessageWith(m, channelID, messageID, pinned)
}

// ChatPinMessageForTab binds the pin change to its original server.
func (a *App) ChatPinMessageForTab(tabID string, channelID, messageID int64, pinned bool) string {
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return err.Error()
	}
	return chatPinMessageWith(m, channelID, messageID, pinned)
}

func chatPinMessageWith(m *connManager, channelID, messageID int64, pinned bool) string {
	ack := m.usesRoleAuthorization()
	if err := m.writeChatMutation(netproto.MsgChatPin, messageID, netproto.ChatPin{
		AckRequested: ack,
		ChannelID:    channelID, MessageID: messageID, Pinned: pinned,
	}, ack); err != nil {
		return err.Error()
	}
	return ""
}

// ChatPins lists a channel's pinned messages, decrypted exactly like history.
func (a *App) ChatPins(channelID int64) (netproto.ChatPinsResponse, error) {
	m, err := a.requireCM()
	if err != nil {
		return netproto.ChatPinsResponse{}, err
	}
	return chatPinsWith(m, channelID)
}

// ChatPinsForTab captures the expected server before requesting encrypted pins.
func (a *App) ChatPinsForTab(tabID string, channelID int64) (netproto.ChatPinsResponse, error) {
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return netproto.ChatPinsResponse{}, err
	}
	return chatPinsWith(m, channelID)
}

func chatPinsWith(m *connManager, channelID int64) (netproto.ChatPinsResponse, error) {
	f, err := m.request(netproto.MsgChatPins, netproto.MsgChatPinsResponse,
		netproto.ChatPins{ChannelID: channelID}, 10*time.Second)
	if err != nil {
		return netproto.ChatPinsResponse{}, err
	}
	var resp netproto.ChatPinsResponse
	if err := decodeJSON(f, &resp); err != nil {
		return netproto.ChatPinsResponse{}, err
	}
	refused := installPageKeys(m, channelID, resp.Keys, resp.Refused)
	for i := range resp.Pins {
		if resp.Pins[i].Message != nil {
			openChatEntry(m, channelID, resp.Pins[i].Message, refused)
		}
	}
	resp.Keys = nil
	return resp, nil
}

// ChatReact toggles a reaction on a message.
func (a *App) ChatReact(messageID int64, emoji string) string {
	m, err := a.requireCM()
	if err != nil {
		return err.Error()
	}
	return chatReactWith(m, messageID, emoji)
}

// ChatReactForTab binds the reaction to its original server.
func (a *App) ChatReactForTab(tabID string, messageID int64, emoji string) string {
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return err.Error()
	}
	return chatReactWith(m, messageID, emoji)
}

func chatReactWith(m *connManager, messageID int64, emoji string) string {
	ack := m.usesRoleAuthorization()
	if err := m.writeChatMutation(netproto.MsgChatReact, messageID, netproto.ChatReact{
		AckRequested: ack,
		MessageID:    messageID, Emoji: emoji,
	}, ack); err != nil {
		return err.Error()
	}
	return ""
}
