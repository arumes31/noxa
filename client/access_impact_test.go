package main

import (
	"testing"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

func TestChannelAccessPreviewRejectsWrongScope(t *testing.T) {
	for _, response := range []authorization.ChannelAccessImpact{{Revision: 2, ChannelID: 3}, {Revision: 1, ChannelID: 4}} {
		a, _ := newPipedApp(t, func(frame *netproto.Frame) (netproto.MessageType, any, bool) {
			return netproto.MsgChannelAccessImpact, response, true
		})
		a.tabs = map[string]*tabState{"a": {cm: a.cmLoad()}}
		a.activeID = "a"
		query := netproto.ChannelAccessPreview{Change: authorization.RoleChange{Kind: authorization.ChannelAccessSet, ExpectedRevision: 1, Channel: authorization.ChannelPolicy{ChannelID: 3}}, UserIDs: []int64{0}}
		if result, err := a.PreviewChannelAccessForTab("a", query); err == nil || result.Revision != 0 {
			t.Fatalf("mismatched preview accepted: %+v, %v", result, err)
		}
	}
}

func TestChannelTreePreviewRejectsWrongScope(t *testing.T) {
	for _, channelID := range []int64{0, 3} {
		for _, response := range []authorization.ChannelAccessImpact{{Revision: 2, ChannelID: channelID}, {Revision: 1, ChannelID: channelID + 1}} {
			a, _ := newPipedApp(t, func(frame *netproto.Frame) (netproto.MessageType, any, bool) {
				return netproto.MsgChannelAccessImpact, response, true
			})
			a.tabs = map[string]*tabState{"a": {cm: a.cmLoad()}}
			a.activeID = "a"
			kind := authorization.ChannelMove
			if channelID == 0 {
				kind = authorization.ChannelCreate
			}
			query := netproto.ChannelAccessPreview{Tree: &authorization.ChannelTreeChange{Kind: kind, ExpectedRevision: 1, ChannelID: channelID}, UserIDs: []int64{0}}
			if result, err := a.PreviewChannelAccessForTab("a", query); err == nil || result.Revision != 0 {
				t.Fatalf("mismatched tree preview accepted: %+v, %v", result, err)
			}
		}
	}
}

func TestDescendantPreviewRejectsParentResponse(t *testing.T) {
	for _, tree := range []bool{false, true} {
		a, _ := newPipedApp(t, func(frame *netproto.Frame) (netproto.MessageType, any, bool) {
			return netproto.MsgChannelAccessImpact, authorization.ChannelAccessImpact{Revision: 1, ChannelID: 3}, true
		})
		a.tabs = map[string]*tabState{"a": {cm: a.cmLoad()}}
		a.activeID = "a"
		query := netproto.ChannelAccessPreview{ScopeChannelID: 5, UserIDs: []int64{0}}
		if tree {
			query.Tree = &authorization.ChannelTreeChange{Kind: authorization.ChannelMove, ExpectedRevision: 1, ChannelID: 3}
		} else {
			query.Change = authorization.RoleChange{Kind: authorization.ChannelAccessSet, ExpectedRevision: 1, Channel: authorization.ChannelPolicy{ChannelID: 3}}
		}
		if _, err := a.PreviewChannelAccessForTab("a", query); err == nil {
			t.Fatal("parent response accepted for descendant")
		}
	}
}
