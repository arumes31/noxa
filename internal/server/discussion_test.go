package server

import (
	"context"
	"sync/atomic"
	"testing"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

type discussionTestStore struct {
	*fakeChat
	writes atomic.Int32
	reads  atomic.Int32
	author string
}

func (f *discussionTestStore) DiscussionFollowers(context.Context, int64, []string) (map[string]bool, error) {
	return nil, nil
}

func (f *discussionTestStore) Discussion(_ context.Context, r netproto.DiscussionRequest, _, _ string) (netproto.DiscussionResult, error) {
	if r.Action == "get" || r.Action == "history" || r.Action == "list" || r.Action == "state" {
		f.reads.Add(1)
	} else {
		f.writes.Add(1)
	}
	return netproto.DiscussionResult{Action: r.Action, ChannelID: r.ChannelID, ThreadID: r.ThreadID, Threads: []netproto.DiscussionThread{{ID: r.ThreadID, ChannelID: r.ChannelID, Author: f.author}}}, nil
}

func TestDiscussionAuthorizationBeforeMutation(t *testing.T) {
	for _, tc := range []struct {
		name, action         string
		caps                 []authorization.Capability
		hidden, own, allowed bool
	}{
		{"read", "list", []authorization.Capability{authorization.ViewChannel, authorization.ReadHistory}, false, false, true},
		{"history", "history", []authorization.Capability{authorization.ViewChannel, authorization.ReadHistory}, false, false, true},
		{"edit own", "edit", []authorization.Capability{authorization.ViewChannel, authorization.ReadHistory, authorization.SendMessages}, false, true, true},
		{"edit others denied", "edit", []authorization.Capability{authorization.ViewChannel, authorization.ReadHistory, authorization.SendMessages}, false, false, false},
		{"resolve own", "resolve", []authorization.Capability{authorization.ViewChannel, authorization.ReadHistory, authorization.SendMessages}, false, true, true},
		{"resolve others denied", "resolve", []authorization.Capability{authorization.ViewChannel, authorization.ReadHistory, authorization.SendMessages}, false, false, false},
		{"pin author denied", "pin", []authorization.Capability{authorization.ViewChannel, authorization.ReadHistory, authorization.SendMessages}, false, true, false},
		{"pin moderator", "pin", []authorization.Capability{authorization.ViewChannel, authorization.ReadHistory, authorization.ManageMessages}, false, false, true},
		{"no history", "list", []authorization.Capability{authorization.ViewChannel}, false, false, false},
		{"hidden", "list", []authorization.Capability{authorization.ViewChannel, authorization.ReadHistory}, true, false, false},
		{"join", "join", []authorization.Capability{authorization.ViewChannel, authorization.ReadHistory}, false, false, true},
		{"configuration denied", "configure", []authorization.Capability{authorization.ViewChannel, authorization.ReadHistory}, false, false, false},
		{"configuration allowed", "configure", []authorization.Capability{authorization.ViewChannel, authorization.ReadHistory, authorization.ManageChannels}, false, false, true},
		{"archive others denied", "archive", []authorization.Capability{authorization.ViewChannel, authorization.ReadHistory, authorization.SendMessages}, false, false, false},
		{"archive own", "archive", []authorization.Capability{authorization.ViewChannel, authorization.ReadHistory, authorization.SendMessages}, false, true, true},
		{"archive moderator", "archive", []authorization.Capability{authorization.ViewChannel, authorization.ReadHistory, authorization.ManageMessages}, false, false, true},
		{"reopen own", "reopen", []authorization.Capability{authorization.ViewChannel, authorization.ReadHistory, authorization.SendMessages}, false, true, true},
		{"send denied", "send", []authorization.Capability{authorization.ViewChannel, authorization.ReadHistory}, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := serverRoleFixture()
			policy.policy.Roles[0].Permissions = tc.caps
			if tc.hidden {
				policy.policy.Channels[0].Overrides = []authorization.RoleOverride{{RoleID: 10, Capability: authorization.ViewChannel, Effect: authorization.Deny}}
			}
			authority, err := authorization.NewAuthority(t.Context(), policy, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			backend := &discussionTestStore{fakeChat: newFakeChat(), author: "other"}
			if tc.own {
				backend.author = "admin-uid"
			}
			env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority; d.Chat = backend })
			defer env.stop()
			conn, _ := dialAuthed(t, env.addr, "admin-uid")
			defer func() { _ = conn.Close() }()
			pub, _ := testX25519(t)
			publishKey(t, conn, pub)
			r := netproto.DiscussionRequest{Action: tc.action, ChannelID: 1, ThreadID: 42, RequestID: "request-1"}
			r.Title = "Edited title"
			if tc.action == "list" || tc.action == "configure" {
				r.ThreadID = 0
			}
			send(t, conn, netproto.MsgDiscussionRequest, r)
			if tc.allowed {
				var out netproto.DiscussionResult
				if err := netproto.Decode(readOfType(t, conn, netproto.MsgDiscussionResult), &out); err != nil || out.Action != r.Action {
					t.Fatalf("response=%+v error=%v", out, err)
				}
			} else {
				readError(t, conn)
				if backend.writes.Load() != 0 {
					t.Fatal("unauthorized mutation reached storage")
				}
			}
			if (tc.hidden || tc.name == "no history") && backend.reads.Load() != 0 {
				t.Fatal("unauthorized read reached storage")
			}
		})
	}
}
