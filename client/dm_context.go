package main

import (
	"errors"
	"strconv"
)

// DMHistoryContext identifies one local-history owner. Revisions are strings
// so the JavaScript bridge does not truncate native uint64 counters. It carries
// no key material. Keep a context for its view's lifetime; an obsolete action
// must never obtain a fresh context and retry against another identity.
type DMHistoryContext struct {
	TabID            string `json:"tab_id"`
	IdentityUID      string `json:"identity_uid"`
	Activation       string `json:"activation"`
	IdentityRevision string `json:"identity_revision"`
}

var errDMHistoryContextChanged = errors.New("DM history owner changed; reopen the conversation")

// DMHistoryContextForTab establishes ownership for a new local-history view.
// An empty tab is allowed only when no server tab or manager is active.
func (a *App) DMHistoryContextForTab(tabID string) (DMHistoryContext, error) {
	_, owner, err := a.captureDMHistoryContext(tabID, nil)
	return owner, err
}

func (a *App) captureDMHistoryContext(tabID string, expected *DMHistoryContext) (dmHistoryStore, DMHistoryContext, error) {
	var empty dmHistoryStore
	var owner DMHistoryContext
	if expected != nil && (expected.IdentityUID == "" || expected.Activation == "" || expected.IdentityRevision == "") {
		return empty, owner, errDMHistoryContextChanged
	}
	// Identity mutation and capture share this lock. Never hold tabsMu while
	// resolving manager identity: manager event delivery takes its own locks.
	a.identityMu.Lock()
	defer a.identityMu.Unlock()
	dir, err := a.dmHistoryDir()
	if err != nil {
		return empty, owner, err
	}
	a.tabsMu.Lock()
	cm, tab, generation := a.cmLoad(), a.tabs[tabID], a.activationGeneration
	valid := tabID == a.activeID && ((tabID == "" && cm == nil) || (tabID != "" && tab != nil && tab.cm != nil && tab.cm == cm))
	a.tabsMu.Unlock()
	if !valid {
		return empty, owner, errDMHistoryContextChanged
	}
	owner = DMHistoryContext{
		TabID: tabID, Activation: strconv.FormatUint(generation, 10),
		IdentityRevision: strconv.FormatUint(a.identityGeneration, 10),
	}
	if expected != nil && (expected.Activation != owner.Activation || expected.IdentityRevision != owner.IdentityRevision) {
		return empty, DMHistoryContext{}, errDMHistoryContextChanged
	}
	var id *identity
	if cm != nil {
		id, err = cm.identity()
	} else {
		id, _, err = a.activeIdentityLocked()
	}
	if err != nil {
		return empty, DMHistoryContext{}, err
	}
	owner.IdentityUID, err = id.uniqueID()
	if err != nil {
		return empty, DMHistoryContext{}, err
	}
	store, err := dmHistoryStoreForIdentity(dir, id)
	if err != nil {
		return empty, DMHistoryContext{}, err
	}
	a.tabsMu.Lock()
	valid = a.activeID == tabID && a.cmLoad() == cm && a.tabs[tabID] == tab && a.activationGeneration == generation
	a.tabsMu.Unlock()
	if !valid || (expected != nil && *expected != owner) {
		return empty, DMHistoryContext{}, errDMHistoryContextChanged
	}
	return store, owner, nil
}

func (a *App) dmHistoryForContext(owner DMHistoryContext) (dmHistoryStore, error) {
	store, _, err := a.captureDMHistoryContext(owner.TabID, &owner)
	return store, err
}

// DMHistoryLoadForContext reads only the identity established by the caller.
func (a *App) DMHistoryLoadForContext(owner DMHistoryContext, peer string) ([]DMEntry, error) {
	store, err := a.dmHistoryForContext(owner)
	if err != nil {
		return nil, err
	}
	return store.messages(peer)
}

// DMHistoryAppendForContext captures ownership before waiting for another writer.
func (a *App) DMHistoryAppendForContext(owner DMHistoryContext, peer, nickname string, entry DMEntry) string {
	store, err := a.dmHistoryForContext(owner)
	if err != nil {
		return err.Error()
	}
	return a.dmHistoryAppendWith(store, peer, nickname, entry)
}

// DMHistoryClearForContext removes only the confirmed peer under its original owner.
func (a *App) DMHistoryClearForContext(owner DMHistoryContext, peer string) string {
	store, err := a.dmHistoryForContext(owner)
	if err != nil {
		return err.Error()
	}
	return a.dmHistoryClearWith(store, peer)
}

// DMHistoryPeersForContext enumerates only the captured identity's conversations.
func (a *App) DMHistoryPeersForContext(owner DMHistoryContext) ([]DMPeer, error) {
	store, err := a.dmHistoryForContext(owner)
	if err != nil {
		return nil, err
	}
	return store.peers(), nil
}

// DMSearchForContext retains ownership across enumeration and all loaded logs.
func (a *App) DMSearchForContext(owner DMHistoryContext, peer, query string, maxMessages int) (ChatSearchResult, error) {
	store, err := a.dmHistoryForContext(owner)
	if err != nil {
		return ChatSearchResult{}, err
	}
	return store.search(peer, query, maxMessages), nil
}

// DMExportHistoryForContext formats the captured identity's whole peer log.
func (a *App) DMExportHistoryForContext(owner DMHistoryContext, peer string) (ChatExportResult, error) {
	store, err := a.dmHistoryForContext(owner)
	if err != nil {
		return ChatExportResult{}, err
	}
	rows, err := store.messages(peer)
	if err != nil {
		return ChatExportResult{}, err
	}
	return dmExportMessages(rows), nil
}
