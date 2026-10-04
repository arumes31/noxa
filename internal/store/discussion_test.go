package store

import (
	"database/sql"
	"errors"
	"testing"

	"noxa/internal/netproto"
)

func TestDiscussionDeletionScopesTombstonesAndRetries(t *testing.T) {
	s := pollTestStore(t)
	var channel, otherChannel, sourceID int64
	for _, id := range []*int64{&channel, &otherChannel} {
		if err := s.db.QueryRowContext(t.Context(), `INSERT INTO channels(name,channel_type) VALUES('Moderation',2) RETURNING id`).Scan(id); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.db.QueryRowContext(t.Context(), `INSERT INTO chat_messages(scope,channel_id,from_unique_id,body_enc,key_id) VALUES(1,$1,'author','source ciphertext',1) RETURNING id`, channel).Scan(&sourceID); err != nil {
		t.Fatal(err)
	}
	call := func(r netproto.DiscussionRequest, uid string) netproto.DiscussionResult {
		t.Helper()
		out, err := s.Discussion(t.Context(), r, uid, uid)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	create := netproto.DiscussionRequest{Action: "create", ChannelID: channel, RootMessageID: sourceID, Title: "Remove this thread", RequestID: "create-1", BodyEnc: "first ciphertext", KeyID: 1}
	created := call(create, "author")
	id := created.ThreadID
	other := create
	other.RequestID = "create-2"
	second := call(other, "author")
	remove := netproto.DiscussionRequest{Action: "delete_message", ChannelID: channel, ThreadID: id, MessageID: second.Messages[0].ID}
	if _, err := s.Discussion(t.Context(), remove, "moderator", "Moderator"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-thread deletion: %v", err)
	}
	remove.ChannelID, remove.MessageID = otherChannel, created.Messages[0].ID
	if _, err := s.Discussion(t.Context(), remove, "moderator", "Moderator"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-channel deletion: %v", err)
	}
	remove.ChannelID = channel
	call(netproto.DiscussionRequest{Action: "join", ChannelID: channel, ThreadID: id}, "follower")
	write := netproto.DiscussionRequest{Action: "send", ChannelID: channel, ThreadID: id, RequestID: "reply-1", BodyEnc: "reply ciphertext", KeyID: 1}
	reply := call(write, "author")
	remove.MessageID = reply.Messages[0].ID
	call(netproto.DiscussionRequest{Action: "archive", ChannelID: channel, ThreadID: id}, "author")
	deleted := call(remove, "moderator") // Nonmember moderation also works on archived threads.
	if !deleted.Messages[0].Deleted || deleted.Messages[0].BodyEnc != "" || deleted.Threads[0].MessageCount != 1 {
		t.Fatalf("message deletion did not clear content/count: %+v", deleted)
	}
	call(remove, "moderator") // Retrying deletion is harmless.
	follower := call(netproto.DiscussionRequest{Action: "state", ChannelID: channel, ThreadID: id}, "follower")
	if follower.Threads[0].Unread {
		t.Fatal("deleted reply remains unread")
	}
	call(netproto.DiscussionRequest{Action: "reopen", ChannelID: channel, ThreadID: id}, "author")
	retried := call(write, "author")
	if len(retried.Messages) != 2 || !retried.Messages[0].Deleted || retried.Messages[0].BodyEnc != "" {
		t.Fatalf("retry resurrected removed reply: %+v", retried)
	}
	wrong := netproto.DiscussionRequest{Action: "delete", ChannelID: otherChannel, ThreadID: id}
	if _, err := s.Discussion(t.Context(), wrong, "moderator", "Moderator"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-channel thread deletion: %v", err)
	}
	call(netproto.DiscussionRequest{Action: "delete", ChannelID: channel, ThreadID: id}, "moderator")
	for _, action := range []string{"get", "history", "state", "join", "reopen", "send"} {
		r := write
		r.Action = action
		if _, err := s.Discussion(t.Context(), r, "author", "Author"); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("deleted thread accepts %s: %v", action, err)
		}
	}
	if _, err := s.Discussion(t.Context(), create, "author", "Author"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("retry resurrected removed thread: %v", err)
	}
	for _, archived := range []bool{false, true} {
		list := call(netproto.DiscussionRequest{Action: "list", ChannelID: channel, Archived: archived}, "moderator")
		for _, thread := range list.Threads {
			if thread.ID == id {
				t.Fatal("deleted thread still listed")
			}
		}
	}
	var remains bool
	if err := s.db.QueryRowContext(t.Context(), `SELECT EXISTS(SELECT 1 FROM discussion_members WHERE thread_id=$1) OR EXISTS(SELECT 1 FROM discussion_messages WHERE thread_id=$1 AND (body_enc<>'' OR deleted_at IS NULL)) OR EXISTS(SELECT 1 FROM discussion_threads WHERE id=$1 AND (title<>'' OR cardinality(tags)>0 OR deleted_at IS NULL))`, id).Scan(&remains); err != nil || remains {
		t.Fatalf("deleted thread retains content or memberships: %t, %v", remains, err)
	}
	if err := s.db.QueryRowContext(t.Context(), `SELECT EXISTS(SELECT 1 FROM chat_messages WHERE id=$1 AND body_enc='source ciphertext' AND deleted_at IS NULL)`, sourceID).Scan(&remains); err != nil || !remains {
		t.Fatalf("source channel message was changed: %t, %v", remains, err)
	}
	untouched := call(netproto.DiscussionRequest{Action: "get", ChannelID: channel, ThreadID: second.ThreadID}, "moderator")
	if len(untouched.Messages) != 1 || untouched.Messages[0].Deleted {
		t.Fatal("another thread was changed")
	}
}

func TestDiscussionPersistenceMembershipArchiveAndScope(t *testing.T) {
	s := pollTestStore(t)
	var channel int64
	if err := s.db.QueryRowContext(t.Context(), `INSERT INTO channels(name,channel_type) VALUES('Forum',2) RETURNING id`).Scan(&channel); err != nil {
		t.Fatal(err)
	}
	call := func(r netproto.DiscussionRequest, uid string) netproto.DiscussionResult {
		t.Helper()
		r.ChannelID = channel
		out, err := s.Discussion(t.Context(), r, uid, uid)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	call(netproto.DiscussionRequest{Action: "configure", Forum: true, Tags: []string{"Help", "Solved"}}, "author")
	create := netproto.DiscussionRequest{Action: "create", Title: "Audio help", Tags: []string{"Help"}, RequestID: "create-1", BodyEnc: "ciphertext", KeyID: 7}
	created := call(create, "author")
	if !created.Forum || len(created.Threads) != 1 || !created.Threads[0].Joined || !created.Threads[0].Subscribed || len(created.Messages) != 1 || created.Messages[0].BodyEnc != "ciphertext" {
		t.Fatalf("create=%+v", created)
	}
	id := created.ThreadID
	if duplicate := call(create, "author"); duplicate.ThreadID != id || duplicate.Threads[0].MessageCount != 1 {
		t.Fatalf("retry duplicated: %+v", duplicate)
	}
	other := testAdditionalStore(t, s)
	loaded, err := other.Discussion(t.Context(), netproto.DiscussionRequest{Action: "list", ChannelID: channel}, "reader", "Reader")
	if err != nil || len(loaded.Threads) != 1 || loaded.Threads[0].Joined {
		t.Fatalf("persistent listing=%+v err=%v", loaded, err)
	}
	write := netproto.DiscussionRequest{Action: "send", ChannelID: channel, ThreadID: id, RequestID: "reply-1", BodyEnc: "encrypted reply", KeyID: 7}
	if _, err = s.Discussion(t.Context(), write, "reader", "Reader"); !errors.Is(err, netproto.ErrDiscussionMembership) {
		t.Fatalf("nonmember write=%v", err)
	}
	call(netproto.DiscussionRequest{Action: "join", ThreadID: id}, "reader")
	call(write, "reader")
	call(netproto.DiscussionRequest{Action: "edit", ThreadID: id, Title: "Audio fixed", Tags: []string{"Solved"}}, "author")
	call(netproto.DiscussionRequest{Action: "pin", ThreadID: id, Pinned: true}, "author")
	updated := call(netproto.DiscussionRequest{Action: "resolve", ThreadID: id, Resolved: true}, "author")
	if !updated.Threads[0].Pinned || !updated.Threads[0].Resolved || updated.Threads[0].Title != "Audio fixed" {
		t.Fatalf("metadata=%+v", updated.Threads[0])
	}
	call(netproto.DiscussionRequest{Action: "edit", ThreadID: id, Title: "Audio help", Tags: []string{"Help"}}, "author")
	write.RequestID = "unread-after-edit"
	call(write, "reader")
	call(netproto.DiscussionRequest{Action: "history", ThreadID: id}, "author")
	state := call(netproto.DiscussionRequest{Action: "state", ThreadID: id}, "author")
	if !state.Threads[0].Unread || len(state.Messages) != 0 {
		t.Fatal("metadata state must preserve unread and omit messages")
	}
	if again := call(netproto.DiscussionRequest{Action: "state", ThreadID: id}, "author"); !again.Threads[0].Unread {
		t.Fatal("state read cleared unread")
	}
	call(netproto.DiscussionRequest{Action: "subscribe", ThreadID: id, Subscribed: false}, "reader")
	if followed := call(netproto.DiscussionRequest{Action: "list", Subscribed: true}, "reader"); len(followed.Threads) != 0 {
		t.Fatal("unfollowed post included in subscription listing")
	}
	if followed := call(netproto.DiscussionRequest{Action: "list", Subscribed: true}, "author"); len(followed.Threads) != 1 {
		t.Fatal("followed post missing from subscription listing")
	}
	if got := call(netproto.DiscussionRequest{Action: "get", ThreadID: id}, "reader"); got.Threads[0].Subscribed || len(got.Messages) != 3 {
		t.Fatalf("subscription/message state=%+v", got)
	}
	call(netproto.DiscussionRequest{Action: "archive", ThreadID: id}, "author")
	write.RequestID = "reply-2"
	if _, err = s.Discussion(t.Context(), write, "reader", "Reader"); !errors.Is(err, netproto.ErrDiscussionArchived) {
		t.Fatalf("archived write=%v", err)
	}
	if got := call(netproto.DiscussionRequest{Action: "list"}, "author"); len(got.Threads) != 0 {
		t.Fatal("archived thread in active list")
	}
	call(netproto.DiscussionRequest{Action: "reopen", ThreadID: id}, "author")
	call(write, "reader")
	call(netproto.DiscussionRequest{Action: "leave", ThreadID: id}, "reader")
	if got := call(netproto.DiscussionRequest{Action: "get", ThreadID: id}, "reader"); got.Threads[0].Joined || got.Threads[0].Subscribed {
		t.Fatal("leave retained membership")
	}
	if got := call(netproto.DiscussionRequest{Action: "list", Tags: []string{"Solved"}}, "reader"); len(got.Threads) != 0 {
		t.Fatal("tag filter leaked unrelated post")
	}
	if _, err = s.Discussion(t.Context(), netproto.DiscussionRequest{Action: "get", ChannelID: channel + 999, ThreadID: id}, "reader", "Reader"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-channel read=%v", err)
	}
	if _, err = s.db.ExecContext(t.Context(), `INSERT INTO discussion_messages(thread_id,from_unique_id,from_nickname,body_enc,key_id,request_id) SELECT $1,'bulk','Bulk','ciphertext',7,'page-'||n FROM generate_series(1,55) n`, id); err != nil {
		t.Fatal(err)
	}
	page := call(netproto.DiscussionRequest{Action: "get", ThreadID: id}, "author")
	if len(page.Messages) != 50 || !page.HasMore {
		t.Fatalf("bounded page=%+v", page)
	}
	older := call(netproto.DiscussionRequest{Action: "get", ThreadID: id, BeforeID: page.Messages[49].ID}, "author")
	if len(older.Messages) != 9 || older.HasMore || older.Messages[0].ID >= page.Messages[49].ID {
		t.Fatalf("older page=%+v", older)
	}
	if _, err = s.db.ExecContext(t.Context(), `DELETE FROM channels WHERE id=$1`, channel); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM discussion_messages WHERE thread_id=$1`, id).Scan(&count); err != nil || count != 0 {
		t.Fatalf("cascade count=%d error=%v", count, err)
	}
}
