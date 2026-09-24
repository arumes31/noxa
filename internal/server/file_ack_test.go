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

type fileMutationFence struct {
	saved  []netproto.FileMutationSaved
	errors []netproto.Error
	err    error
}

func readFileMutationFence(conn net.Conn, accepted chan<- struct{}) fileMutationFence {
	var result fileMutationFence
	for {
		f, err := netproto.ReadFrame(conn)
		if err != nil {
			result.err = err
			return result
		}
		switch netproto.MessageType(f.Type) {
		case netproto.MsgFileMutationSaved:
			var saved netproto.FileMutationSaved
			result.err = netproto.Decode(f, &saved)
			result.saved = append(result.saved, saved)
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

type gatedFileMutations struct {
	*fakeFileTransfer
	before func() error
}

func (f *gatedFileMutations) DeleteFile(ctx context.Context, channelID int64, folder, name string) error {
	if err := f.before(); err != nil {
		return err
	}
	return f.fakeFileTransfer.DeleteFile(ctx, channelID, folder, name)
}

func (f *gatedFileMutations) MoveFile(ctx context.Context, channelID int64, folder, name string, target int64, newFolder, newName string) error {
	if err := f.before(); err != nil {
		return err
	}
	return f.fakeFileTransfer.MoveFile(ctx, channelID, folder, name, target, newFolder, newName)
}

func fileMutationForTest(action, clientID string, ack bool) (netproto.MessageType, any, netproto.FileMutationSaved) {
	result := netproto.FileMutationSaved{ClientID: clientID, ChannelID: 1, Folder: "docs", Name: "report.txt"}
	if action == "delete" {
		result.Operation = netproto.MsgFileDelete
		return result.Operation, netproto.FileDelete{AckRequested: ack, ChannelID: 1, Folder: result.Folder, Name: result.Name}, result
	}
	result.Operation = netproto.MsgFileRename
	result.NewFolder, result.NewName = "archive", "renamed.txt"
	if action == "move" {
		result.NewChannelID = 2
	}
	return result.Operation, netproto.FileRename{AckRequested: ack, ChannelID: result.ChannelID, Folder: result.Folder, Name: result.Name,
		NewChannelID: result.NewChannelID, NewFolder: result.NewFolder, NewName: result.NewName}, result
}

func TestFileMutationAcknowledgementFollowsEffect(t *testing.T) {
	for _, action := range []string{"delete", "rename", "move"} {
		for _, outcome := range []string{"done", "failed", "denied", "unrequested"} {
			t.Run(action+"/"+outcome, func(t *testing.T) {
				backend := serverRoleFixture()
				backend.policy.Channels = append(backend.policy.Channels, authorization.ChannelPolicy{ChannelID: 2})
				authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
				if err != nil {
					t.Fatal(err)
				}
				entered, release := make(chan struct{}), make(chan struct{})
				var once sync.Once
				unblock := func() { once.Do(func() { close(release) }) }
				env := startTestEnvDeps(t, nil, nil, func(d *Deps) {
					d.Authority = authority
					d.FileTransfer = &gatedFileMutations{fakeFileTransfer: d.FileTransfer.(*fakeFileTransfer), before: func() error {
						close(entered)
						<-release
						if outcome == "failed" {
							return errors.New("file backend failed")
						}
						return nil
					}}
				})
				defer env.stop()
				defer unblock()
				env.state.AddChannel(testChannel(1))
				env.state.AddChannel(testChannel(2))
				uid := "user-uid"
				if outcome == "denied" {
					uid = "admin-uid"
				}
				conn, id := dialAuthed(t, env.addr, uid)
				defer func() { _ = conn.Close() }()
				kind, msg, want := fileMutationForTest(action, id, outcome != "unrequested")
				send(t, conn, kind, msg)
				send(t, conn, netproto.MsgPing, netproto.Ping{})
				if err := conn.SetReadDeadline(time.Now().Add(8 * time.Second)); err != nil {
					t.Fatal(err)
				}
				done, accepted := make(chan fileMutationFence, 1), make(chan struct{}, 4)
				go func() { done <- readFileMutationFence(conn, accepted) }()
				if outcome != "denied" {
					select {
					case <-entered:
					case got := <-done:
						t.Fatalf("returned before backend: %+v", got)
					case <-time.After(time.Second):
						t.Fatal("no backend effect")
					}
					select {
					case <-accepted:
						t.Fatal("acknowledged before effect")
					case <-time.After(30 * time.Millisecond):
					}
				}
				unblock()
				got := <-done
				if got.err != nil {
					t.Fatal(got.err)
				}
				if outcome == "done" {
					if len(got.saved) != 1 || got.saved[0] != want || len(got.errors) != 0 {
						t.Fatalf("result: %+v", got)
					}
				} else if len(got.saved) != 0 || (len(got.errors) == 0) != (outcome == "unrequested") {
					t.Fatalf("result: %+v", got)
				}
				env.ft.mu.Lock()
				effects := len(env.ft.deleted) + len(env.ft.moved)
				env.ft.mu.Unlock()
				if (effects == 1) != (outcome == "done" || outcome == "unrequested") {
					t.Fatalf("effects: %d", effects)
				}
			})
		}
	}
}
