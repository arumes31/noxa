package store

import (
	"errors"
	"slices"
	"testing"
)

func TestBulkDeleteChatMessages(t *testing.T) {
	s := testScratchStore(t)
	if err := s.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	ctx := t.Context()
	store := func(channel int64, author, clientID string) int64 {
		t.Helper()
		id, _, err := s.StoreChatMessage(ctx, channel, author, author, sealTest(t, testScopeKey(11), "hello"), 1, 0, clientID)
		if err != nil {
			t.Fatalf("StoreChatMessage: %v", err)
		}
		return id
	}
	var channel int64
	if err := s.DB().QueryRowContext(ctx, `INSERT INTO channels(name,channel_type) VALUES('bulk delete',2) RETURNING id`).Scan(&channel); err != nil {
		t.Fatal(err)
	}
	own := store(0, "bot", "a")
	other := store(0, "alice", "b")
	elsewhere := store(channel, "alice", "c")
	gone := store(0, "alice", "d")
	if err := s.DeleteChatMessage(ctx, gone); err != nil {
		t.Fatal(err)
	}

	refused := errors.New("refused")
	var seen map[int64]string
	_, err := s.BulkDeleteChatMessages(ctx, 0, []int64{own, other, elsewhere, gone, 999999999}, func(authors map[int64]string) error {
		seen = authors
		return refused
	})
	if !errors.Is(err, refused) {
		t.Fatalf("authorize error not returned: %v", err)
	}
	if len(seen) != 2 || seen[own] != "bot" || seen[other] != "alice" {
		t.Fatalf("authorize saw %v, want only the two live messages of channel 0", seen)
	}
	if msg, _ := s.GetChatMessage(ctx, other); msg == nil || msg.DeletedAt != nil {
		t.Fatal("a refused bulk delete changed a message")
	}

	deleted, err := s.BulkDeleteChatMessages(ctx, 0, []int64{other, own, elsewhere, gone}, func(map[int64]string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	want := []int64{own, other}
	slices.Sort(want)
	if !slices.Equal(deleted, want) {
		t.Fatalf("deleted = %v, want %v", deleted, want)
	}
	for _, id := range want {
		msg, err := s.GetChatMessage(ctx, id)
		if err != nil || msg == nil || msg.DeletedAt == nil || msg.BodyEnc != "" {
			t.Fatalf("message %d not tombstoned: %+v, %v", id, msg, err)
		}
	}
	if msg, _ := s.GetChatMessage(ctx, elsewhere); msg == nil || msg.DeletedAt != nil {
		t.Fatal("a message of another channel was deleted")
	}
	again, err := s.BulkDeleteChatMessages(ctx, 0, []int64{own}, func(map[int64]string) error {
		t.Fatal("authorize called with nothing to delete")
		return nil
	})
	if err != nil || len(again) != 0 {
		t.Fatalf("second delete = %v, %v; want nothing", again, err)
	}
}
