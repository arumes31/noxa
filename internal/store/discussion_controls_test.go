package store

import (
	"noxa/internal/netproto"
	"testing"
)

func TestDiscussionAutomaticArchiveAndPinnedPagination(t *testing.T) {
	s := pollTestStore(t)
	var ch int64
	if err := s.db.QueryRowContext(t.Context(), `INSERT INTO channels(name,channel_type) VALUES('Controls',2) RETURNING id`).Scan(&ch); err != nil {
		t.Fatal(err)
	}
	call := func(r netproto.DiscussionRequest) netproto.DiscussionResult {
		t.Helper()
		r.ChannelID = ch
		out, err := s.Discussion(t.Context(), r, "owner", "Owner")
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	first := call(netproto.DiscussionRequest{Action: "create", Title: "Pinned old post", RequestID: "1", BodyEnc: "cipher", KeyID: 1})
	call(netproto.DiscussionRequest{Action: "pin", ThreadID: first.ThreadID, Pinned: true})
	second := call(netproto.DiscussionRequest{Action: "create", Title: "Newer post", RequestID: "2", BodyEnc: "cipher", KeyID: 1})
	list := call(netproto.DiscussionRequest{Action: "list"})
	if len(list.Threads) != 2 || list.Threads[0].ID != first.ThreadID {
		t.Fatalf("pin order=%+v", list.Threads)
	}
	list = call(netproto.DiscussionRequest{Action: "list", BeforeID: first.ThreadID, BeforePinned: true})
	if len(list.Threads) != 1 || list.Threads[0].ID != second.ThreadID {
		t.Fatalf("pin cursor=%+v", list.Threads)
	}
	call(netproto.DiscussionRequest{Action: "configure", AutoArchiveHours: 24})
	if _, err := s.db.ExecContext(t.Context(), `UPDATE discussion_threads SET last_activity_at=NOW()-INTERVAL '25 hours' WHERE id=$1`, first.ThreadID); err != nil {
		t.Fatal(err)
	}
	list = call(netproto.DiscussionRequest{Action: "list"})
	if list.AutoArchiveHours != 24 || len(list.Threads) != 1 || list.Threads[0].ID != second.ThreadID {
		t.Fatalf("archive=%+v", list)
	}
	opened := call(netproto.DiscussionRequest{Action: "reopen", ThreadID: first.ThreadID})
	if opened.Threads[0].Archived {
		t.Fatal("reopen failed")
	}
	if got := call(netproto.DiscussionRequest{Action: "get", ThreadID: first.ThreadID}); got.Threads[0].Archived {
		t.Fatal("reopened post immediately archived")
	}
}
