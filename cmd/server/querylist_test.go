// querylist_test.go covers channel ordering shared by client and integration snapshots.
package main

import (
	"testing"

	"go.uber.org/zap"

	"noxa/internal/state"
)

// TestListChannelsTotalOrder verifies the state tree's total
// (parent, order index, id) order used by client and integration snapshots.
func TestListChannelsTotalOrder(t *testing.T) {
	sm := state.New(zap.NewNop())
	sm.AddChannel(&state.Channel{ChannelID: 1, Name: "Root", OrderIndex: 5})
	sm.AddChannel(&state.Channel{ChannelID: 2, Name: "First", OrderIndex: 1})
	for _, id := range []int64{30, 10, 20} {
		sm.AddChannel(&state.Channel{ChannelID: id, ParentID: 1, Name: "Child", OrderIndex: 0})
	}

	want := []int64{2, 1, 10, 20, 30}
	// Repeat: an unstable sort on a non-total key only misbehaves sometimes.
	for i := 0; i < 20; i++ {
		got := sm.ChannelTreeOrdered()
		if len(got) != len(want) {
			t.Fatalf("channel tree returned %d rows, want %d", len(got), len(want))
		}
		for j, id := range want {
			if got[j].ChannelID != id {
				t.Fatalf("channel tree order = %v, want %v", channelIDs(got), want)
			}
		}
	}
}

func channelIDs(rows []*state.Channel) []int64 {
	out := make([]int64, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ChannelID)
	}
	return out
}
