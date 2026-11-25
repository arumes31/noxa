// channeltree_test.go covers the channel-tree helpers: the total sibling
// order (163) and the permission/join-power inheritance chain (157/168).
package state

import (
	"testing"

	"go.uber.org/zap"
)

func treeManager(chans ...*Channel) *Manager {
	m := New(zap.NewNop())
	for _, ch := range chans {
		m.AddChannel(ch)
	}
	return m
}

func orderOf(m *Manager) []int64 {
	var ids []int64
	for _, ch := range m.ChannelTreeOrdered() {
		ids = append(ids, ch.ChannelID)
	}
	return ids
}

// TestChannelTreeOrderedIsTotal verifies siblings sharing an order index still
// have one fixed position: repeated calls must not reshuffle the tree.
func TestChannelTreeOrderedIsTotal(t *testing.T) {
	m := treeManager(
		&Channel{ChannelID: 5, ParentID: 1, OrderIndex: 0},
		&Channel{ChannelID: 3, ParentID: 1, OrderIndex: 0},
		&Channel{ChannelID: 9, ParentID: 1, OrderIndex: 0},
		&Channel{ChannelID: 1, OrderIndex: 2},
		&Channel{ChannelID: 2, OrderIndex: 1},
	)

	want := []int64{2, 1, 3, 5, 9}
	for i := 0; i < 20; i++ {
		got := orderOf(m)
		if len(got) != len(want) {
			t.Fatalf("order = %v, want %v", got, want)
		}
		for j := range want {
			if got[j] != want[j] {
				t.Fatalf("order = %v, want %v", got, want)
			}
		}
	}
}

func TestChannelTreeOrderedReturnsClones(t *testing.T) {
	m := treeManager(&Channel{ChannelID: 1, Name: "original"})
	ordered := m.ChannelTreeOrdered()
	ordered[0].Name = "caller mutation"
	got, ok := m.GetChannel(1)
	if !ok || got.Name != "original" {
		t.Fatalf("ChannelTreeOrdered exposed live channel: %+v", got)
	}
}
