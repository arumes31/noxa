package state

import (
	"errors"
	"sync"
	"testing"
)

func TestConcurrentCapacityReservationPreservesRejectedMembership(t *testing.T) {
	m := newTestManager(t)
	m.AddChannel(&Channel{ChannelID: 1})
	m.AddChannel(&Channel{ChannelID: 2, MaxClients: 1})
	for _, id := range []string{"a", "b"} {
		m.AddClient(&Client{ClientID: id})
		if err := m.MoveClient(id, 1); err != nil {
			t.Fatal(err)
		}
	}
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for _, id := range []string{"a", "b"} {
		workers.Go(func() { results <- m.MoveClientWithinCapacity(id, 2) })
	}
	workers.Wait()
	first, second := <-results, <-results
	accepted := first == nil && errors.Is(second, ErrChannelFull) || second == nil && errors.Is(first, ErrChannelFull)
	if !accepted {
		t.Fatalf("capacity results: %v / %v", first, second)
	}
	if len(m.ChannelMembers(1)) != 1 || len(m.ChannelMembers(2)) != 1 {
		t.Fatal("rejected move changed original membership")
	}
	if err := m.MoveClientWithinCapacity(m.ChannelMembers(2)[0].ClientID, 2); err != nil {
		t.Fatalf("idempotent move rejected: %v", err)
	}
}
