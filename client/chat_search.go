// Client-side history paging, decryption, and search.
package main

import (
	"fmt"
	"strings"

	"noxa/internal/netproto"
)

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
