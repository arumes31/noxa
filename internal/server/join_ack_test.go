package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

type joinFence struct {
	joined []netproto.ChannelJoined
	errors []netproto.Error
	err    error
}

func readJoinFence(conn net.Conn, accepted chan<- struct{}) joinFence {
	var result joinFence
	for {
		f, err := netproto.ReadFrame(conn)
		if err != nil {
			result.err = err
			return result
		}
		switch netproto.MessageType(f.Type) {
		case netproto.MsgChannelJoined:
			var joined netproto.ChannelJoined
			result.err = netproto.Decode(f, &joined)
			result.joined = append(result.joined, joined)
			accepted <- struct{}{}
		case netproto.MsgError:
			var e netproto.Error
			result.err = netproto.Decode(f, &e)
			result.errors = append(result.errors, e)
		case netproto.MsgPong:
			return result
		}
		if result.err != nil {
			return result
		}
	}
}

func TestJoinAcknowledgementFollowsMembership(t *testing.T) {
	for _, channelID := range []int64{0, 2} {
		for _, outcome := range []string{"done", "failed", "no connect", "unrequested", "already there", "state fallback"} {
			t.Run(fmt.Sprintf("%d/%s", channelID, outcome), func(t *testing.T) {
				backend := serverRoleFixture()
				backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.Connect}
				if outcome == "no connect" {
					backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel}
				}
				backend.policy.Channels = append(backend.policy.Channels, authorization.ChannelPolicy{ChannelID: 2})
				authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
				if err != nil {
					t.Fatal(err)
				}
				entered, release := make(chan struct{}), make(chan struct{})
				var enterOnce, releaseOnce sync.Once
				unblock := func() { releaseOnce.Do(func() { close(release) }) }
				env := startTestEnvDeps(t, nil, nil, func(d *Deps) {
					d.Authority = authority
					if outcome == "state fallback" {
						d.Channels = nil
						return
					}
					d.Channels = &gatedMemberChannels{fakeChannels: d.Channels.(*fakeChannels), before: func(kind string) error {
						if kind != "move" && kind != "disconnect" {
							return nil
						}
						enterOnce.Do(func() { close(entered) })
						<-release
						if outcome == "failed" {
							return errors.New("membership backend failed")
						}
						return nil
					}}
				})
				defer env.stop()
				defer unblock()
				env.state.AddChannel(testChannel(1))
				env.state.AddChannel(testChannel(2))
				conn, id := dialAuthed(t, env.addr, "admin-uid")
				defer func() { _ = conn.Close() }()
				initial := int64(1)
				if outcome == "already there" {
					initial = channelID
				}
				if initial != 0 {
					if err := env.state.MoveClient(id, initial); err != nil {
						t.Fatal(err)
					}
				}
				send(t, conn, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: channelID, AckRequested: outcome != "unrequested"})
				send(t, conn, netproto.MsgPing, netproto.Ping{})
				if err := conn.SetReadDeadline(time.Now().Add(8 * time.Second)); err != nil {
					t.Fatal(err)
				}
				accepted := make(chan struct{}, 4)
				done := make(chan joinFence, 1)
				go func() { done <- readJoinFence(conn, accepted) }()
				denied := outcome == "no connect" && channelID != 0
				if !denied && outcome != "state fallback" {
					select {
					case <-entered:
					case result := <-done:
						t.Fatalf("completed before backend: %+v", result)
					case <-time.After(time.Second):
						t.Fatal("did not reach backend")
					}
					select {
					case <-accepted:
						t.Fatal("acknowledged before membership changed")
					case <-time.After(30 * time.Millisecond):
					}
				}
				unblock()
				result := <-done
				if result.err != nil {
					t.Fatal(result.err)
				}
				success := !denied && outcome != "failed"
				if success && outcome != "unrequested" {
					if len(result.joined) != 1 || result.joined[0].ClientID != id || result.joined[0].ChannelID != channelID || len(result.errors) != 0 {
						t.Fatalf("wrong membership result: %+v", result)
					}
				} else if len(result.joined) != 0 || (len(result.errors) == 0) != success {
					t.Fatalf("unexpected result: %+v", result)
				}
				want := initial
				if success {
					want = channelID
				}
				member, ok := env.state.GetClient(id)
				if !ok || member.ChannelID != want {
					t.Fatalf("membership: %+v, want channel %d", member, want)
				}
			})
		}
	}
}
