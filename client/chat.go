// chat.go exposes channel history and message mutations. Related chat features
// live in chat_*.go, and file-transfer framing lives in file_transfer_*.go.
// History and pins are decrypted in Go before plaintext reaches the webview;
// ciphertext and key identifiers are removed at this boundary.
package main

import (
	"encoding/json"
	"errors"
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

// SubscribeChannels asks the server to (un)subscribe the given channels. The
// answer is never returned here: the server replies with the authoritative
// full set on MsgSubscriptionState, which arrives on the read loop as the
// "subscriptions" event. Returning a set from here would give the UI a second
// source of truth that could disagree with it.
func (a *App) SubscribeChannels(channelIDs []int64, subscribe bool) string {
	if len(channelIDs) == 0 {
		return "no channels given"
	}
	m := a.cmLoad()
	if m == nil {
		return "not connected"
	}
	return m.subscribeChannels(channelIDs, subscribe)
}

// SubscribeChannelsForTab submits a change to the displayed server. The
// subscriptions event remains the sole source of authoritative membership.
func (a *App) SubscribeChannelsForTab(tabID string, channelIDs []int64, subscribe bool) string {
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return err.Error()
	}
	return m.subscribeChannels(channelIDs, subscribe)
}

func (m *connManager) subscribeChannels(channelIDs []int64, subscribe bool) string {
	if len(channelIDs) == 0 {
		return "no channels given"
	}
	if err := m.write(netproto.MsgChannelSubscribe, netproto.ChannelSubscribe{
		ChannelIDs: channelIDs, Subscribe: subscribe,
	}); err != nil {
		return err.Error()
	}
	return ""
}

// Subscriptions returns the newest authoritative subscription set the server
// pushed on this connection. A background server tab drops live events, so
// this is how a tab switch recovers the set without a round trip (281/312).
func (a *App) Subscriptions() []int64 {
	m := a.cmLoad()
	if m == nil {
		return []int64{}
	}
	return m.subscriptions()
}

// SubscriptionsForTab reads the cached subscription set of the displayed server.
func (a *App) SubscriptionsForTab(tabID string) ([]int64, error) {
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return nil, err
	}
	return m.subscriptions(), nil
}

func (m *connManager) subscriptions() []int64 {
	m.mu.Lock()
	raw := m.lastSubscriptions
	m.mu.Unlock()
	out := []int64{}
	if raw == "" {
		return out
	}
	var st netproto.SubscriptionState
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		return out
	}
	return append(out, st.ChannelIDs...)
}

// ChatFilterGet reads the word/link moderation lists in force. Reading is
// gated server-side by b_chat_filter_manage exactly like writing, because the
// word list tells an evader what to avoid; a caller without the permission
// gets an error frame instead of a response and this call times out.
func (a *App) ChatFilterGet() (netproto.ChatFilterResponse, error) {
	m, err := a.requireCM()
	if err != nil {
		return netproto.ChatFilterResponse{}, err
	}
	return m.chatFilterGet()
}

// ChatFilterGetForTab rejects operations from a different server's dialog.
func (a *App) ChatFilterGetForTab(tabID string) (netproto.ChatFilterResponse, error) {
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return netproto.ChatFilterResponse{}, err
	}
	return m.chatFilterGet()
}

func (m *connManager) chatFilterGet() (netproto.ChatFilterResponse, error) {
	f, err := m.request(netproto.MsgChatFilterGet, netproto.MsgChatFilterResponse,
		netproto.ChatFilterGet{}, 5*time.Second)
	if err != nil {
		return netproto.ChatFilterResponse{}, err
	}
	var resp netproto.ChatFilterResponse
	if err := decodeJSON(f, &resp); err != nil {
		return netproto.ChatFilterResponse{}, err
	}
	return resp, nil
}

// ChatFilterSet replaces all three lists and returns the state now in force.
// The dialog always submits the whole form, so every list is sent explicitly:
// the wire form treats a missing list as "leave unchanged", which cannot say
// "clear it" (117/118).
func (a *App) ChatFilterSet(wordFilter, linkBlacklist, linkWhitelist string) (netproto.ChatFilterResponse, error) {
	m, err := a.requireCM()
	if err != nil {
		return netproto.ChatFilterResponse{}, err
	}
	return m.chatFilterSet(wordFilter, linkBlacklist, linkWhitelist)
}

// ChatFilterSetForTab rejects operations from a different server's dialog.
func (a *App) ChatFilterSetForTab(tabID string, wordFilter, linkBlacklist, linkWhitelist string) (netproto.ChatFilterResponse, error) {
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return netproto.ChatFilterResponse{}, err
	}
	return m.chatFilterSet(wordFilter, linkBlacklist, linkWhitelist)
}

func (m *connManager) chatFilterSet(wordFilter, linkBlacklist, linkWhitelist string) (netproto.ChatFilterResponse, error) {
	f, err := m.request(netproto.MsgChatFilterSet, netproto.MsgChatFilterResponse,
		netproto.ChatFilterSet{
			WordFilter:    &wordFilter,
			LinkBlacklist: &linkBlacklist,
			LinkWhitelist: &linkWhitelist,
		}, 5*time.Second)
	if err != nil {
		return netproto.ChatFilterResponse{}, err
	}
	var resp netproto.ChatFilterResponse
	if err := decodeJSON(f, &resp); err != nil {
		return netproto.ChatFilterResponse{}, err
	}
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

// SendTyping relays a typing indicator (channel scope or DM).
func (a *App) SendTyping(channelID int64, toUniqueID string) string {
	m, err := a.requireCM()
	if err != nil {
		return err.Error()
	}
	return sendTypingWith(m, channelID, toUniqueID)
}

// SendTypingForTab sends activity only through the displayed server.
func (a *App) SendTypingForTab(tabID string, channelID int64, toUniqueID string) string {
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return err.Error()
	}
	return sendTypingWith(m, channelID, toUniqueID)
}

func sendTypingWith(m *connManager, channelID int64, toUniqueID string) string {
	if err := m.write(netproto.MsgTyping, netproto.Typing{
		ChannelID: channelID, ToUniqueID: toUniqueID,
	}); err != nil {
		return err.Error()
	}
	return ""
}

// SendChatDelivered acks a received DM (delivery receipt to the sender).
func (a *App) SendChatDelivered(toUniqueID, clientMsgID string) string {
	m, err := a.requireCM()
	if err != nil {
		return err.Error()
	}
	return sendChatDeliveredWith(m, toUniqueID, clientMsgID)
}

// SendChatDeliveredForTab binds a delivery receipt to its source server.
func (a *App) SendChatDeliveredForTab(tabID, toUniqueID, clientMsgID string) string {
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return err.Error()
	}
	return sendChatDeliveredWith(m, toUniqueID, clientMsgID)
}

func sendChatDeliveredWith(m *connManager, toUniqueID, clientMsgID string) string {
	if err := m.write(netproto.MsgChatDelivered, netproto.ChatDelivered{
		ToUniqueID: toUniqueID, ClientMsgID: clientMsgID,
	}); err != nil {
		return err.Error()
	}
	return ""
}

// SendChatRead acks a read DM (read receipt to the sender).
func (a *App) SendChatRead(toUniqueID, clientMsgID string) string {
	m, err := a.requireCM()
	if err != nil {
		return err.Error()
	}
	return sendChatReadWith(m, toUniqueID, clientMsgID)
}

// SendChatReadForTab binds a read receipt to its source server.
func (a *App) SendChatReadForTab(tabID, toUniqueID, clientMsgID string) string {
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return err.Error()
	}
	return sendChatReadWith(m, toUniqueID, clientMsgID)
}

func sendChatReadWith(m *connManager, toUniqueID, clientMsgID string) string {
	if err := m.write(netproto.MsgChatRead, netproto.ChatRead{
		ToUniqueID: toUniqueID, ClientMsgID: clientMsgID,
	}); err != nil {
		return err.Error()
	}
	return ""
}

// EmojiList lists the server's custom emojis.
// EmojiUpload uploads a custom server emoji (96). The server gates it on
// ManageEmoji in role mode and waits for the storage result before succeeding.
func (a *App) EmojiUpload(name, dataBase64 string) string {
	m, err := a.requireCM()
	if err != nil {
		return err.Error()
	}
	return m.emojiUpload(name, dataBase64)
}

// EmojiUploadForTab rejects uploads from another server's view.
func (a *App) EmojiUploadForTab(tabID, name, dataBase64 string) string {
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return err.Error()
	}
	return m.emojiUpload(name, dataBase64)
}

func (m *connManager) emojiUpload(name, dataBase64 string) string {
	if err := m.mutateAsset(netproto.MsgEmojiUpload, name, "", dataBase64); err != nil {
		return err.Error()
	}
	return ""
}

func (a *App) EmojiList() (netproto.EmojiListResponse, error) {
	m, err := a.requireCM()
	if err != nil {
		return netproto.EmojiListResponse{}, err
	}
	return m.emojiList()
}

// EmojiListForTab keeps emoji discovery on the originating server.
func (a *App) EmojiListForTab(tabID string) (netproto.EmojiListResponse, error) {
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return netproto.EmojiListResponse{}, err
	}
	return m.emojiList()
}

func (m *connManager) emojiList() (netproto.EmojiListResponse, error) {
	f, err := m.request(netproto.MsgEmojiList, netproto.MsgEmojiListResponse,
		netproto.EmojiList{}, 10*time.Second)
	if err != nil {
		return netproto.EmojiListResponse{}, err
	}
	var resp netproto.EmojiListResponse
	if err := decodeJSON(f, &resp); err != nil {
		return netproto.EmojiListResponse{}, err
	}
	return resp, nil
}

// EmojiGet fetches one custom emoji image (cached by the frontend).
func (a *App) EmojiGet(name string) (netproto.EmojiData, error) {
	m, err := a.requireCM()
	if err != nil {
		return netproto.EmojiData{}, err
	}
	return m.emojiGet(name)
}

// EmojiGetForTab keeps emoji images on the originating server.
func (a *App) EmojiGetForTab(tabID, name string) (netproto.EmojiData, error) {
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return netproto.EmojiData{}, err
	}
	return m.emojiGet(name)
}

func (m *connManager) emojiGet(name string) (netproto.EmojiData, error) {
	f, err := m.request(netproto.MsgEmojiGet, netproto.MsgEmojiData,
		netproto.EmojiGet{Name: name}, 10*time.Second)
	if err != nil {
		return netproto.EmojiData{}, err
	}
	var resp netproto.EmojiData
	if err := decodeJSON(f, &resp); err != nil {
		return netproto.EmojiData{}, err
	}
	if resp.DataBase64 == "" {
		return netproto.EmojiData{}, errors.New("emoji not found")
	}
	return resp, nil
}
