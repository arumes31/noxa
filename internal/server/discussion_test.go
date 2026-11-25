package server

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"noxa/internal/state"
)

type discussionTestStore struct {
	*fakeChat
	writes atomic.Int32
	reads  atomic.Int32
	author string
	fail   atomic.Bool // mutations fail like an archived thread
}

func (f *discussionTestStore) DiscussionFollowers(context.Context, int64, []string) (map[string]bool, error) {
	return nil, nil
}

func (f *discussionTestStore) Discussion(_ context.Context, r netproto.DiscussionRequest, _, _ string) (netproto.DiscussionResult, error) {
	if r.Action == "get" || r.Action == "history" || r.Action == "list" || r.Action == "state" {
		f.reads.Add(1)
	} else {
		if f.fail.Load() {
			return netproto.DiscussionResult{}, netproto.ErrDiscussionArchived
		}
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
		{"delete moderator", "delete", []authorization.Capability{authorization.ViewChannel, authorization.ReadHistory, authorization.ManageMessages}, false, false, true},
		{"delete administrator", "delete", []authorization.Capability{authorization.Administrator}, false, false, true},
		{"delete owner", "delete", nil, false, false, true},
		{"delete message moderator", "delete_message", []authorization.Capability{authorization.ViewChannel, authorization.ReadHistory, authorization.ManageMessages}, false, false, true},
		{"delete message administrator", "delete_message", []authorization.Capability{authorization.Administrator}, false, false, true},
		{"delete message owner", "delete_message", nil, false, false, true},
		{"delete message denied", "delete_message", []authorization.Capability{authorization.ViewChannel, authorization.ReadHistory, authorization.SendMessages}, false, false, false},
		{"delete other denied", "delete", []authorization.Capability{authorization.ViewChannel, authorization.ReadHistory, authorization.SendMessages}, false, false, false},
		{"delete author denied", "delete", []authorization.Capability{authorization.ViewChannel, authorization.ReadHistory, authorization.SendMessages}, false, true, false},
		{"delete hidden denied", "delete", []authorization.Capability{authorization.ViewChannel, authorization.ReadHistory, authorization.ManageMessages}, true, false, false},
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
			if tc.name == "delete administrator" || tc.name == "delete message administrator" {
				policy.policy.Roles[0].Permissions = nil
				policy.policy.Roles = append(policy.policy.Roles, authorization.Role{ID: 20, Name: "Administrator", Position: 1, Permissions: tc.caps})
				policy.policy.Members = []authorization.RoleMember{{UserID: 1, RoleIDs: []int64{20}}}
			}
			if tc.name == "delete owner" || tc.name == "delete message owner" {
				policy.policy.OwnerID = 1
			}
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
			if tc.action == "delete_message" {
				r.MessageID = 17
			}
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

// A post the store refuses (an archived thread) does not start the channel's
// slow-mode window, and a post refused for slow mode is not stored.
func TestDiscussionRejectedPostKeepsSlowModeSlot(t *testing.T) {
	policy := serverRoleFixture()
	policy.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.ReadHistory, authorization.SendMessages}
	authority, err := authorization.NewAuthority(t.Context(), policy, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	backend := &discussionTestStore{fakeChat: newFakeChat(), author: "other"}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority; d.Chat = backend })
	defer env.stop()
	env.state.AddChannel(testChannel(1))
	slowMode := 60
	if !env.state.UpdateChannel(1, state.ChannelUpdate{SlowModeSeconds: &slowMode}) {
		t.Fatal("channel 1 missing")
	}
	conn, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = conn.Close() }()
	pub, _ := testX25519(t)
	publishKey(t, conn, pub)
	keyID, key, err := env.srv.chatKeys.EnsureScope(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	post := func(text string) {
		send(t, conn, netproto.MsgDiscussionRequest, netproto.DiscussionRequest{Action: "send", ChannelID: 1, ThreadID: 42, BodyEnc: sealScopeTest(t, key, text), KeyID: keyID, RequestID: text})
	}
	// reply reads the answer to a post: "" for a stored post, else the error.
	reply := func() string {
		for {
			f := readFrame(t, conn)
			switch netproto.MessageType(f.Type) {
			case netproto.MsgDiscussionResult:
				return ""
			case netproto.MsgError:
				var e netproto.Error
				if err := netproto.Decode(f, &e); err != nil {
					t.Fatal(err)
				}
				return e.Message
			}
		}
	}
	backend.fail.Store(true)
	post("into an archived thread")
	if got := reply(); got == "" {
		t.Fatal("post into an archived thread succeeded")
	}
	backend.fail.Store(false)
	post("first stored post")
	if got := reply(); got != "" {
		t.Fatalf("post after a refused one: %s", got)
	}
	writes := backend.writes.Load()
	post("inside the window")
	if got := reply(); !strings.Contains(got, "slow mode") {
		t.Fatalf("error = %q, want slow mode", got)
	}
	// The next request is answered after the refused post finished.
	send(t, conn, netproto.MsgDiscussionRequest, netproto.DiscussionRequest{Action: "list", ChannelID: 1})
	readOfType(t, conn, netproto.MsgDiscussionResult)
	if backend.writes.Load() != writes {
		t.Fatal("a post refused for slow mode reached storage")
	}
}
