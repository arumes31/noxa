//go:build integration

package server

import (
	"encoding/json"
	"testing"

	"noxa/internal/netproto"
)

func TestIntegrationDiscussionEncryptedTCPRoundTrip(t *testing.T) {
	db := integrationManagementStore(t)
	if _, err := db.DB().ExecContext(t.Context(), `INSERT INTO channels(id,name,channel_type) VALUES(1,'Discussion integration',2) ON CONFLICT(id) DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Chat = db; d.ScopeKeys = db })
	defer env.stop()
	writer, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = writer.Close() }()
	reader, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = reader.Close() }()
	writerPub, _ := testX25519(t)
	publishKey(t, writer, writerPub)
	readerPub, readerPriv := testX25519(t)
	publishKey(t, reader, readerPub)
	keyID, key, err := env.srv.chatKeys.EnsureScope(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	plain := "Encrypted thread post over actual TCP"
	request := netproto.DiscussionRequest{Action: "create", ChannelID: 1, Title: "TCP forum", BodyEnc: sealScopeTest(t, key, plain), KeyID: keyID, RequestID: "native-create-1"}
	send(t, writer, netproto.MsgDiscussionRequest, request)
	var created netproto.DiscussionResult
	if err := netproto.Decode(readOfType(t, writer, netproto.MsgDiscussionResult), &created); err != nil || created.ThreadID == 0 {
		t.Fatalf("create response=%+v error=%v", created, err)
	}
	var stored string
	if err := db.DB().QueryRowContext(t.Context(), `SELECT body_enc FROM discussion_messages WHERE thread_id=$1`, created.ThreadID).Scan(&stored); err != nil || stored != request.BodyEnc || stored == plain {
		t.Fatalf("stored body is not original ciphertext: %v", err)
	}
	send(t, reader, netproto.MsgDiscussionRequest, netproto.DiscussionRequest{Action: "get", ChannelID: 1, ThreadID: created.ThreadID})
	var read netproto.DiscussionResult
	if err := netproto.Decode(readOfType(t, reader, netproto.MsgDiscussionResult), &read); err != nil || len(read.Messages) != 1 || len(read.Keys) != 1 {
		t.Fatalf("history response=%+v error=%v", read, err)
	}
	var recipientKey [32]byte
	copy(recipientKey[:], unseal(t, read.Keys[0], readerPub, readerPriv))
	if got := openScopeTest(t, recipientKey, read.Messages[0].BodyEnc); got != plain {
		t.Fatalf("recipient plaintext=%q", got)
	}
	if read.Messages[0].Body != "" {
		t.Fatal("plaintext leaked on server wire")
	}
	// Following must deliver updates independently of a channel subscription or
	// voice membership. Both test clients remain outside every voice channel.
	send(t, reader, netproto.MsgDiscussionRequest, netproto.DiscussionRequest{Action: "join", ChannelID: 1, ThreadID: created.ThreadID})
	readOfType(t, reader, netproto.MsgDiscussionResult)
	send(t, writer, netproto.MsgDiscussionRequest, netproto.DiscussionRequest{Action: "send", ChannelID: 1, ThreadID: created.ThreadID, BodyEnc: sealScopeTest(t, key, "Followed update"), KeyID: keyID, RequestID: "followed-update-1"})
	readOfType(t, writer, netproto.MsgDiscussionResult)
	var notice struct {
		ChannelID  int64 `json:"channel_id"`
		ThreadID   int64 `json:"thread_id"`
		NewMessage bool  `json:"new_message"`
	}
	if err := json.Unmarshal(readEventOfType(t, reader, "discussion_changed"), &notice); err != nil || notice.ChannelID != 1 || notice.ThreadID != created.ThreadID || !notice.NewMessage {
		t.Fatalf("independent follower notification=%+v error=%v", notice, err)
	}
	// The server rejects a body offered in plaintext before touching storage.
	request.RequestID = "plaintext-attempt"
	request.Text = plain
	send(t, writer, netproto.MsgDiscussionRequest, request)
	readError(t, writer)
	var count int
	if err := db.DB().QueryRowContext(t.Context(), `SELECT count(*) FROM discussion_threads`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("rejected plaintext mutated storage: count=%d error=%v", count, err)
	}
	// A thread ID never substitutes for the caller's parent-channel authorization.
	send(t, reader, netproto.MsgDiscussionRequest, netproto.DiscussionRequest{Action: "get", ChannelID: 999, ThreadID: created.ThreadID})
	readError(t, reader)
}
