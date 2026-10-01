package server

import (
	"context"
	"encoding/json"
	"math"
	"sync"
	"testing"
	"time"

	"noxa/internal/netproto"
	"noxa/internal/store"
)

type recordingSpoolLookup struct {
	SpoolStore
	users []int64
}

func (s *recordingSpoolLookup) PendingMessages(_ context.Context, userID int64) ([]store.SpooledMessage, error) {
	s.users = append(s.users, userID)
	return nil, nil
}

func TestSpooledDeliveryRequiresPositiveUserID(t *testing.T) {
	for _, tc := range []struct {
		name   string
		id     int64
		lookup bool
	}{
		{"negative", -1, false},
		{"minimum", math.MinInt64, false},
		{"guest", 0, false},
		{"registered", 1, true},
		{"maximum", math.MaxInt64, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := startTestEnv(t, nil)
			defer env.stop()
			spool := &recordingSpoolLookup{}
			env.srv.deps.Spool = spool
			env.srv.deliverSpooled(t.Context(), &Client{ID: "test"}, tc.id)
			if (len(spool.users) == 1) != tc.lookup {
				t.Fatalf("spool lookups = %v, expected lookup %v", spool.users, tc.lookup)
			}
		})
	}
}

func TestDirectMessageBindsAuthenticatedDeviceKeys(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	sender, senderID := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = sender.Close() }()
	receiver, receiverID := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = receiver.Close() }()
	other, otherID := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = other.Close() }()
	senderKey, _ := testX25519(t)
	receiverKey, _ := testX25519(t)
	otherKey, _ := testX25519(t)
	env.state.SetE2EPublicKey(senderID, b64e(senderKey[:]))
	env.state.SetE2EPublicKey(receiverID, b64e(receiverKey[:]))
	env.state.SetE2EPublicKey(otherID, b64e(otherKey[:]))
	send(t, sender, netproto.MsgChatSend, map[string]any{
		"ack_requested": true, "client_msg_id": "bound-one", "to_unique_id": "user-uid", "enc": true,
		"text": "ciphertext", "recipient_public_key": b64e(receiverKey[:]), "sender_public_key": b64e(otherKey[:]),
	})
	f := readOfType(t, sender, netproto.MsgEvent)
	var event struct {
		Type string         `json:"type"`
		Data map[string]any `json:"data"`
	}
	for {
		if err := netproto.Decode(f, &event); err != nil {
			t.Fatal(err)
		}
		if event.Type == eventChat {
			break
		}
		f = readOfType(t, sender, netproto.MsgEvent)
	}
	if event.Data["sender_public_key"] != b64e(senderKey[:]) || event.Data["recipient_public_key"] != b64e(receiverKey[:]) {
		encoded, _ := json.Marshal(event.Data)
		t.Fatalf("authenticated key binding missing: %s", encoded)
	}
	readOfType(t, sender, netproto.MsgChatAccepted)
	for {
		f = readOfType(t, receiver, netproto.MsgEvent)
		if err := netproto.Decode(f, &event); err != nil {
			t.Fatal(err)
		}
		if event.Type == eventChat {
			break
		}
	}
	if event.Data["client_msg_id"] != "bound-one" {
		t.Fatalf("wrong recipient message: %+v", event.Data)
	}
}

func TestStaleDirectMessageKeyCannotRelayOrAcknowledge(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	sender, senderID := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = sender.Close() }()
	receiver, receiverID := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = receiver.Close() }()
	senderKey, _ := testX25519(t)
	receiverKey, _ := testX25519(t)
	staleKey, _ := testX25519(t)
	env.state.SetE2EPublicKey(senderID, b64e(senderKey[:]))
	env.state.SetE2EPublicKey(receiverID, b64e(receiverKey[:]))
	send(t, sender, netproto.MsgChatSend, netproto.ChatSend{AckRequested: true, ClientMsgID: "rejected", ToUniqueID: "user-uid", Enc: true, Text: "ciphertext", RecipientPublicKey: b64e(staleKey[:])})
	send(t, sender, netproto.MsgPing, netproto.Ping{})
	result := readChatSendFence(sender, nil)
	if result.err != nil || len(result.accepted) != 0 || len(result.echoes) != 0 || len(result.errors) != 1 || !result.errors[0].RecipientKeyStale {
		t.Fatalf("unsafe stale rejection: %+v", result)
	}
	pending, err := env.spool.PendingMessages(t.Context(), 2)
	if err != nil || len(pending) != 0 {
		t.Fatalf("rejected send reached spool: %v %v", pending, err)
	}
}

func TestBoundSpoolWaitsForMatchingDevicePublication(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	senderKey, _ := testX25519(t)
	recipientKey, _ := testX25519(t)
	wrongKey, _ := testX25519(t)
	binding := store.DMKeyBinding{SenderPublicKey: b64e(senderKey[:]), RecipientPublicKey: b64e(recipientKey[:]), ClientMsgID: "offline-bound"}
	if err := env.spool.SpoolMessage(t.Context(), 1, 2, "admin-uid", "sealed-body", binding); err != nil {
		t.Fatal(err)
	}
	receiver, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = receiver.Close() }()
	for _, key := range [][32]byte{wrongKey, recipientKey} {
		send(t, receiver, netproto.MsgKeyPublish, netproto.KeyPublish{PublicKey: b64e(key[:])})
		send(t, receiver, netproto.MsgPing, netproto.Ping{})
		fence := readChatSendFence(receiver, nil)
		if fence.err != nil {
			t.Fatal(fence.err)
		}
		pending, err := env.spool.PendingMessages(t.Context(), 2)
		if err != nil {
			t.Fatal(err)
		}
		if key == wrongKey {
			if len(fence.echoes) != 0 || len(pending) != 1 {
				t.Fatalf("wrong device consumed spool: %+v %v", fence, pending)
			}
			continue
		}
		if len(pending) != 0 {
			t.Fatalf("matching key did not consume pending row: %v", pending)
		}
		if len(fence.echoes) == 0 {
			// The broadcaster can enqueue the committed event behind the direct pong.
			for {
				frame := readOfType(t, receiver, netproto.MsgEvent)
				var event struct {
					Type string                 `json:"type"`
					Data netproto.ChatBroadcast `json:"data"`
				}
				if err := netproto.Decode(frame, &event); err != nil {
					t.Fatal(err)
				}
				if event.Type == eventChat {
					fence.echoes = append(fence.echoes, event.Data)
					break
				}
			}
		}
		if len(fence.echoes) != 1 || fence.echoes[0].RecipientPublicKey != binding.RecipientPublicKey || fence.echoes[0].SenderPublicKey != binding.SenderPublicKey || fence.echoes[0].ClientMsgID != binding.ClientMsgID || !fence.echoes[0].Offline {
			t.Fatalf("spooled metadata lost: %+v", fence.echoes)
		}
	}
}

type gatedBoundSpool struct {
	*fakeSpool
	mu               sync.Mutex
	reads, marked    int
	entered, release chan struct{}
}

func (s *gatedBoundSpool) PendingMessages(ctx context.Context, userID int64) ([]store.SpooledMessage, error) {
	rows, err := s.fakeSpool.PendingMessages(ctx, userID)
	s.mu.Lock()
	s.reads++
	first := s.reads == 1
	s.mu.Unlock()
	if first {
		close(s.entered)
		<-s.release
	}
	return rows, err
}

func (s *gatedBoundSpool) MarkMessagesDelivered(ctx context.Context, ids []int64) error {
	s.mu.Lock()
	s.marked += len(ids)
	s.mu.Unlock()
	return s.fakeSpool.MarkMessagesDelivered(ctx, ids)
}

func TestConcurrentBoundSpoolHasOneRecipient(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	firstConn, firstID := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = firstConn.Close() }()
	secondConn, secondID := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = secondConn.Close() }()
	// Complete both login paths before replacing the spool dependency.
	send(t, firstConn, netproto.MsgPing, netproto.Ping{})
	readOfType(t, firstConn, netproto.MsgPong)
	send(t, secondConn, netproto.MsgPing, netproto.Ping{})
	readOfType(t, secondConn, netproto.MsgPong)
	key, _ := testX25519(t)
	senderKey, _ := testX25519(t)
	env.state.SetE2EPublicKey(firstID, b64e(key[:]))
	env.state.SetE2EPublicKey(secondID, b64e(key[:]))
	spool := &gatedBoundSpool{fakeSpool: env.spool, entered: make(chan struct{}), release: make(chan struct{})}
	env.srv.deps.Spool = spool
	if err := spool.SpoolMessage(t.Context(), 1, 2, "admin-uid", "sealed", store.DMKeyBinding{SenderPublicKey: b64e(senderKey[:]), RecipientPublicKey: b64e(key[:])}); err != nil {
		t.Fatal(err)
	}
	first, _ := env.srv.clientByID(firstID)
	second, _ := env.srv.clientByID(secondID)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); env.srv.deliverSpooled(t.Context(), first, 2) }()
	<-spool.entered
	wg.Add(1)
	go func() { defer wg.Done(); env.srv.deliverSpooled(t.Context(), second, 2) }()
	// Widen the read/mark overlap; a second unsynchronized fetch sees the row.
	time.Sleep(25 * time.Millisecond)
	close(spool.release)
	wg.Wait()
	spool.mu.Lock()
	marked := spool.marked
	spool.mu.Unlock()
	if marked != 1 {
		t.Fatalf("same-key sessions consumed the same row %d times", marked)
	}
}
