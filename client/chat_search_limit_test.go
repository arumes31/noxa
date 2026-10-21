package main

import (
	"noxa/internal/netproto"
	"testing"
)

func TestChatSearchRespectsRemainingBudget(t *testing.T) {
	for _, count := range []int{2, 5} {
		t.Run(string(rune('0'+count)), func(t *testing.T) {
			requested := 0
			app, _ := newPipedApp(t, func(f *netproto.Frame) (netproto.MessageType, any, bool) {
				if netproto.MessageType(f.Type) != netproto.MsgChatHistory {
					return 0, nil, false
				}
				var request netproto.ChatHistory
				if err := netproto.Decode(f, &request); err != nil {
					return 0, nil, false
				}
				requested = request.Limit
				messages := make([]netproto.ChatHistoryEntry, count)
				for i := range messages {
					messages[i] = netproto.ChatHistoryEntry{ID: int64(100 - i), Deleted: true}
				}
				return netproto.MsgChatHistoryResponse, netproto.ChatHistoryResponse{ChannelID: 7, Messages: messages}, true
			})
			result, err := app.ChatSearch(7, "needle", 3)
			if err != nil {
				t.Fatal(err)
			}
			if requested != 3 {
				t.Fatalf("requested %d messages, budget is 3", requested)
			}
			want := min(count, 3)
			if result.Scanned != want || result.Complete != (count < 3) {
				t.Fatalf("result = %+v, want scanned=%d complete=%v", result, want, count < 3)
			}
		})
	}
}
