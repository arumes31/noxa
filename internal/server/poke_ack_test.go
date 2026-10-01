package server

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"noxa/internal/broadcast"
	"noxa/internal/netproto"
)

type pokeFence struct {
	accepted []netproto.PokeAccepted
	errors   []netproto.Error
}

func readPokeFence(t *testing.T, conn net.Conn) pokeFence {
	t.Helper()
	var result pokeFence
	for {
		f := readFrame(t, conn)
		switch netproto.MessageType(f.Type) {
		case netproto.MsgPokeAccepted:
			var a netproto.PokeAccepted
			if err := netproto.Decode(f, &a); err != nil {
				t.Fatal(err)
			}
			result.accepted = append(result.accepted, a)
		case netproto.MsgError:
			var e netproto.Error
			if err := netproto.Decode(f, &e); err != nil {
				t.Fatal(err)
			}
			result.errors = append(result.errors, e)
		case netproto.MsgPong:
			return result
		}
	}
}

func TestPokeAcknowledgement(t *testing.T) {
	for _, outcome := range []string{"accepted", "unrequested", "denied", "missing", "long message", "cooldown"} {
		t.Run(outcome, func(t *testing.T) {
			authority := chatSendTestAuthority(t)
			env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority })
			defer env.stop()
			senderUID, targetUID := "user-uid", "admin-uid"
			if outcome == "denied" {
				senderUID, targetUID = targetUID, senderUID
			}
			sender, senderID := dialAuthed(t, env.addr, senderUID)
			defer func() { _ = sender.Close() }()
			target, targetID := dialAuthed(t, env.addr, targetUID)
			defer func() { _ = target.Close() }()
			msg := netproto.Poke{AckRequested: outcome != "unrequested", ClientID: targetID, Message: "hello"}
			switch outcome {
			case "missing":
				msg.ClientID = "missing"
			case "long message":
				msg.Message = strings.Repeat("x", maxStatusMessage+1)
			case "cooldown":
				if !env.srv.pokes.allow(senderID+"→"+targetID, time.Now()) {
					t.Fatal("could not prime cooldown")
				}
			}
			send(t, sender, netproto.MsgPoke, msg)
			send(t, sender, netproto.MsgPing, netproto.Ping{})
			got := readPokeFence(t, sender)
			if outcome == "accepted" {
				if len(got.accepted) != 1 || got.accepted[0].ClientID != targetID || len(got.errors) != 0 {
					t.Fatalf("acceptance: %+v", got)
				}
			} else if len(got.accepted) != 0 {
				t.Fatalf("unexpected acceptance: %+v", got)
			}
			if outcome == "accepted" || outcome == "unrequested" {
				if len(got.errors) != 0 {
					t.Fatalf("unexpected error: %+v", got)
				}
				var event pokeEvent
				if err := json.Unmarshal(readEventOfType(t, target, eventPoke), &event); err != nil {
					t.Fatal(err)
				}
				if event.FromClientID != senderID || event.Message != msg.Message {
					t.Fatalf("wrong event: %+v", event)
				}
			} else if len(got.errors) != 1 || got.errors[0].OriginType != uint16(netproto.MsgPoke) {
				t.Fatalf("missing correlated error: %+v", got)
			}
		})
	}
}

func TestPokeAcknowledgementRejectsUnreachableQueue(t *testing.T) {
	for _, outcome := range []string{"full", "unregistered"} {
		t.Run(outcome, func(t *testing.T) {
			env := startTestEnv(t, nil)
			defer env.stop()
			const targetID = "stalled-target"
			if outcome == "full" {
				if _, err := env.deps.Broadcast.Register(targetID); err != nil {
					t.Fatal(err)
				}
				defer env.deps.Broadcast.Unregister(targetID)
				for {
					err := env.deps.Broadcast.BroadcastToClient(targetID, []byte("queued"))
					if errors.Is(err, broadcast.ErrChannelFull) {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			server, conn := net.Pipe()
			defer func() { _ = server.Close(); _ = conn.Close() }()
			sender, target := &Client{ID: "sender", Conn: server}, &Client{ID: targetID}
			done := make(chan error, 1)
			go func() {
				ctx := context.WithValue(t.Context(), requestOriginContextKey{}, netproto.MsgPoke)
				if err := env.srv.sendPoke(ctx, sender, target, netproto.Poke{AckRequested: true, ClientID: targetID}); err != nil {
					done <- err
					return
				}
				done <- env.srv.writeMessage(sender, netproto.MsgPong, netproto.Pong{})
			}()
			got := readPokeFence(t, conn)
			if len(got.accepted) != 0 || len(got.errors) != 1 || got.errors[0].OriginType != uint16(netproto.MsgPoke) {
				t.Fatalf("unreachable result: %+v", got)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}
