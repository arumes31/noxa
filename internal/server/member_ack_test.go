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
	"noxa/internal/state"
)

type gatedMemberChannels struct {
	*fakeChannels
	before func(string) error
}

func (b *gatedMemberChannels) MoveClientWithinCapacity(id string, channel int64, after func(int64)) (int64, error) {
	if err := b.before("move"); err != nil {
		return 0, err
	}
	return b.fakeChannels.MoveClientWithinCapacity(id, channel, after)
}

func (b *gatedMemberChannels) LeaveClient(id string) (int64, error) {
	if err := b.before("disconnect"); err != nil {
		return 0, err
	}
	return b.fakeChannels.LeaveClient(id)
}

func (b *gatedMemberChannels) RemoveClient(id string) (*state.Client, error) {
	if err := b.before("kick"); err != nil {
		return nil, err
	}
	return b.fakeChannels.RemoveClient(id)
}

type memberActionFence struct {
	moved   []netproto.ClientMoved
	removed []netproto.ClientRemoved
	errors  []netproto.Error
	err     error
}

func readMemberActionFence(conn net.Conn, acknowledged chan<- struct{}) memberActionFence {
	var result memberActionFence
	for {
		f, err := netproto.ReadFrame(conn)
		if err != nil {
			result.err = err
			return result
		}
		switch netproto.MessageType(f.Type) {
		case netproto.MsgClientMoved:
			var moved netproto.ClientMoved
			result.err = netproto.Decode(f, &moved)
			result.moved = append(result.moved, moved)
			if acknowledged != nil {
				acknowledged <- struct{}{}
			}
		case netproto.MsgClientRemoved:
			var removed netproto.ClientRemoved
			result.err = netproto.Decode(f, &removed)
			result.removed = append(result.removed, removed)
			if acknowledged != nil {
				acknowledged <- struct{}{}
			}
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

func TestMemberAcknowledgementFollowsEffect(t *testing.T) {
	for _, action := range []string{"move", "disconnect", "kick"} {
		for _, outcome := range []string{"done", "failed", "denied", "unrequested"} {
			t.Run(action+"/"+outcome, func(t *testing.T) {
				backend := serverRoleFixture()
				backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.Connect}
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
					d.Channels = &gatedMemberChannels{fakeChannels: d.Channels.(*fakeChannels), before: func(kind string) error {
						if kind != action {
							return nil
						}
						enterOnce.Do(func() { close(entered) })
						<-release
						if outcome == "failed" {
							return errors.New("channel backend failed")
						}
						return nil
					}}
				})
				defer env.stop()
				defer unblock()
				env.state.AddChannel(testChannel(1))
				env.state.AddChannel(testChannel(2))
				senderUID, targetUID := "user-uid", "admin-uid"
				if outcome == "denied" {
					senderUID, targetUID = targetUID, senderUID
				}
				sender, _ := dialAuthed(t, env.addr, senderUID)
				defer func() { _ = sender.Close() }()
				targetConn, targetID := dialAuthed(t, env.addr, targetUID)
				defer func() { _ = targetConn.Close() }()
				if err := env.state.MoveClient(targetID, 1); err != nil {
					t.Fatal(err)
				}
				target, _ := env.srv.clientByID(targetID)
				ack := outcome != "unrequested"
				kind := netproto.MsgKickClient
				removal := netproto.KickClient{ClientID: targetID, FromServer: action == "kick", AckRequested: ack}
				if action == "disconnect" && ack {
					removal.ExpectedChannelID = 1
				}
				var msg any = removal
				if action == "move" {
					kind = netproto.MsgMoveClient
					msg = netproto.MoveClient{ClientID: targetID, ChannelID: 2, AckRequested: ack}
				}
				send(t, sender, kind, msg)
				send(t, sender, netproto.MsgPing, netproto.Ping{})
				if err := sender.SetReadDeadline(time.Now().Add(8 * time.Second)); err != nil {
					t.Fatal(err)
				}
				done, accepted := make(chan memberActionFence, 1), make(chan struct{}, 4)
				go func() { done <- readMemberActionFence(sender, accepted) }()
				if outcome != "denied" {
					select {
					case <-entered:
					case got := <-done:
						t.Fatalf("did not reach effect: %+v", got)
					case <-time.After(time.Second):
						t.Fatal("no effect")
					}
					select {
					case <-accepted:
						t.Fatal("acknowledged before effect completed")
					case got := <-done:
						t.Fatalf("returned early: %+v", got)
					case <-time.After(25 * time.Millisecond):
					}
					unblock()
				}
				got := <-done
				if got.err != nil {
					t.Fatal(got.err)
				}
				committed := outcome == "done" || outcome == "unrequested" || action == "kick" && outcome == "failed"
				if committed && ack {
					if action == "move" {
						if len(got.moved) != 1 || got.moved[0] != (netproto.ClientMoved{ClientID: targetID, ChannelID: 2}) || len(got.removed) != 0 {
							t.Fatalf("move response: %+v", got)
						}
					} else if len(got.removed) != 1 || !got.removed[0].Matches(removal) || got.removed[0].CleanupPending != (outcome == "failed") || len(got.moved) != 0 {
						t.Fatalf("removal response: %+v", got)
					}
				} else if len(got.moved)+len(got.removed) != 0 {
					t.Fatalf("unexpected acknowledgement: %+v", got)
				}
				if committed {
					if len(got.errors) != 0 {
						t.Fatalf("committed effect reported error: %+v", got)
					}
				} else if len(got.errors) != 1 || got.errors[0].OriginType != uint16(kind) {
					t.Fatalf("missing correlated error: %+v", got)
				}
				channel, _, exists := env.state.ClientChannelState(targetID)
				switch {
				case action == "kick" && committed:
					if !target.sessionRevoked() || target.isAuthed() {
						t.Fatal("kick acknowledged without revocation")
					}
				case committed:
					want := int64(0)
					if action == "move" {
						want = 2
					}
					if !exists || channel != want {
						t.Fatalf("membership = %d/%v, want %d", channel, exists, want)
					}
				default:
					if !exists || channel != 1 || !target.isAuthed() {
						t.Fatal("failed action changed target")
					}
				}
			})
		}
	}
}

func TestCommittedReplyBoundsWriterQueue(t *testing.T) {
	client := &Client{}
	client.wmu.Lock()
	defer client.wmu.Unlock()
	done := make(chan error, 1)
	go func() {
		done <- (&TCPServer{}).writeCommittedReply(client, netproto.MsgClientMoved, netproto.ClientMoved{ClientID: "target", ChannelID: 1})
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("writer queue error: %v", err)
		}
	case <-time.After(7 * time.Second):
		t.Fatal("committed reply did not bound writer lock wait")
	}
}
