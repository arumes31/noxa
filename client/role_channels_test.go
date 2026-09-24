package main

import (
	"sync"
	"testing"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

func TestRoleChannelBindingWaitsForCommittedAcknowledgement(t *testing.T) {
	received := make(chan netproto.RoleChannelChange, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	app, _ := newPipedApp(t, func(f *netproto.Frame) (netproto.MessageType, any, bool) {
		if f.Type == uint16(netproto.MsgRoleChannelQuery) {
			return netproto.MsgRoleChannelState, netproto.RoleChannelState{Revision: 7, ChannelID: 3}, true
		}
		var change netproto.RoleChannelChange
		if err := netproto.Decode(f, &change); err != nil {
			t.Error(err)
		}
		received <- change
		<-release
		return netproto.MsgRoleChannelResult, netproto.RoleChannelResult{Revision: 8, ChannelID: 3, EnforcementPending: true}, true
	})
	app.tabs = map[string]*tabState{"a": {cm: app.cmLoad()}, "b": {}}
	app.activeID = "a"
	query, err := app.RoleChannelStateForTab("a", netproto.RoleChannelQuery{Kind: authorization.ChannelMove, ChannelID: 3})
	if err != nil || query.Revision != 7 {
		t.Fatalf("channel query: %+v %v", query, err)
	}
	done := make(chan netproto.RoleChannelResult, 1)
	go func() {
		result, err := app.ChangeRoleChannelForTab("a", netproto.RoleChannelChange{Kind: authorization.ChannelMove, ExpectedRevision: 7, ChannelID: 3, ParentID: 4})
		if err != nil {
			t.Error(err)
		}
		done <- result
	}()
	change := <-received
	if change.ExpectedRevision != 7 || change.SyncToParent || change.ParentID != 4 {
		t.Fatalf("move contract: %+v", change)
	}
	select {
	case <-done:
		t.Fatal("binding returned before server acknowledgement")
	default:
	}
	// A request already sent to A must still receive A's committed result.
	app.tabsMu.Lock()
	_, _, _, ok := app.activateLocked("b")
	app.tabsMu.Unlock()
	if !ok {
		t.Fatal("native activation failed")
	}
	releaseOnce.Do(func() { close(release) })
	if result := <-done; result.Revision != 8 || !result.EnforcementPending {
		t.Fatalf("lost committed acknowledgement: %+v", result)
	}
}
