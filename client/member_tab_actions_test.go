package main

import (
	"encoding/json"
	"reflect"
	"testing"

	"noxa/internal/netproto"
)

func TestMemberAndBrandingWritesRejectNativeTabSwitch(t *testing.T) {
	tests := []struct {
		name    string
		kind    netproto.MessageType
		request any
		call    func(*App, string) string
	}{
		{"priority", netproto.MsgPrioritySpeaker, netproto.PrioritySpeaker{Active: true},
			func(a *App, tab string) string { return a.SetPrioritySpeakerForTab(tab, true) }},
		{"whisper", netproto.MsgWhisperSet, netproto.WhisperSet{UniqueIDs: []string{"peer"}, ChannelIDs: []int64{3}, Active: true},
			func(a *App, tab string) string { return a.WhisperSetForTab(tab, []string{"peer"}, []int64{3}, true) }},
		{"share", netproto.MsgScreenShare, netproto.ScreenShare{Active: true, MaxHeight: 720},
			func(a *App, tab string) string { return a.SetScreenShareQualityForTab(tab, true, 720) }},
		{"stop share", netproto.MsgScreenShare, netproto.ScreenShare{Active: false},
			func(a *App, tab string) string { return a.SetScreenShareForTab(tab, false) }},
		{"quality", netproto.MsgVideoQuality, netproto.VideoQuality{Quality: "mid"},
			func(a *App, tab string) string { return a.SetVideoQualityForTab(tab, "mid") }},
		{"WebRTC answer", netproto.MsgWebRTCAnswer, netproto.WebRTCAnswer{SDP: "answer"},
			func(a *App, tab string) string {
				if err := a.WebRTCAnswerForTab(tab, "answer"); err != nil {
					return err.Error()
				}
				return ""
			}},
		{"ICE candidate", netproto.MsgICECandidate, netproto.ICECandidate{Candidate: "candidate", SDPMid: "0", SDPMLineIndex: 1},
			func(a *App, tab string) string {
				if err := a.SendICECandidateForTab(tab, "candidate", "0", 1); err != nil {
					return err.Error()
				}
				return ""
			}},
		{"presence", netproto.MsgSetStatus, netproto.SetStatus{Status: "busy", Message: "meeting"},
			func(a *App, tab string) string { return a.SetStatusForTab(tab, "busy", "meeting") }},
		{"rules acceptance", netproto.MsgServerRulesAccept, netproto.ServerRulesAccept{Hash: "revision-a"},
			func(a *App, tab string) string { return a.AcceptServerRulesForTab(tab, "revision-a") }},
		{"channel subscription", netproto.MsgChannelSubscribe, netproto.ChannelSubscribe{ChannelIDs: []int64{3}, Subscribe: true},
			func(a *App, tab string) string { return a.SubscribeChannelsForTab(tab, []int64{3}, true) }},
		{"chat typing", netproto.MsgTyping, netproto.Typing{ChannelID: 3, ToUniqueID: "peer"},
			func(a *App, tab string) string { return a.SendTypingForTab(tab, 3, "peer") }},
		{"chat delivered", netproto.MsgChatDelivered, netproto.ChatDelivered{ToUniqueID: "peer", ClientMsgID: "message"},
			func(a *App, tab string) string { return a.SendChatDeliveredForTab(tab, "peer", "message") }},
		{"chat read", netproto.MsgChatRead, netproto.ChatRead{ToUniqueID: "peer", ClientMsgID: "message"},
			func(a *App, tab string) string { return a.SendChatReadForTab(tab, "peer", "message") }},
		{"chat delete", netproto.MsgChatDelete, netproto.ChatDelete{MessageID: 17},
			func(a *App, tab string) string { return a.ChatDeleteMessageForTab(tab, 17) }},
		{"chat pin", netproto.MsgChatPin, netproto.ChatPin{ChannelID: 3, MessageID: 17, Pinned: true},
			func(a *App, tab string) string { return a.ChatPinMessageForTab(tab, 3, 17, true) }},
		{"chat reaction", netproto.MsgChatReact, netproto.ChatReact{MessageID: 17, Emoji: "👍"},
			func(a *App, tab string) string { return a.ChatReactForTab(tab, 17, "👍") }},
		{"file deletion", netproto.MsgFileDelete, netproto.FileDelete{ChannelID: 3, Folder: "docs", Name: "report.txt"},
			func(a *App, tab string) string { return a.FileDeleteForTab(tab, 3, "docs", "report.txt") }},
		{"file rename", netproto.MsgFileRename, netproto.FileRename{ChannelID: 3, Folder: "docs", Name: "report.txt", NewFolder: "archive", NewName: "renamed.txt"},
			func(a *App, tab string) string {
				return a.FileRenameForTab(tab, 3, "docs", "report.txt", "archive", "renamed.txt", 0)
			}},
		{"file move", netproto.MsgFileRename, netproto.FileRename{ChannelID: 3, Folder: "docs", Name: "report.txt", NewFolder: "archive", NewName: "report.txt", NewChannelID: 4},
			func(a *App, tab string) string {
				return a.FileRenameForTab(tab, 3, "docs", "report.txt", "archive", "report.txt", 4)
			}},
		{"emoji upload", netproto.MsgEmojiUpload, netproto.EmojiUpload{Name: "wave", DataBase64: "aW1hZ2U="},
			func(a *App, tab string) string { return a.EmojiUploadForTab(tab, "wave", "aW1hZ2U=") }},
		{"emoji rename", netproto.MsgEmojiRename, netproto.EmojiRename{Name: "wave", NewName: "hello"},
			func(a *App, tab string) string { return a.EmojiRenameForTab(tab, "wave", "hello") }},
		{"emoji delete", netproto.MsgEmojiDelete, netproto.EmojiDelete{Name: "wave"},
			func(a *App, tab string) string { return a.EmojiDeleteForTab(tab, "wave") }},
		{"channel icon upload", netproto.MsgChannelIconSet, netproto.ChannelIconSet{ChannelID: 3, DataBase64: "aW1hZ2U="},
			func(a *App, tab string) string { return a.ChannelIconSetForTab(tab, 3, "aW1hZ2U=", 0) }},
		{"channel icon copy", netproto.MsgChannelIconSet, netproto.ChannelIconSet{ChannelID: 3, CopyFromChannelID: 4},
			func(a *App, tab string) string { return a.ChannelIconSetForTab(tab, 3, "", 4) }},
		{"join", netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: 3},
			func(a *App, tab string) string { return a.JoinChannelForTab(tab, 3) }},
		{"kick", netproto.MsgKickClient, netproto.KickClient{ClientID: "member", FromServer: true, Ban: true, Reason: "reason", DurationSeconds: 60},
			func(a *App, tab string) string { return a.KickClientForTab(tab, "member", true, true, "reason", 60) }},
		{"move", netproto.MsgMoveClient, netproto.MoveClient{ClientID: "member", ChannelID: 3},
			func(a *App, tab string) string { return a.MoveClientForTab(tab, "member", 3) }},
		{"poke", netproto.MsgPoke, netproto.Poke{ClientID: "member", Message: "hello"},
			func(a *App, tab string) string { return a.PokeForTab(tab, "member", "hello") }},
		{"avatar", netproto.MsgAvatarSet, netproto.AvatarSet{DataBase64: "aW1hZ2U="},
			func(a *App, tab string) string { return a.SetAvatarForTab(tab, "aW1hZ2U=") }},
		{"icon", netproto.MsgServerIconSet, netproto.ServerIconSet{DataBase64: "aW1hZ2U="},
			func(a *App, tab string) string { return a.ServerIconSetForTab(tab, "aW1hZ2U=") }},
		{"banner", netproto.MsgServerBannerSet, netproto.ServerBannerSet{DataBase64: "aW1hZ2U="},
			func(a *App, tab string) string { return a.ServerBannerSetForTab(tab, "aW1hZ2U=") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			framesA, framesB := make(chan *netproto.Frame, 4), make(chan *netproto.Frame, 4)
			backend := func(frames chan<- *netproto.Frame) frameHandler {
				return func(frame *netproto.Frame) (netproto.MessageType, any, bool) {
					frames <- frame
					return 0, nil, false
				}
			}
			app, _ := newPipedApp(t, backend(framesA))
			other, _ := newPipedApp(t, backend(framesB))
			app.tabs = map[string]*tabState{"a": {cm: app.cmLoad()}, "b": {cm: other.cmLoad()}}
			app.activeID = "a"
			checkFrame := func(frames <-chan *netproto.Frame) {
				t.Helper()
				var got, want any
				if err := netproto.Decode(nextFrame(t, frames, tt.kind), &got); err != nil {
					t.Fatal(err)
				}
				encoded, err := json.Marshal(tt.request)
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(encoded, &want); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("payload = %#v, want %#v", got, want)
				}
			}
			if err := tt.call(app, "a"); err != "" {
				t.Fatal(err)
			}
			checkFrame(framesA)
			app.tabsMu.Lock()
			_, _, _, ok := app.activateLocked("b")
			app.tabsMu.Unlock()
			if !ok {
				t.Fatal("native activation failed")
			}
			for _, tabID := range []string{"", "a", "missing"} {
				if err := tt.call(app, tabID); err == "" {
					t.Fatalf("accepted stale action for %q", tabID)
				}
			}
			if err := tt.call(app, "b"); err != "" {
				t.Fatal(err)
			}
			checkFrame(framesB)
			for name, frames := range map[string]<-chan *netproto.Frame{"a": framesA, "b": framesB} {
				select {
				case frame := <-frames:
					t.Fatalf("unexpected write to %s: %+v", name, frame)
				default:
				}
			}
		})
	}
}
