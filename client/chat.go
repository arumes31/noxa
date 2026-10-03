// chat.go exposes channel history and message mutations. Related chat features
// live in chat_*.go, and file-transfer framing lives in file_transfer_*.go.
// History and pins are decrypted in Go before plaintext reaches the webview;
// ciphertext and key identifiers are removed at this boundary.
package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/nacl/secretbox"

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

// chatSearchPage is the store's own page cap. Paging in Go rather than in the
// webview lets a search use it instead of the UI's 50, cutting round trips 4x.
const chatSearchPage = 200

// chatSearchDefaultMax mirrors the server's chat_search_max_messages default.
const chatSearchDefaultMax = 2000

// ChatSearchResult is one client-side history search. Undecryptable counts
// messages under a generation this client could not obtain, so a partial
// search never looks complete.
type ChatSearchResult struct {
	Messages      []netproto.ChatHistoryEntry `json:"messages"`
	Scanned       int                         `json:"scanned"`
	Undecryptable int                         `json:"undecryptable"`
}

// chatScanWith pages a scope's history backwards from newest, decrypting each
// page with the generations the server bundles alongside it, and hands every
// entry to visit. Search (110) and export (125) both need the WHOLE scope
// rather than the window the view happens to hold, so they share one pager —
// two copies would drift apart on exactly the key handling that decides
// whether a gap reads as "no access" or as data loss (103).
//
// It reports how many entries were scanned and whether it reached the
// beginning of history; complete=false means maxMessages or a mid-scan page
// error stopped it, so the caller must present a partial result as partial.
func chatScanWith(m *connManager, channelID int64, maxMessages int, progress, requestID string, visit func(netproto.ChatHistoryEntry)) (int, bool, error) {
	scanned := 0
	before := int64(0)
	for scanned < maxMessages {
		resp, err := chatHistoryWith(m, channelID, before, chatSearchPage)
		if err != nil {
			if scanned == 0 {
				return 0, false, err // nothing to show; surface it
			}
			return scanned, false, nil // partial beats losing the pages we have
		}
		if len(resp.Messages) == 0 {
			return scanned, true, nil
		}
		for _, m := range resp.Messages {
			scanned++
			visit(m)
		}
		before = resp.Messages[len(resp.Messages)-1].ID // pages are newest-first
		if len(resp.Messages) < chatSearchPage {
			return scanned, true, nil
		}
		if progress != "" {
			// Keep progress with the manager captured before paging. A tab switch
			// must not send this scan's UI updates into another server tab.
			if requestID == "" {
				m.emit(progress, scanned)
			} else {
				m.emit(progress, chatScanProgress{RequestID: requestID, Scanned: scanned})
			}
		}
	}
	return scanned, false, nil
}

// ChatSearch pages the scope's history backwards, decrypting each page with
// the keys the server bundles alongside it, and returns matching messages
// newest-first. Search runs entirely client-side: the server stores only
// ciphertext and cannot match on content (110).
func (a *App) ChatSearch(channelID int64, query string, maxMessages int) (ChatSearchResult, error) {
	m, err := a.requireCM()
	if err != nil {
		return ChatSearchResult{}, err
	}
	return chatSearchWith(m, "", channelID, query, maxMessages)
}

type chatScanProgress struct {
	RequestID string `json:"request_id"`
	Scanned   int    `json:"scanned"`
}

// ChatSearchForTab binds every page and progress event to the caller's tab and request.
func (a *App) ChatSearchForTab(tabID, requestID string, channelID int64, query string, maxMessages int) (ChatSearchResult, error) {
	if len(requestID) == 0 || len(requestID) > 128 {
		return ChatSearchResult{}, fmt.Errorf("invalid chat scan request ID")
	}
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return ChatSearchResult{}, err
	}
	return chatSearchWith(m, requestID, channelID, query, maxMessages)
}

func chatSearchWith(m *connManager, requestID string, channelID int64, query string, maxMessages int) (ChatSearchResult, error) {
	q := strings.ToLower(strings.TrimSpace(query))
	if maxMessages <= 0 {
		maxMessages = chatSearchDefaultMax
	}
	var out []netproto.ChatHistoryEntry
	undecryptable := 0
	scanned, _, err := chatScanWith(m, channelID, maxMessages, "chatsearch:progress", requestID, func(m netproto.ChatHistoryEntry) {
		if m.Deleted {
			return
		}
		if !m.EncVerified {
			undecryptable++
			return
		}
		if q != "" && strings.Contains(strings.ToLower(m.Body), q) {
			out = append(out, m)
		}
	})
	if err != nil {
		return ChatSearchResult{}, err
	}
	return ChatSearchResult{Messages: out, Scanned: scanned, Undecryptable: undecryptable}, nil
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

// attachmentStorageName derives the upload name from the CIPHERTEXT. Content
// addressing is what makes per-file keys viable: files has UNIQUE (channel_id,
// name) and same-name uploads rotate through .v1..v3, so with a random key per
// file a second "image.png" would leave every older message holding a key that
// fails Poly1305 against the new blob. Random keys make two uploads of the
// same bytes distinct, so a chat attachment never collides and never rotates.
func attachmentStorageName(blob []byte) string {
	sum := sha256.Sum256(blob)
	return hex.EncodeToString(sum[:])[:32] + ".vcx"
}

// safeDisplayName strips the two characters that would break the token
// grammar. The name is only ever shown, never used to address storage.
func safeDisplayName(name string) string {
	return strings.NewReplacer("]", "_", "#", "_").Replace(name)
}

// parseFileRef splits a [file:<capture>] body token. It is total: zero or one
// separators means a legacy plain reference, which keeps pre-encryption
// messages rendering. chat-ui.js mirrors this exactly.
func parseFileRef(capture string) (storage, keyB64, name string) {
	i := strings.Index(capture, "#")
	if i < 0 {
		return capture, "", capture // legacy [file:photo.png]
	}
	j := strings.Index(capture[i+1:], "#")
	if j < 0 {
		return capture, "", capture // malformed -> treat as plain
	}
	j += i + 1
	return capture[:i], capture[i+1 : j], capture[j+1:]
}

// UploadChatAttachment seals data with a fresh random 32-byte key, uploads
// the ciphertext under a content-derived name, and returns the body token
// "[file:<storage>#<keyB64>#<display>]" for embedding in the chat message.
// The key only ever exists inside the (encrypted) message body, so the file
// gets exactly the protection the message text gets.
func (a *App) UploadChatAttachment(channelID int64, name, dataBase64 string) (string, error) {
	cm, err := a.requireCM()
	if err != nil {
		return "", err
	}
	return uploadChatAttachmentWith(cm, channelID, name, dataBase64)
}

// UploadChatAttachmentForTab captures the server before preparing encrypted bytes.
func (a *App) UploadChatAttachmentForTab(tabID string, channelID int64, name, dataBase64 string) (string, error) {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return "", err
	}
	return uploadChatAttachmentWith(cm, channelID, name, dataBase64)
}

func uploadChatAttachmentWith(cm *connManager, channelID int64, name, dataBase64 string) (string, error) {
	data, err := io.ReadAll(io.LimitReader(
		base64.NewDecoder(base64.StdEncoding, strings.NewReader(dataBase64)),
		int64(maxChatAttachmentBytes)+1,
	))
	if err != nil {
		return "", errors.New("invalid file data")
	}
	if len(data) > maxChatAttachmentBytes {
		return "", errors.New("attachment exceeds 25 MiB limit")
	}
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		return "", err
	}
	blob, err := sealFile(data, key)
	if err != nil {
		return "", err
	}
	storage := attachmentStorageName(blob)
	if err := ftPutBytesWith(cm, channelID, storage, blob); err != nil {
		return "", err
	}
	return "[file:" + storage + "#" + base64.StdEncoding.EncodeToString(key[:]) +
		"#" + safeDisplayName(name) + "]", nil
}

const maxChatAttachmentBytes = 25 << 20

// Sealed attachments include a header plus one length-prefixed AES-GCM record
// per plaintext chunk. They may therefore be slightly larger on the wire while
// still representing at most 25 MiB after decryption.
var maxSealedChatAttachmentBytes = sealedAttachmentSizeForPlaintext(maxChatAttachmentBytes)

const maxInlineAttachmentBase64Bytes = 8 << 20

var errChatAttachmentPreviewTooLarge = errors.New("attachment is too large to preview")

// chatAttachmentBytesForCM fetches and opens one attachment with the captured
// manager. A native save dialog may suspend the UI; retaining this manager
// prevents a concurrent tab switch from redirecting the later download to a
// different server.
func (a *App) chatAttachmentBytesForCM(cm *connManager, channelID int64, storage, keyB64 string) ([]byte, error) {
	// A caller that hands over the raw capture instead of its parts is parsed
	// here too, so a token can never be mistaken for a file name.
	if keyB64 == "" && strings.Contains(storage, "#") {
		storage, keyB64, _ = parseFileRef(storage)
	}
	if storage == "" {
		return nil, errors.New("invalid attachment storage")
	}
	var key [32]byte
	if keyB64 != "" {
		raw, err := base64.StdEncoding.DecodeString(keyB64)
		if err != nil || len(raw) != len(key) {
			return nil, errors.New("invalid attachment key")
		}
		copy(key[:], raw)
	}
	var (
		blob []byte
		err  error
	)
	if a.chatAttachmentFetch != nil {
		blob, err = a.chatAttachmentFetch(cm, channelID, storage)
	} else {
		maxWireBytes := maxChatAttachmentBytes
		if keyB64 != "" {
			maxWireBytes = maxSealedChatAttachmentBytes
		}
		blob, err = a.ftGetBytesForCMWithLimit(cm, channelID, storage, int64(maxWireBytes))
	}
	if err != nil {
		return nil, err
	}
	if keyB64 == "" && len(blob) > maxChatAttachmentBytes {
		return nil, errors.New("attachment exceeds 25 MiB limit")
	}
	if keyB64 == "" {
		return blob, nil
	}
	if bytes.HasPrefix(blob, attachmentGCMHeader) {
		if len(blob) > maxSealedChatAttachmentBytes {
			return nil, errors.New("attachment exceeds 25 MiB limit")
		}
	} else if len(blob) > maxChatAttachmentBytes {
		// The larger encrypted wire allowance is exclusively for the chunked
		// GCM format. Legacy secretbox attachments retain their former cap.
		return nil, errors.New("attachment exceeds 25 MiB limit")
	}
	plain, err := openFileLimited(blob, key, maxChatAttachmentBytes)
	if err != nil {
		return nil, err
	}
	return plain, nil
}

func (a *App) chatAttachmentBytes(channelID int64, storage, keyB64 string) ([]byte, error) {
	cm, err := a.requireCM()
	if err != nil {
		return nil, err
	}
	return a.chatAttachmentBytesForCM(cm, channelID, storage, keyB64)
}

// DownloadChatAttachment fetches <storage> and unseals it with keyB64,
// returning bounded base64 plaintext for inline preview. An empty keyB64
// downloads a plain legacy [file:photo.png] reference.
func (a *App) DownloadChatAttachment(channelID int64, storage, keyB64 string) (string, error) {
	plain, err := a.chatAttachmentBytes(channelID, storage, keyB64)
	return inlineChatAttachment(plain, err)
}

// DownloadChatAttachmentForTab binds an inline preview to its source server.
func (a *App) DownloadChatAttachmentForTab(tabID string, channelID int64, storage, keyB64 string) (string, error) {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return "", err
	}
	plain, err := a.chatAttachmentBytesForCM(cm, channelID, storage, keyB64)
	return inlineChatAttachment(plain, err)
}

func inlineChatAttachment(plain []byte, err error) (string, error) {
	if err != nil {
		return "", err
	}
	if base64.StdEncoding.EncodedLen(len(plain)) > maxInlineAttachmentBase64Bytes {
		return "", errChatAttachmentPreviewTooLarge
	}
	return base64.StdEncoding.EncodeToString(plain), nil
}

var errChatAttachmentDestinationExists = errors.New("the selected file already exists")

// chatAttachmentLink is replaceable in tests that model a filename appearing
// after the dialog/preflight check. os.Link is an atomic no-overwrite publish
// on a same-directory temporary file, including on Windows NTFS.
var chatAttachmentLink = os.Link

func safeAttachmentFilename(defaultName string) string {
	name := strings.TrimSpace(defaultName)
	if i := strings.LastIndexAny(name, "/\\"); i >= 0 {
		name = name[i+1:]
	}
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	if name == "" || name == "." || name == ".." {
		return "attachment"
	}
	return name
}

func chatAttachmentDestination(dest string) (string, error) {
	dest = filepath.Clean(dest)
	base := filepath.Base(dest)
	if dest == "." || base == "." || base == string(filepath.Separator) {
		return "", errors.New("invalid attachment destination")
	}
	info, err := os.Stat(filepath.Dir(dest))
	if err != nil {
		return "", fmt.Errorf("opening attachment destination directory: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("attachment destination parent is not a directory")
	}
	if _, err := os.Lstat(dest); err == nil {
		return "", errChatAttachmentDestinationExists
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("checking attachment destination: %w", err)
	}
	return dest, nil
}

func (a *App) chooseChatAttachmentPath(defaultName string) (string, error) {
	options := wailsRuntime.SaveDialogOptions{
		Title:           "Save chat attachment",
		DefaultFilename: safeAttachmentFilename(defaultName),
	}
	if a.chatAttachmentSaveDialog != nil {
		return a.chatAttachmentSaveDialog(a.ctx, options)
	}
	if a.ctx == nil {
		return "", errors.New("save dialog is unavailable")
	}
	return wailsRuntime.SaveFileDialog(a.ctx, options)
}

func writeChatAttachmentAtomically(dest string, data []byte) (err error) {
	dest, err = chatAttachmentDestination(dest)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(dest), "."+filepath.Base(dest)+".tmp-")
	if err != nil {
		return err
	}
	temp := f.Name()
	defer func() {
		if temp != "" {
			_ = os.Remove(temp)
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if n, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	} else if n != len(data) {
		_ = f.Close()
		return io.ErrShortWrite
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := chatAttachmentLink(temp, dest); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return errChatAttachmentDestinationExists
		}
		return fmt.Errorf("publishing attachment: %w", err)
	}
	if err := os.Remove(temp); err != nil {
		return fmt.Errorf("cleaning attachment temporary file: %w", err)
	}
	temp = ""
	return nil
}

// SaveChatAttachment opens the native save dialog before downloading. It
// decrypts only in Go and publishes a fully verified new destination without
// replacing an existing file. Cancel returns an empty path and no error.
func (a *App) SaveChatAttachment(channelID int64, storage, keyB64, defaultName string) (string, error) {
	// Capture the active manager before the native dialog. A tab switch while
	// the dialog is open must not redirect the selected attachment to another
	// server; no network operation occurs until after the user chooses a path.
	cm := a.cmLoad()
	return a.saveChatAttachmentWith(cm, channelID, storage, keyB64, defaultName)
}

// SaveChatAttachmentForTab rejects stale tabs before opening the native dialog.
func (a *App) SaveChatAttachmentForTab(tabID string, channelID int64, storage, keyB64, defaultName string) (string, error) {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return "", err
	}
	return a.saveChatAttachmentWith(cm, channelID, storage, keyB64, defaultName)
}

func (a *App) saveChatAttachmentWith(cm *connManager, channelID int64, storage, keyB64, defaultName string) (string, error) {
	dest, err := a.chooseChatAttachmentPath(defaultName)
	if err != nil || dest == "" {
		return dest, err
	}
	dest, err = chatAttachmentDestination(dest)
	if err != nil {
		return "", err
	}
	if cm == nil {
		return "", errors.New("not connected")
	}
	plain, err := a.chatAttachmentBytesForCM(cm, channelID, storage, keyB64)
	if err != nil {
		return "", err
	}
	write := a.chatAttachmentWrite
	if write == nil {
		write = writeChatAttachmentAtomically
	}
	if err := write(dest, plain); err != nil {
		return "", err
	}
	return dest, nil
}

// chatExportDefaultMax bounds a full-history export. It is far above a
// search's cap on purpose: an export that quietly stops after 2000 messages is
// the very "only what the view happened to hold" gap 125 exists to close.
const chatExportDefaultMax = 100000

// ChatExportResult is a rendered full-channel transcript, ready to hand to
// ExportChat or ExportChatEncrypted.
type ChatExportResult struct {
	Text          string `json:"text"`
	Messages      int    `json:"messages"`
	Undecryptable int    `json:"undecryptable"`
	Complete      bool   `json:"complete"`
}

// ChatExportHistory pages and decrypts a scope's ENTIRE stored history and
// renders it oldest-first, instead of exporting the window the chat view
// happens to hold (125). It runs on the search pager, so export and search can
// never disagree about which generations they could open. Progress lands on
// the "chatexport:progress" event as the running scanned count.
func (a *App) ChatExportHistory(channelID int64, maxMessages int) (ChatExportResult, error) {
	m, err := a.requireCM()
	if err != nil {
		return ChatExportResult{}, err
	}
	return chatExportHistoryWith(m, "", channelID, maxMessages)
}

// ChatExportHistoryForTab captures the confirmed source before the first page.
func (a *App) ChatExportHistoryForTab(tabID, requestID string, channelID int64, maxMessages int) (ChatExportResult, error) {
	if len(requestID) == 0 || len(requestID) > 128 {
		return ChatExportResult{}, fmt.Errorf("invalid chat scan request ID")
	}
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return ChatExportResult{}, err
	}
	return chatExportHistoryWith(m, requestID, channelID, maxMessages)
}

func chatExportHistoryWith(m *connManager, requestID string, channelID int64, maxMessages int) (ChatExportResult, error) {
	if maxMessages <= 0 {
		maxMessages = chatExportDefaultMax
	}
	var lines []string
	undecryptable := 0
	scanned, complete, err := chatScanWith(m, channelID, maxMessages, "chatexport:progress", requestID, func(m netproto.ChatHistoryEntry) {
		if !m.Deleted && !m.EncVerified {
			undecryptable++
		}
		lines = append(lines, chatExportLine(m))
	})
	if err != nil {
		return ChatExportResult{}, err
	}
	// chatScanWith walks newest-first; a transcript reads oldest-first.
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	if !complete || undecryptable > 0 {
		// The notice rides INSIDE the file: a caller that writes Text straight
		// out must not be able to produce a partial transcript that looks whole.
		reach := "complete"
		if !complete {
			reach = "TRUNCATED at the export limit"
		}
		lines = append([]string{fmt.Sprintf(
			"# noXa export — %d messages, %d unreadable (no key), %s",
			scanned, undecryptable, reach)}, lines...)
	}
	text := ""
	if len(lines) > 0 {
		text = strings.Join(lines, "\n") + "\n"
	}
	return ChatExportResult{
		Text: text, Messages: scanned, Undecryptable: undecryptable, Complete: complete,
	}, nil
}

// chatExportLine renders one transcript line. A body this client could not
// open keeps its placeholder rather than vanishing, so a gap in the export is
// visible as a gap instead of looking like a quiet channel.
func chatExportLine(m netproto.ChatHistoryEntry) string {
	body := m.Body
	if m.Deleted {
		body = "(deleted)"
	}
	if m.EditedAt != 0 {
		body += " (edited)"
	}
	name := m.FromNickname
	if name == "" {
		name = m.FromUniqueID
	}
	return "[" + time.Unix(m.SentAt, 0).Format("2006-01-02 15:04:05") + "] " + name + ": " + body
}

// DMExportHistory renders a stored DM conversation into the same transcript
// shape as ChatExportHistory, so exporting a PM tab reaches the whole local
// log rather than the loaded window (122/125).
func (a *App) DMExportHistory(peer string) (ChatExportResult, error) {
	msgs, err := a.DMHistoryLoad(peer)
	if err != nil {
		return ChatExportResult{}, err
	}
	return dmExportMessages(msgs), nil
}

func dmExportMessages(msgs []DMEntry) ChatExportResult {
	lines := make([]string, 0, len(msgs))
	for _, m := range msgs {
		lines = append(lines, chatExportLine(netproto.ChatHistoryEntry{
			FromUniqueID: m.FromUniqueID,
			FromNickname: m.FromNickname,
			Body:         m.Body,
			SentAt:       m.SentAt,
		}))
	}
	text := ""
	if len(lines) > 0 {
		text = strings.Join(lines, "\n") + "\n"
	}
	return ChatExportResult{Text: text, Messages: len(msgs), Complete: true}
}

// ExportChat saves text to a user-chosen file via the native save dialog.
// Returns "" on success or when the user cancels, or the error.
func (a *App) ExportChat(defaultName, contents string) string {
	path, err := wailsRuntime.SaveFileDialog(a.ctx, wailsRuntime.SaveDialogOptions{
		DefaultFilename: defaultName,
		Filters: []wailsRuntime.FileFilter{
			{DisplayName: "Text / HTML", Pattern: "*.txt;*.html"},
		},
	})
	if err != nil || path == "" {
		return "" // cancelled
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		return err.Error()
	}
	return ""
}

// Encrypted export container: magic || salt || nonce || secretbox. The key is
// derived from the passphrase with argon2id, so the file is worth no more than
// the passphrase — but a plaintext export of an encrypted chat is worth far
// less than that (125).
// Keep the format identifier stable so existing encrypted exports remain readable.
var exportMagic = []byte("VOICXCHAT1")

const (
	exportSaltLen = 16
	// argon2id parameters: one pass over 64 MiB, 4 lanes. Interactive cost on
	// a desktop, far past a GPU-friendly KDF.
	exportArgonTime    = 1
	exportArgonMemory  = 64 * 1024
	exportArgonThreads = 4
)

// sealExport builds the encrypted export container.
func sealExport(contents, passphrase string) ([]byte, error) {
	if passphrase == "" {
		return nil, errors.New("a passphrase is required for an encrypted export")
	}
	salt := make([]byte, exportSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	var nonce [24]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	var key [32]byte
	copy(key[:], argon2.IDKey([]byte(passphrase), salt, exportArgonTime, exportArgonMemory, exportArgonThreads, 32))

	out := make([]byte, 0, len(exportMagic)+exportSaltLen+24+len(contents)+secretbox.Overhead)
	out = append(out, exportMagic...)
	out = append(out, salt...)
	out = append(out, nonce[:]...)
	return secretbox.Seal(out, []byte(contents), &nonce, &key), nil
}

// openExport reverses sealExport (the import half and the round-trip test).
func openExport(blob []byte, passphrase string) (string, error) {
	head := len(exportMagic) + exportSaltLen + 24
	if len(blob) < head || !bytes.Equal(blob[:len(exportMagic)], exportMagic) {
		return "", errors.New("not a noxa encrypted chat export")
	}
	salt := blob[len(exportMagic) : len(exportMagic)+exportSaltLen]
	var nonce [24]byte
	copy(nonce[:], blob[len(exportMagic)+exportSaltLen:head])
	var key [32]byte
	copy(key[:], argon2.IDKey([]byte(passphrase), salt, exportArgonTime, exportArgonMemory, exportArgonThreads, 32))

	plain, ok := secretbox.Open(nil, blob[head:], &nonce, &key)
	if !ok {
		return "", errors.New("wrong passphrase or corrupt export")
	}
	return string(plain), nil
}

// ExportChatEncrypted saves the transcript passphrase-encrypted as
// .noxachat, so an export of an encrypted chat does not become the plaintext
// copy the whole design exists to prevent. Returns "" on success or cancel.
func (a *App) ExportChatEncrypted(defaultName, contents, passphrase string) (string, error) {
	blob, err := sealExport(contents, passphrase)
	if err != nil {
		return "", err
	}
	path, err := wailsRuntime.SaveFileDialog(a.ctx, wailsRuntime.SaveDialogOptions{
		DefaultFilename: defaultName,
		Filters: []wailsRuntime.FileFilter{
			{DisplayName: "Encrypted chat export", Pattern: "*.noxachat"},
		},
	})
	if err != nil || path == "" {
		return "", nil // cancelled
	}
	if err := os.WriteFile(path, blob, 0o600); err != nil {
		return "", err
	}
	return path, nil
}
