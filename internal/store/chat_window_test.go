package store

import (
	"context"
	"slices"
	"testing"
)

// TestChatHistoryWindows covers the window around a message and forward
// paging (601): order, bounds, the centring split and channel isolation.
func TestChatHistoryWindows(t *testing.T) {
	s := testDBStore(t)
	ctx := context.Background()
	scope := testChatScope(t, s)
	other := testChatScope(t, s)
	key := testScopeKey(21)
	t.Cleanup(func() {
		_, _ = s.DB().ExecContext(ctx, `DELETE FROM chat_messages WHERE channel_id = ANY($1)`, []int64{scope, other})
	})
	ids := make([]int64, 0, 30)
	for i := range 30 {
		id, _, err := s.StoreChatMessage(ctx, scope, "u1", "nick", sealTest(t, key, "m"), 1, 0, "")
		if err != nil {
			t.Fatalf("StoreChatMessage %d: %v", i, err)
		}
		ids = append(ids, id)
		// Interleave another channel so id ranges are not contiguous.
		if _, _, err := s.StoreChatMessage(ctx, other, "u1", "nick", sealTest(t, key, "x"), 1, 0, ""); err != nil {
			t.Fatalf("StoreChatMessage other: %v", err)
		}
	}
	got := func(msgs []ChatMessage) []int64 {
		out := make([]int64, 0, len(msgs))
		for _, m := range msgs {
			if m.ChannelID != scope {
				t.Fatalf("message %d reports channel %d", m.ID, m.ChannelID)
			}
			out = append(out, m.ID)
		}
		return out
	}
	reversed := func(in []int64) []int64 {
		out := slices.Clone(in)
		slices.Reverse(out)
		return out
	}

	around, err := s.ChatHistoryAround(ctx, scope, ids[10], 10)
	if err != nil {
		t.Fatal(err)
	}
	// limit 10: the target and 5 older, then 4 newer; newest first.
	if want := reversed(ids[5:15]); !slices.Equal(got(around), want) {
		t.Fatalf("around = %v, want %v", got(around), want)
	}

	// Near the oldest message the window is just shorter.
	edge, err := s.ChatHistoryAround(ctx, scope, ids[1], 10)
	if err != nil {
		t.Fatal(err)
	}
	if want := reversed(ids[0:6]); !slices.Equal(got(edge), want) {
		t.Fatalf("around oldest = %v, want %v", got(edge), want)
	}

	// A deleted or never-existing id still centres on its neighbours.
	missing, err := s.ChatHistoryAround(ctx, scope, ids[20]+1, 4)
	if err != nil {
		t.Fatal(err)
	}
	if want := reversed(ids[18:22]); !slices.Equal(got(missing), want) {
		t.Fatalf("around missing = %v, want %v", got(missing), want)
	}

	after, err := s.ChatHistoryAfter(ctx, scope, ids[25], 10)
	if err != nil {
		t.Fatal(err)
	}
	if want := reversed(ids[26:30]); !slices.Equal(got(after), want) {
		t.Fatalf("after = %v, want %v", got(after), want)
	}
	page, err := s.ChatHistoryAfter(ctx, scope, ids[0], 3)
	if err != nil {
		t.Fatal(err)
	}
	if want := reversed(ids[1:4]); !slices.Equal(got(page), want) {
		t.Fatalf("after page = %v, want %v", got(page), want)
	}

	// Out-of-range limits fall back to a normal page.
	big, err := s.ChatHistoryAround(ctx, scope, ids[29], 10_000)
	if err != nil || len(big) != 26 {
		t.Fatalf("around with huge limit = %d rows, err=%v; want 26 (26 older incl. target, limit 50)", len(big), err)
	}
}
