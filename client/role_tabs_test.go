package main

import (
	"encoding/json"
	"reflect"
	"sync/atomic"
	"testing"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

func TestRoleBindingsRejectTabSwitchBeforeFrontendReset(t *testing.T) {
	tests := []struct {
		name              string
		send, reply       netproto.MessageType
		request, response any
		call              func(*App, string) (any, error)
	}{
		{name: "WebRTCOffer", send: netproto.MsgWebRTCOffer, reply: netproto.MsgWebRTCAnswer,
			request: netproto.WebRTCOffer{SDP: "offer", Tracks: []netproto.TrackSlot{{TrackID: "audio", Slot: "mic"}}}, response: netproto.WebRTCAnswer{SDP: "answer"},
			call: func(a *App, tabID string) (any, error) {
				sdp, err := a.WebRTCOfferForTab(tabID, "offer", []netproto.TrackSlot{{TrackID: "audio", Slot: "mic"}})
				return netproto.WebRTCAnswer{SDP: sdp}, err
			}},
		{name: "FileList", send: netproto.MsgFileList, reply: netproto.MsgFileListResponse,
			request: netproto.FileList{ChannelID: 3, Folder: "docs"}, response: netproto.FileListResponse{},
			call: func(a *App, tabID string) (any, error) { return a.FileListForTab(tabID, 3, "docs") }},
		{name: "FileVersions", send: netproto.MsgFileVersions, reply: netproto.MsgFileVersionsResponse,
			request: netproto.FileVersions{ChannelID: 3, Folder: "docs", Name: "report.txt"}, response: netproto.FileVersionsResponse{},
			call: func(a *App, tabID string) (any, error) { return a.FileVersionsForTab(tabID, 3, "docs", "report.txt") }},
		{name: "FileLink", send: netproto.MsgFileLink, reply: netproto.MsgFileLinkResponse,
			request: netproto.FileLink{ChannelID: 3, Folder: "docs", Name: "report.txt"}, response: netproto.FileLinkResponse{Path: "/dl/00112233445566778899aabbccddeeff", Scheme: "https", HealthPort: 12334, ExpiresAt: 1000, SessionBound: true},
			call: func(a *App, tabID string) (any, error) { return a.FileLinkForTab(tabID, 3, "docs", "report.txt") }},
		{name: "DescendantAccessPreview", send: netproto.MsgChannelAccessPreview, reply: netproto.MsgChannelAccessImpact,
			request:  netproto.ChannelAccessPreview{ScopeChannelID: 5, Change: authorization.RoleChange{Kind: authorization.ChannelAccessSet, ExpectedRevision: 1, Channel: authorization.ChannelPolicy{ChannelID: 3}}, UserIDs: []int64{0, 2}},
			response: authorization.ChannelAccessImpact{Revision: 1, ChannelID: 5},
			call: func(a *App, tabID string) (any, error) {
				return a.PreviewChannelAccessForTab(tabID, netproto.ChannelAccessPreview{ScopeChannelID: 5, Change: authorization.RoleChange{Kind: authorization.ChannelAccessSet, ExpectedRevision: 1, Channel: authorization.ChannelPolicy{ChannelID: 3}}, UserIDs: []int64{0, 2}})
			}},
		{name: "DescendantMovePreview", send: netproto.MsgChannelAccessPreview, reply: netproto.MsgChannelAccessImpact,
			request:  netproto.ChannelAccessPreview{ScopeChannelID: 5, Tree: &authorization.ChannelTreeChange{Kind: authorization.ChannelMove, ExpectedRevision: 1, ChannelID: 3, ParentID: 4, SyncToParent: true}, UserIDs: []int64{0, 2}},
			response: authorization.ChannelAccessImpact{Revision: 1, ChannelID: 5},
			call: func(a *App, tabID string) (any, error) {
				return a.PreviewChannelAccessForTab(tabID, netproto.ChannelAccessPreview{ScopeChannelID: 5, Tree: &authorization.ChannelTreeChange{Kind: authorization.ChannelMove, ExpectedRevision: 1, ChannelID: 3, ParentID: 4, SyncToParent: true}, UserIDs: []int64{0, 2}})
			}},
		{name: "ChannelCreatePreview", send: netproto.MsgChannelAccessPreview, reply: netproto.MsgChannelAccessImpact,
			request:  netproto.ChannelAccessPreview{Tree: &authorization.ChannelTreeChange{Kind: authorization.ChannelCreate, ExpectedRevision: 1, ParentID: 3, Access: authorization.ChannelPolicy{ParentID: 3, Synced: true}}, UserIDs: []int64{0, 2}},
			response: authorization.ChannelAccessImpact{Revision: 1, ChannelID: 0},
			call: func(a *App, tabID string) (any, error) {
				return a.PreviewChannelAccessForTab(tabID, netproto.ChannelAccessPreview{Tree: &authorization.ChannelTreeChange{Kind: authorization.ChannelCreate, ExpectedRevision: 1, ParentID: 3, Access: authorization.ChannelPolicy{ParentID: 3, Synced: true}}, UserIDs: []int64{0, 2}})
			}},
		{name: "ChannelMovePreview", send: netproto.MsgChannelAccessPreview, reply: netproto.MsgChannelAccessImpact,
			request:  netproto.ChannelAccessPreview{Tree: &authorization.ChannelTreeChange{Kind: authorization.ChannelMove, ExpectedRevision: 1, ChannelID: 3, ParentID: 4, SyncToParent: true}, UserIDs: []int64{0, 2}},
			response: authorization.ChannelAccessImpact{Revision: 1, ChannelID: 3},
			call: func(a *App, tabID string) (any, error) {
				return a.PreviewChannelAccessForTab(tabID, netproto.ChannelAccessPreview{Tree: &authorization.ChannelTreeChange{Kind: authorization.ChannelMove, ExpectedRevision: 1, ChannelID: 3, ParentID: 4, SyncToParent: true}, UserIDs: []int64{0, 2}})
			}},
		{name: "ChannelAccessPreview", send: netproto.MsgChannelAccessPreview, reply: netproto.MsgChannelAccessImpact,
			request:  netproto.ChannelAccessPreview{Change: authorization.RoleChange{Kind: authorization.ChannelAccessSet, ExpectedRevision: 1, Channel: authorization.ChannelPolicy{ChannelID: 3}}, UserIDs: []int64{0, 2}},
			response: authorization.ChannelAccessImpact{Revision: 1, ChannelID: 3},
			call: func(a *App, tabID string) (any, error) {
				return a.PreviewChannelAccessForTab(tabID, netproto.ChannelAccessPreview{Change: authorization.RoleChange{Kind: authorization.ChannelAccessSet, ExpectedRevision: 1, Channel: authorization.ChannelPolicy{ChannelID: 3}}, UserIDs: []int64{0, 2}})
			}},
		{name: "EmojiList", send: netproto.MsgEmojiList, reply: netproto.MsgEmojiListResponse,
			request: netproto.EmojiList{}, response: netproto.EmojiListResponse{},
			call: func(a *App, tabID string) (any, error) { return a.EmojiListForTab(tabID) }},
		{name: "EmojiGet", send: netproto.MsgEmojiGet, reply: netproto.MsgEmojiData,
			request: netproto.EmojiGet{Name: "wave"}, response: netproto.EmojiData{DataBase64: "aW1hZ2U="},
			call: func(a *App, tabID string) (any, error) { return a.EmojiGetForTab(tabID, "wave") }},
		{name: "GetClientInfo", send: netproto.MsgClientInfoQuery, reply: netproto.MsgClientInfoResponse,
			request: netproto.ClientInfoQuery{ClientID: "member"}, response: netproto.ClientInfoResponse{},
			call: func(a *App, tabID string) (any, error) { return a.GetClientInfoForTab(tabID, "member") }},
		{name: "ServerInfo", send: netproto.MsgServerInfoQuery, reply: netproto.MsgServerInfoResponse,
			request: netproto.ServerInfoQuery{}, response: netproto.ServerInfoResponse{},
			call: func(a *App, tabID string) (any, error) { return a.ServerInfoForTab(tabID) }},
		{name: "ChannelIconGet", send: netproto.MsgChannelIconGet, reply: netproto.MsgChannelIconData,
			request: netproto.ChannelIconGet{ChannelID: 3}, response: netproto.ChannelIconData{},
			call: func(a *App, tabID string) (any, error) { return a.ChannelIconGetForTab(tabID, 3) }},
		{name: "ServerIconGet", send: netproto.MsgServerIconGet, reply: netproto.MsgServerIconData,
			request: netproto.ServerIconGet{}, response: netproto.ServerIconData{DataBase64: "aWNvbg=="},
			call: func(a *App, tabID string) (any, error) { return a.ServerIconGetForTab(tabID) }},
		{name: "ServerBannerGet", send: netproto.MsgServerBannerGet, reply: netproto.MsgServerBannerDat,
			request: netproto.ServerBannerGet{}, response: netproto.ServerBannerData{DataBase64: "YmFubmVy"},
			call: func(a *App, tabID string) (any, error) { return a.ServerBannerGetForTab(tabID) }},
		{name: "BanList", send: netproto.MsgBanList, reply: netproto.MsgBanListResponse,
			request: netproto.BanList{}, response: netproto.BanListResponse{},
			call: func(a *App, tabID string) (any, error) { return a.BanListForTab(tabID) }},
		{name: "RemoveRoleBan", send: netproto.MsgRoleBanRemove, reply: netproto.MsgRoleBanRemoved,
			request: netproto.BanRemove{BanID: 17}, response: netproto.RoleBanRemoved{BanID: 17},
			call: func(a *App, tabID string) (any, error) { return a.RemoveRoleBanForTab(tabID, 17) }},
		{name: "RoleIconUpload", send: netproto.MsgRoleChannelIconSet, reply: netproto.MsgRoleChannelIconSaved,
			request: netproto.ChannelIconSet{ChannelID: 3, DataBase64: "aW1hZ2U="}, response: netproto.RoleChannelIconSaved{ChannelID: 3},
			call: func(a *App, tabID string) (any, error) { return a.SetRoleChannelIconForTab(tabID, 3, "aW1hZ2U=", 0) }},
		{name: "RoleIconCopy", send: netproto.MsgRoleChannelIconSet, reply: netproto.MsgRoleChannelIconSaved,
			request: netproto.ChannelIconSet{ChannelID: 3, CopyFromChannelID: 4}, response: netproto.RoleChannelIconSaved{ChannelID: 3},
			call: func(a *App, tabID string) (any, error) { return a.SetRoleChannelIconForTab(tabID, 3, "", 4) }},
		{name: "ComplaintList", send: netproto.MsgComplaintList, reply: netproto.MsgComplaints,
			request: netproto.ComplaintList{}, response: netproto.Complaints{},
			call: func(a *App, tabID string) (any, error) { return a.ComplaintListForTab(tabID) }},
		{name: "ComplaintClear", send: netproto.MsgComplaintClear, reply: netproto.MsgComplaints,
			request: netproto.ComplaintClear{TargetUniqueID: "member", FromUniqueID: "reporter"}, response: netproto.Complaints{},
			call: func(a *App, tabID string) (any, error) { return a.ComplaintClearForTab(tabID, "member", "reporter") }},
		{name: "ChatFilterGet", send: netproto.MsgChatFilterGet, reply: netproto.MsgChatFilterResponse,
			request: netproto.ChatFilterGet{}, response: netproto.ChatFilterResponse{WordFilter: "old", FromConfig: true},
			call: func(a *App, tabID string) (any, error) { return a.ChatFilterGetForTab(tabID) }},
		{name: "ChatFilterSet", send: netproto.MsgChatFilterSet, reply: netproto.MsgChatFilterResponse,
			request: map[string]string{"word_filter": "", "link_blacklist": "bad.test", "link_whitelist": ""}, response: netproto.ChatFilterResponse{LinkBlacklist: "bad.test"},
			call: func(a *App, tabID string) (any, error) { return a.ChatFilterSetForTab(tabID, "", "bad.test", "") }},
		{name: "AuditLog", send: netproto.MsgAuditLog, reply: netproto.MsgAuditLogResponse,
			request: netproto.AuditLog{BeforeID: 7, Limit: 50}, response: netproto.AuditLogResponse{},
			call: func(a *App, tabID string) (any, error) { return a.AuditLogForTab(tabID, 7, 50) }},
		{name: "RoleChannelState", send: netproto.MsgRoleChannelQuery, reply: netproto.MsgRoleChannelState,
			request: netproto.RoleChannelQuery{Kind: authorization.ChannelMove, ChannelID: 3}, response: netproto.RoleChannelState{Revision: 7, ChannelID: 3},
			call: func(a *App, tabID string) (any, error) {
				return a.RoleChannelStateForTab(tabID, netproto.RoleChannelQuery{Kind: authorization.ChannelMove, ChannelID: 3})
			}},
		{name: "ChangeRoleChannel", send: netproto.MsgRoleChannelChange, reply: netproto.MsgRoleChannelResult,
			request: netproto.RoleChannelChange{Kind: authorization.ChannelMove, ExpectedRevision: 7, ChannelID: 3, ParentID: 4, OrderIndex: new(int32)}, response: netproto.RoleChannelResult{Revision: 8, ChannelID: 3, EnforcementPending: true},
			call: func(a *App, tabID string) (any, error) {
				return a.ChangeRoleChannelForTab(tabID, netproto.RoleChannelChange{Kind: authorization.ChannelMove, ExpectedRevision: 7, ChannelID: 3, ParentID: 4, OrderIndex: new(int32)})
			}},
		{name: "SetMemberVoice", send: netproto.MsgMemberVoiceSet, reply: netproto.MsgMemberVoiceState,
			request: netproto.MemberVoiceSet{ClientID: "member", ChannelID: 3}, response: netproto.MemberVoiceState{Revision: 8, ClientID: "member", ChannelID: 3, Muted: true},
			call: func(a *App, tabID string) (any, error) {
				return a.SetMemberVoiceForTab(tabID, netproto.MemberVoiceSet{ClientID: "member", ChannelID: 3})
			}},
		{name: "RoleMembers", send: netproto.MsgRoleMemberQuery, reply: netproto.MsgRoleMembers,
			request: authorization.MemberQuery{ChannelID: 3, ExpectedRevision: 7, Search: "Alice", AfterID: 2}, response: authorization.MemberPage{Revision: 7, More: true},
			call: func(a *App, tabID string) (any, error) {
				return a.RoleMembersForTab(tabID, authorization.MemberQuery{ChannelID: 3, ExpectedRevision: 7, Search: "Alice", AfterID: 2})
			}},
		{name: "ChatHistory", send: netproto.MsgChatHistory, reply: netproto.MsgChatHistoryResponse,
			request: netproto.ChatHistory{ChannelID: 3, BeforeID: 15, Limit: 50}, response: netproto.ChatHistoryResponse{},
			call: func(a *App, tabID string) (any, error) { return a.ChatHistoryForTab(tabID, 3, 15, 50) }},
		{name: "ChatPins", send: netproto.MsgChatPins, reply: netproto.MsgChatPinsResponse,
			request: netproto.ChatPins{ChannelID: 3}, response: netproto.ChatPinsResponse{},
			call: func(a *App, tabID string) (any, error) { return a.ChatPinsForTab(tabID, 3) }},
		{name: "RoleState", send: netproto.MsgRoleQuery, reply: netproto.MsgRoleState,
			request: netproto.RoleQuery{ChannelID: 3}, response: netproto.RoleState{ActorID: 2},
			call: func(a *App, tabID string) (any, error) { return a.RoleStateForTab(tabID, int64(3)) }},
		{name: "RoleChange", send: netproto.MsgRoleChange, reply: netproto.MsgRoleChangeResult,
			request: authorization.RoleChange{Kind: "owner_transfer", ExpectedRevision: 7, UserID: 2}, response: netproto.RoleChangeResult{Revision: 8, CreatedRoleID: 5, EnforcementPending: true},
			call: func(a *App, tabID string) (any, error) {
				return a.RoleChangeForTab(tabID, authorization.RoleChange{Kind: "owner_transfer", ExpectedRevision: 7, UserID: 2})
			}},
		{name: "CheckAccess", send: netproto.MsgAccessCheck, reply: netproto.MsgAccessCheckResult,
			request: netproto.AccessCheck{UserID: 2, ChannelID: 3, Capability: authorization.ViewChannel, ExpectedRevision: 7}, response: netproto.AccessCheckResult{CanManageMember: true},
			call: func(a *App, tabID string) (any, error) {
				return a.CheckAccessForTab(tabID, netproto.AccessCheck{UserID: 2, ChannelID: 3, Capability: authorization.ViewChannel, ExpectedRevision: 7})
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var writesA, writesB atomic.Int32
			backend := func(count *atomic.Int32) func(*netproto.Frame) (netproto.MessageType, any, bool) {
				return func(frame *netproto.Frame) (netproto.MessageType, any, bool) {
					count.Add(1)
					if frame.Type != uint16(tt.send) {
						t.Errorf("message type = %v, want %v", frame.Type, tt.send)
					}
					var got any
					if err := netproto.Decode(frame, &got); err != nil {
						t.Error(err)
					}
					encoded, err := json.Marshal(tt.request)
					if err != nil {
						t.Error(err)
					}
					var want any
					if err := json.Unmarshal(encoded, &want); err != nil {
						t.Error(err)
					}
					if !reflect.DeepEqual(got, want) {
						t.Errorf("request = %#v, want %#v", got, want)
					}
					return tt.reply, tt.response, true
				}
			}
			app, _ := newPipedApp(t, backend(&writesA))
			other, _ := newPipedApp(t, backend(&writesB))
			app.tabs = map[string]*tabState{"a": {cm: app.cmLoad()}, "b": {cm: other.cmLoad()}, "offline": {}}
			app.activeID = "a"
			if got, err := tt.call(app, "a"); err != nil || !reflect.DeepEqual(got, tt.response) {
				t.Fatalf("current tab: %#v, %v", got, err)
			}
			// Activate B without publishing tab_reset: JavaScript still displays A.
			app.tabsMu.Lock()
			_, _, _, ok := app.activateLocked("b")
			app.tabsMu.Unlock()
			if !ok {
				t.Fatal("native activation failed")
			}
			for _, tabID := range []string{"", "a", "missing", "offline"} {
				if _, err := tt.call(app, tabID); err == nil {
					t.Fatalf("accepted stale request for %q", tabID)
				}
			}
			if writesA.Load() != 1 || writesB.Load() != 0 {
				t.Fatalf("stale requests reached transport: a=%d b=%d", writesA.Load(), writesB.Load())
			}
			if got, err := tt.call(app, "b"); err != nil || !reflect.DeepEqual(got, tt.response) {
				t.Fatalf("new tab: %#v, %v", got, err)
			}
		})
	}
}

func TestRequireTabCMRejectsUnavailableOrMismatchedManager(t *testing.T) {
	connected := &connManager{}
	other := &connManager{}
	for _, tt := range []struct {
		name   string
		active *connManager
		tab    *tabState
	}{
		{"missing", connected, nil},
		{"offline", nil, &tabState{}},
		{"disconnected", nil, &tabState{cm: connected}},
		{"mismatched", other, &tabState{cm: connected}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			app := &App{activeID: "a", tabs: map[string]*tabState{"a": tt.tab}}
			app.cmStore(tt.active)
			if _, err := app.requireTabCM("a"); err == nil {
				t.Fatal("accepted unavailable manager")
			}
		})
	}
}
