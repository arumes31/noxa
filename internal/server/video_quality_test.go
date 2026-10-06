package server

import (
	"context"
	"testing"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"noxa/internal/webrtc"
)

func TestScopedVideoQualityAcknowledgesOnlyCurrentAuthorizedWatch(t *testing.T) {
	backend := serverRoleFixture()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.Connect, authorization.ViewChannel, authorization.ShareScreen}
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	voice := &fakeVoice{}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority; d.Voice = voice })
	defer env.stop()
	env.state.AddChannel(testChannel(1))
	pub, pubID := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = pub.Close() }()
	sub, subID := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = sub.Close() }()
	for _, id := range []string{pubID, subID} {
		if err := env.state.MoveClient(id, 1); err != nil {
			t.Fatal(err)
		}
		voice.videoRouter().JoinChannel(1, id)
	}
	generation, err := voice.PublishVideo(pubID, webrtc.SlotScreen, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	session := voice.VideoWatchSession(subID)
	if _, err := voice.WatchVideo(subID, pubID, webrtc.SlotScreen, generation, 1, session, true); err != nil {
		t.Fatal(err)
	}
	request := netproto.VideoQuality{PublisherID: pubID, Slot: webrtc.SlotScreen, Generation: generation, Session: session, Quality: "high", AckRequested: true}
	send(t, sub, netproto.MsgVideoQuality, request)
	var saved netproto.MediaControlSaved
	if err := netproto.Decode(readOfType(t, sub, netproto.MsgMediaControlSaved), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Operation != netproto.MsgVideoQuality || saved.ClientID != subID || saved.PublisherID != pubID || saved.Slot != request.Slot || saved.Generation != generation || saved.Session != session || saved.Quality != "high" {
		t.Fatalf("wrong ACK: %+v", saved)
	}
	for _, scenario := range []string{"unknown publisher", "wrong slot", "stale publication", "stale session"} {
		bad := request
		switch scenario {
		case "unknown publisher":
			bad.PublisherID = "missing"
		case "wrong slot":
			bad.Slot = webrtc.SlotCam
		case "stale publication":
			bad.Generation++
		case "stale session":
			bad.Session++
		}
		send(t, sub, netproto.MsgVideoQuality, bad)
		if reply := readError(t, sub); reply.Code != errCodeMalformed {
			t.Fatalf("%s: %+v", scenario, reply)
		}
	}
	// Changing the role while watching must deny controls before the router.
	if _, err := authority.ChangeRolePolicy(t.Context(), 2, authorization.RoleChange{Kind: authorization.RoleUpdate, ExpectedRevision: 1, Role: authorization.Role{ID: 10, Name: "@everyone", Permissions: []authorization.Capability{authorization.ViewChannel}}}); err != nil {
		t.Fatal(err)
	}
	send(t, sub, netproto.MsgVideoQuality, request)
	readRoleMediaDenial(t, sub)
	voice.mu.Lock()
	defer voice.mu.Unlock()
	if len(voice.qualities) != 0 {
		t.Fatal("scoped control changed the global quality")
	}
}
