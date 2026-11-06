package server

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"noxa/internal/netproto"
	"noxa/internal/store"
)

// windowedChat adds history windows (601) to the in-memory chat store.
type windowedChat struct{ *fakeChat }

func (w windowedChat) ChatHistoryAround(ctx context.Context, channelID, aroundID int64, limit int) ([]store.ChatMessage, error) {
	all, err := w.ChatHistory(ctx, channelID, 0, 200) // newest first
	if err != nil {
		return nil, err
	}
	older, newer := limit/2+1, limit-(limit/2+1)
	var out []store.ChatMessage
	var after []store.ChatMessage
	for _, m := range all {
		if m.ID > aroundID {
			after = append(after, m)
		} else if older > 0 {
			out = append(out, m)
			older--
		}
	}
	if len(after) > newer {
		after = after[len(after)-newer:]
	}
	return append(after, out...), nil
}

func (w windowedChat) ChatHistoryAfter(ctx context.Context, channelID, afterID int64, limit int) ([]store.ChatMessage, error) {
	all, err := w.ChatHistory(ctx, channelID, 0, 200)
	if err != nil {
		return nil, err
	}
	var after []store.ChatMessage
	for _, m := range all {
		if m.ID > afterID {
			after = append(after, m)
		}
	}
	if len(after) > limit {
		after = after[len(after)-limit:]
	}
	return after, nil
}

func historyIDs(t *testing.T, resp netproto.ChatHistoryResponse) []int64 {
	t.Helper()
	ids := make([]int64, 0, len(resp.Messages))
	for _, m := range resp.Messages {
		ids = append(ids, m.ID)
	}
	return ids
}

// TestChatHistoryAroundAndAfter checks the window request, its echo, the
// exclusivity of the paging fields and the fallback without store support.
func TestChatHistoryAroundAndAfter(t *testing.T) {
	for _, windows := range []bool{true, false} {
		t.Run(fmt.Sprintf("windows=%v", windows), func(t *testing.T) {
			var chat *fakeChat
			env := startTestEnvDeps(t, nil, nil, func(d *Deps) {
				chat = d.Chat.(*fakeChat)
				if windows {
					d.Chat = windowedChat{chat}
				}
			})
			defer env.stop()
			alice, bob, key, keyID, _ := chatPair(t, env)
			defer func() { _ = alice.Close() }()
			defer func() { _ = bob.Close() }()
			// Seeded directly: ten live sends would hit the chat rate limit.
			for i := range 10 {
				if _, _, err := chat.StoreChatMessage(t.Context(), 1, "user-uid", "bob", sealScopeTest(t, key, fmt.Sprint("m", i)), keyID, 0, ""); err != nil {
					t.Fatal(err)
				}
			}
			send(t, bob, netproto.MsgChatHistory, netproto.ChatHistory{ChannelID: 1})
			var latest netproto.ChatHistoryResponse
			if err := netproto.Decode(readOfType(t, bob, netproto.MsgChatHistoryResponse), &latest); err != nil {
				t.Fatal(err)
			}
			ids := historyIDs(t, latest) // newest first
			if len(ids) != 10 {
				t.Fatalf("latest page = %v", ids)
			}
			slices.Reverse(ids) // oldest first

			send(t, bob, netproto.MsgChatHistory, netproto.ChatHistory{ChannelID: 1, AroundID: ids[3], Limit: 4})
			var around netproto.ChatHistoryResponse
			if err := netproto.Decode(readOfType(t, bob, netproto.MsgChatHistoryResponse), &around); err != nil {
				t.Fatal(err)
			}
			if !windows {
				if around.AroundID != 0 || len(around.Messages) != 4 || around.Messages[0].ID != ids[9] {
					t.Fatalf("fallback = echo %d, ids %v; want the latest page without echo", around.AroundID, historyIDs(t, around))
				}
				return
			}
			want := []int64{ids[4], ids[3], ids[2], ids[1]}
			if around.AroundID != ids[3] || !slices.Equal(historyIDs(t, around), want) {
				t.Fatalf("around = echo %d, ids %v; want %d, %v", around.AroundID, historyIDs(t, around), ids[3], want)
			}
			if len(around.Keys) == 0 {
				t.Fatal("window carries no keys for its bodies")
			}

			send(t, bob, netproto.MsgChatHistory, netproto.ChatHistory{ChannelID: 1, AfterID: ids[7], Limit: 5})
			var after netproto.ChatHistoryResponse
			if err := netproto.Decode(readOfType(t, bob, netproto.MsgChatHistoryResponse), &after); err != nil {
				t.Fatal(err)
			}
			if after.AfterID != ids[7] || !slices.Equal(historyIDs(t, after), []int64{ids[9], ids[8]}) {
				t.Fatalf("after = echo %d, ids %v", after.AfterID, historyIDs(t, after))
			}

			send(t, bob, netproto.MsgChatHistory, netproto.ChatHistory{ChannelID: 1, BeforeID: ids[5], AroundID: ids[3]})
			var e netproto.Error
			if err := netproto.Decode(readOfType(t, bob, netproto.MsgError), &e); err != nil {
				t.Fatal(err)
			}
			if e.Code != errCodeMalformed {
				t.Fatalf("combined cursors: error %d, want malformed", e.Code)
			}
		})
	}
}
