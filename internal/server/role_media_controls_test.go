package server

import (
	"context"
	"net"
	"testing"
	"time"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

// Unlike readOfType, a Ping barrier makes missing permission errors fail at
// the end of this request rather than waiting through unrelated events.
func readRoleMediaDenial(t *testing.T, conn net.Conn) {
	t.Helper()
	send(t, conn, netproto.MsgPing, netproto.Ping{})
	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	for {
		frame, err := netproto.ReadFrame(conn)
		if err != nil {
			t.Fatal(err)
		}
		switch netproto.MessageType(frame.Type) {
		case netproto.MsgError:
			var msg netproto.Error
			if err := netproto.Decode(frame, &msg); err != nil {
				t.Fatal(err)
			}
			if msg.Code != errCodePermissionDenied {
				t.Fatalf("expected permission denial: %+v", msg)
			}
			readOfType(t, conn, netproto.MsgPong)
			return
		case netproto.MsgPong, netproto.MsgWebRTCAnswer:
			t.Fatalf("request completed without permission denial: %s", netproto.MessageType(frame.Type))
		}
	}
}

func TestRoleMediaControlsRejectLegacyAdminBypass(t *testing.T) {
	for _, request := range []struct {
		name string
		kind netproto.MessageType
		body any
	}{
		{"whisper", netproto.MsgWhisperSet, netproto.WhisperSet{Active: true, ChannelIDs: []int64{1}}},
		{"priority", netproto.MsgPrioritySpeaker, netproto.PrioritySpeaker{Active: true}},
		{"screen", netproto.MsgScreenShare, netproto.ScreenShare{Active: true, MaxHeight: 1080}},
	} {
		t.Run(request.name, func(t *testing.T) {
			backend := serverRoleFixture()
			backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.Connect, authorization.Speak}
			authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority })
			defer env.stop()
			env.state.AddChannel(testChannel(1))
			conn, id := dialAuthed(t, env.addr, "admin-uid")
			defer func() { _ = conn.Close() }()
			if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
				t.Fatal(err)
			}
			if err := env.state.MoveClient(id, 1); err != nil {
				t.Fatal(err)
			}
			send(t, conn, request.kind, request.body)
			readRoleMediaDenial(t, conn)
		})
	}
}

func TestRoleMediaSetupRequiresCurrentVoiceAccess(t *testing.T) {
	backend := serverRoleFixture()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel}
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority })
	defer env.stop()
	env.state.AddChannel(testChannel(1))
	conn, id := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = conn.Close() }()
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := env.state.MoveClient(id, 1); err != nil {
		t.Fatal(err)
	}
	requests := []struct {
		kind netproto.MessageType
		body any
	}{
		{netproto.MsgWebRTCOffer, netproto.WebRTCOffer{SDP: "offer"}},
		{netproto.MsgWebRTCAnswer, netproto.WebRTCAnswer{SDP: "answer"}},
		{netproto.MsgICECandidate, netproto.ICECandidate{Candidate: "candidate"}},
		{netproto.MsgVideoQuality, netproto.VideoQuality{Quality: "high"}},
	}
	for _, request := range requests {
		send(t, conn, request.kind, request.body)
		readRoleMediaDenial(t, conn)
	}
	voice := env.srv.deps.Voice.(*fakeVoice)
	voice.mu.Lock()
	called := len(voice.offers) + len(voice.answers) + len(voice.candidates) + len(voice.qualities)
	voice.mu.Unlock()
	if called != 0 {
		t.Fatal("denied signaling reached the voice backend")
	}
	if _, err := authority.ChangeRolePolicy(t.Context(), 2, authorization.RoleChange{Kind: authorization.RoleUpdate, ExpectedRevision: 1, Role: authorization.Role{ID: 10, Name: "@everyone", Permissions: []authorization.Capability{authorization.ViewChannel, authorization.Connect}}}); err != nil {
		t.Fatal(err)
	}
	for _, request := range requests {
		send(t, conn, request.kind, request.body)
	}
	readOfType(t, conn, netproto.MsgWebRTCAnswer)
	send(t, conn, netproto.MsgPing, netproto.Ping{})
	readOfType(t, conn, netproto.MsgPong)
	voice.mu.Lock()
	called = len(voice.offers) + len(voice.answers) + len(voice.candidates) + len(voice.qualities)
	onCandidate := voice.onCandidate
	offerSender := voice.offerSender
	offerGuard := voice.offerGuard
	voice.mu.Unlock()
	if called != len(requests) {
		t.Fatalf("allowed signaling calls: %d", called)
	}
	if _, err := authority.ChangeRolePolicy(t.Context(), 2, authorization.RoleChange{Kind: authorization.RoleUpdate, ExpectedRevision: 2, Role: authorization.Role{ID: 10, Name: "@everyone", Permissions: []authorization.Capability{authorization.ViewChannel}}}); err != nil {
		t.Fatal(err)
	}
	onCandidate("late candidate", "0", 0)
	if err := offerGuard(id, func() error { return offerSender(id, "late offer") }); err == nil {
		t.Fatal("revoked session received a server offer")
	}
	send(t, conn, netproto.MsgPing, netproto.Ping{})
	for {
		frame := readFrame(t, conn)
		kind := netproto.MessageType(frame.Type)
		if kind == netproto.MsgICECandidate || kind == netproto.MsgWebRTCOffer {
			t.Fatal("revoked session received late signaling")
		}
		if kind == netproto.MsgPong {
			break
		}
	}
}

func TestRoleMediaControlsCheckDestinationsAndAllowRevokedCleanup(t *testing.T) {
	backend := serverRoleFixture()
	base := []authorization.Capability{authorization.ViewChannel, authorization.Connect, authorization.Speak}
	backend.policy.Roles[0].Permissions = append(append([]authorization.Capability(nil), base...), authorization.Whisper, authorization.PrioritySpeaker, authorization.ShareScreen)
	backend.policy.Channels = append(backend.policy.Channels, authorization.ChannelPolicy{ChannelID: 2, Overrides: []authorization.RoleOverride{{RoleID: 10, Capability: authorization.ViewChannel, Effect: authorization.Deny}}})
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority })
	defer env.stop()
	for _, id := range []int64{1, 2} {
		env.state.AddChannel(testChannel(id))
	}
	conn, id := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = conn.Close() }()
	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := env.state.MoveClient(id, 1); err != nil {
		t.Fatal(err)
	}
	client, _ := env.srv.clientByID(id)
	if err := env.srv.roleWhisperSet(t.Context(), client, netproto.WhisperSet{Active: true, ChannelIDs: []int64{2}}); err != nil {
		t.Fatal(err)
	}
	if got := readError(t, conn); got.Code != errCodePermissionDenied {
		t.Fatalf("hidden whisper target accepted: %+v", got)
	}
	if err := env.srv.roleWhisperSet(t.Context(), client, netproto.WhisperSet{Active: true, ChannelIDs: []int64{1}}); err != nil {
		t.Fatal(err)
	}
	if err := env.srv.rolePrioritySpeaker(t.Context(), client, netproto.PrioritySpeaker{Active: true}); err != nil {
		t.Fatal(err)
	}
	if err := env.srv.roleScreenShare(t.Context(), client, netproto.ScreenShare{Active: true, MaxHeight: 1080}); err != nil {
		t.Fatal(err)
	}
	member, _ := env.state.GetClient(id)
	if !member.PrioritySpeaker || !member.Sharing {
		t.Fatal("role priority/screen-share state did not apply")
	}
	if _, err := authority.ChangeRolePolicy(t.Context(), 2, authorization.RoleChange{Kind: authorization.RoleUpdate, ExpectedRevision: 1, Role: authorization.Role{ID: 10, Name: "@everyone", Permissions: base}}); err != nil {
		t.Fatal(err)
	}
	if err := env.srv.roleWhisperSet(t.Context(), client, netproto.WhisperSet{}); err != nil {
		t.Fatal(err)
	}
	if err := env.srv.rolePrioritySpeaker(t.Context(), client, netproto.PrioritySpeaker{}); err != nil {
		t.Fatal(err)
	}
	if err := env.srv.roleScreenShare(t.Context(), client, netproto.ScreenShare{}); err != nil {
		t.Fatal(err)
	}
	member, _ = env.state.GetClient(id)
	if member.PrioritySpeaker || member.Sharing {
		t.Fatal("revoked priority/screen-share state could not be cleared")
	}
	voice := env.srv.deps.Voice.(*fakeVoice)
	voice.mu.Lock()
	defer voice.mu.Unlock()
	if len(voice.whispers) != 2 || !voice.whispers[0].active || voice.whispers[1].active {
		t.Fatalf("whisper authorization/cleanup: %+v", voice.whispers)
	}
}
