package server

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

type gatedChatMutationStore struct {
	*fakeChat
	before func() error
}

func (s *gatedChatMutationStore) EditChatMessage(ctx context.Context, id int64, body string, key uint32, version uint64) (uint64, error) {
	if err := s.before(); err != nil {
		return 0, err
	}
	return s.fakeChat.EditChatMessage(ctx, id, body, key, version)
}

func (s *gatedChatMutationStore) DeleteChatMessage(ctx context.Context, id int64) error {
	if err := s.before(); err != nil {
		return err
	}
	return s.fakeChat.DeleteChatMessage(ctx, id)
}

func (s *gatedChatMutationStore) PinChatMessage(ctx context.Context, channelID, id int64, uid string) error {
	if err := s.before(); err != nil {
		return err
	}
	return s.fakeChat.PinChatMessage(ctx, channelID, id, uid)
}

func (s *gatedChatMutationStore) UnpinChatMessage(ctx context.Context, channelID, id int64) error {
	if err := s.before(); err != nil {
		return err
	}
	return s.fakeChat.UnpinChatMessage(ctx, channelID, id)
}

func (s *gatedChatMutationStore) ToggleReaction(ctx context.Context, id int64, uid, emoji string) (map[string]int, bool, error) {
	if err := s.before(); err != nil {
		return nil, false, err
	}
	return s.fakeChat.ToggleReaction(ctx, id, uid, emoji)
}

type chatMutationFence struct {
	saved  []netproto.ChatMutationSaved
	errors []netproto.Error
	err    error
}

func readChatMutationFence(conn net.Conn, acknowledged chan<- struct{}) chatMutationFence {
	var result chatMutationFence
	for {
		f, err := netproto.ReadFrame(conn)
		if err != nil {
			result.err = err
			return result
		}
		switch netproto.MessageType(f.Type) {
		case netproto.MsgChatMutationSaved:
			var saved netproto.ChatMutationSaved
			if err := netproto.Decode(f, &saved); err != nil {
				result.err = err
				return result
			}
			result.saved = append(result.saved, saved)
			acknowledged <- struct{}{}
		case netproto.MsgError:
			var failure netproto.Error
			if err := netproto.Decode(f, &failure); err != nil {
				result.err = err
				return result
			}
			result.errors = append(result.errors, failure)
		case netproto.MsgPong:
			return result
		}
	}
}

func TestChatMutationAcknowledgementFollowsStorage(t *testing.T) {
	for _, operation := range []struct {
		kind   netproto.MessageType
		pinned bool
	}{
		{netproto.MsgChatEdit, false}, {netproto.MsgChatDelete, false},
		{netproto.MsgChatPin, true}, {netproto.MsgChatPin, false}, {netproto.MsgChatReact, false},
	} {
		kind := operation.kind
		for _, outcome := range []string{"saved", "failed", "denied", "unrequested"} {
			t.Run(kind.String()+"/"+outcome, func(t *testing.T) {
				backend := serverRoleFixture()
				authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
				if err != nil {
					t.Fatal(err)
				}
				entered, release := make(chan struct{}), make(chan struct{})
				var once sync.Once
				unblock := func() { once.Do(func() { close(release) }) }
				defer unblock()
				storage := &gatedChatMutationStore{fakeChat: newFakeChat(), before: func() error {
					close(entered)
					<-release
					if outcome == "failed" {
						return errors.New("storage unavailable")
					}
					return nil
				}}
				env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority; d.Chat = storage })
				defer env.stop()
				// Release a blocked handler before stopping its server, including on failure.
				defer unblock()
				uid := "user-uid"
				if outcome == "denied" {
					uid = "admin-uid"
				}
				conn, _ := dialAuthed(t, env.addr, uid)
				defer func() { _ = conn.Close() }()
				id, _, err := storage.StoreChatMessage(t.Context(), 0, "user-uid", "user", "ciphertext", 1, 0, "")
				if err != nil {
					t.Fatal(err)
				}
				if kind == netproto.MsgChatPin && !operation.pinned {
					if err := storage.fakeChat.PinChatMessage(t.Context(), 0, id, "user-uid"); err != nil {
						t.Fatal(err)
					}
				}
				ack := outcome != "unrequested"
				var msg any
				switch kind {
				case netproto.MsgChatEdit:
					msg = netproto.ChatEdit{AckRequested: ack, MessageID: id, NewText: "changed"}
				case netproto.MsgChatDelete:
					msg = netproto.ChatDelete{AckRequested: ack, MessageID: id}
				case netproto.MsgChatPin:
					msg = netproto.ChatPin{AckRequested: ack, MessageID: id, Pinned: operation.pinned}
				case netproto.MsgChatReact:
					msg = netproto.ChatReact{AckRequested: ack, MessageID: id, Emoji: "👍"}
				}
				send(t, conn, kind, msg)
				send(t, conn, netproto.MsgPing, netproto.Ping{})
				if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
					t.Fatal(err)
				}
				done := make(chan chatMutationFence, 1)
				acknowledged := make(chan struct{}, 4)
				go func() { done <- readChatMutationFence(conn, acknowledged) }()
				if outcome != "denied" {
					select {
					case <-entered:
					case got := <-done:
						t.Fatalf("operation did not reach storage: %+v", got)
					case <-time.After(time.Second):
						t.Fatal("storage not reached")
					}
					select {
					case <-acknowledged:
						t.Fatal("acknowledged before storage completed")
					case got := <-done:
						t.Fatalf("completed before storage: %+v", got)
					case <-time.After(20 * time.Millisecond):
					}
					unblock()
				}
				got := <-done
				if got.err != nil {
					t.Fatal(got.err)
				}
				if outcome == "saved" {
					if len(got.saved) != 1 || got.saved[0] != (netproto.ChatMutationSaved{Operation: kind, MessageID: id}) || len(got.errors) != 0 {
						t.Fatalf("acknowledgement: %+v", got)
					}
				} else if len(got.saved) != 0 {
					t.Fatalf("unexpected success acknowledgement: %+v", got)
				}
				if outcome == "failed" || outcome == "denied" {
					if len(got.errors) != 1 || got.errors[0].OriginType != uint16(kind) {
						t.Fatalf("missing correlated failure: %+v", got)
					}
				} else if len(got.errors) != 0 {
					t.Fatalf("unexpected failure: %+v", got)
				}
				if outcome == "denied" {
					select {
					case <-entered:
						t.Fatal("denial reached storage")
					default:
					}
				}
				storage.mu.Lock()
				changed := false
				switch kind {
				case netproto.MsgChatEdit:
					changed = storage.messages[id].Version > 1
				case netproto.MsgChatDelete:
					changed = storage.messages[id].DeletedAt != nil
				case netproto.MsgChatPin:
					_, pinned := storage.pins[0][id]
					changed = pinned == operation.pinned
				case netproto.MsgChatReact:
					changed = storage.reactions[id]["👍"]["user-uid"]
				}
				storage.mu.Unlock()
				if changed != (outcome == "saved" || outcome == "unrequested") {
					t.Fatalf("storage changed = %v for %s", changed, outcome)
				}
			})
		}
	}
}
