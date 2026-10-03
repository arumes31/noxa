// Chat moderation filter settings for the selected connection.
package main

import (
	"time"

	"noxa/internal/netproto"
)

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
