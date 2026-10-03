// Custom emoji upload, discovery, and retrieval.
package main

import (
	"errors"
	"time"

	"noxa/internal/netproto"
)

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
