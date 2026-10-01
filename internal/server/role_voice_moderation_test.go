package server

import (
	"context"
	"testing"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"noxa/internal/webrtc"
)

func TestRoleVoiceModerationEnforcesHierarchyAndAudioDirections(t *testing.T) {
	backend := serverRoleFixture()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.Connect, authorization.Speak, authorization.ShareScreen, authorization.ShareCamera, authorization.Whisper}
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority })
	defer env.stop()
	env.state.AddChannel(testChannel(1))
	member, memberID := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = member.Close() }()
	owner, ownerID := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = owner.Close() }()
	for _, id := range []string{memberID, ownerID} {
		if err := env.state.MoveClient(id, 1); err != nil {
			t.Fatal(err)
		}
	}
	on, off := true, false
	send(t, owner, netproto.MsgMemberVoiceSet, netproto.MemberVoiceSet{ChannelID: 1, Muted: &on})
	if got := readError(t, owner); got.Code != errCodeMalformed {
		t.Fatalf("empty target did not get a recoverable malformed response: %+v", got)
	}
	send(t, member, netproto.MsgMemberVoiceSet, netproto.MemberVoiceSet{ClientID: ownerID, ChannelID: 1, Muted: &on})
	readRoleMediaDenial(t, member)
	send(t, owner, netproto.MsgMemberVoiceSet, netproto.MemberVoiceSet{ClientID: ownerID, ChannelID: 1, Muted: &on})
	readRoleMediaDenial(t, owner)
	set := func(muted, deafened *bool) {
		t.Helper()
		send(t, owner, netproto.MsgMemberVoiceSet, netproto.MemberVoiceSet{ClientID: memberID, ChannelID: 1, Muted: muted, Deafened: deafened})
		readOfType(t, owner, netproto.MsgMemberVoiceState)
	}
	check := func(sender, receiver, slot string, want bool) {
		t.Helper()
		written := false
		if err := env.srv.guardRoleMedia(webrtc.MediaDelivery{SenderID: sender, RecipientID: receiver, ChannelID: 1, RecipientChannelID: 1, Slot: slot}, func() error { written = true; return nil }); err != nil {
			t.Fatal(err)
		}
		if written != want {
			t.Fatalf("%s -> %s %s: wrote=%t", sender, receiver, slot, written)
		}
	}
	checkWhisper := func(sender, receiver string, speaking, want bool) {
		t.Helper()
		payload, err := eventEnvelope(eventWhisper, whisperEvent{FromClientID: sender, ChannelID: 1, Speaking: speaking})
		if err != nil {
			t.Fatal(err)
		}
		target, _ := env.srv.clientByID(receiver)
		if err := authority.WithPolicy(t.Context(), func(e *authorization.RoleEvaluator) error {
			frame, err := env.srv.roleBroadcastFrame(target, payload, e)
			if err != nil || (frame != nil) != want {
				t.Fatalf("queued whisper speaking=%t delivered=%t err=%v", speaking, frame != nil, err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	set(&on, nil)
	checkWhisper(memberID, ownerID, true, false)
	checkWhisper(memberID, ownerID, false, true)
	check(memberID, ownerID, webrtc.SlotMic, false)
	check(memberID, ownerID, webrtc.SlotScreenAudio, false)
	check(memberID, ownerID, webrtc.SlotCam, true)
	check(ownerID, memberID, webrtc.SlotMic, true)
	if env.srv.canTalk(memberID) {
		t.Fatal("server-muted member retained talk permission")
	}
	env.state.SetSpeaking(memberID, true)
	if current, _ := env.state.GetClient(memberID); current.IsSpeaking {
		t.Fatal("late VAD revived a server-muted member")
	}
	set(&off, &on)
	checkWhisper(ownerID, memberID, true, false)
	checkWhisper(ownerID, memberID, false, true)
	check(memberID, ownerID, webrtc.SlotMic, true)
	check(ownerID, memberID, webrtc.SlotMic, false)
	check(ownerID, memberID, webrtc.SlotScreenAudio, false)
	check(ownerID, memberID, webrtc.SlotScreen, true)
	set(nil, &off)
	check(ownerID, memberID, webrtc.SlotMic, true)
}

func TestRoleVoiceModerationChecksEveryFlagBeforeChangingEither(t *testing.T) {
	backend := serverRoleFixture()
	backend.policy.OwnerID = 3
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.Connect}
	backend.policy.Roles = append(backend.policy.Roles, authorization.Role{ID: 20, Name: "Voice moderator", Position: 1, Permissions: []authorization.Capability{authorization.MuteMembers}})
	backend.policy.Members = []authorization.RoleMember{{UserID: 1, RoleIDs: []int64{20}}}
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority })
	defer env.stop()
	env.state.AddChannel(testChannel(1))
	moderator, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = moderator.Close() }()
	member, memberID := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = member.Close() }()
	if err := env.state.MoveClient(memberID, 1); err != nil {
		t.Fatal(err)
	}
	on, off := true, false
	env.state.SetStatus(memberID, "invisible", "")
	send(t, moderator, netproto.MsgMemberVoiceSet, netproto.MemberVoiceSet{ClientID: memberID, ChannelID: 1, Muted: &on})
	readRoleMediaDenial(t, moderator)
	if current, _ := env.state.GetClient(memberID); current.ServerMuted || current.VoiceRevision != 0 {
		t.Fatal("hidden target was disclosed or mutated")
	}
	env.state.SetStatus(memberID, "online", "")
	send(t, moderator, netproto.MsgMemberVoiceSet, netproto.MemberVoiceSet{ClientID: memberID, ChannelID: 1, Muted: &on, Deafened: &on})
	readRoleMediaDenial(t, moderator)
	if current, _ := env.state.GetClient(memberID); current.ServerMuted || current.ServerDeafened {
		t.Fatal("combined denial partially changed voice state")
	}
	send(t, moderator, netproto.MsgMemberVoiceSet, netproto.MemberVoiceSet{ClientID: memberID, ChannelID: 1, Muted: &on})
	readOfType(t, moderator, netproto.MsgMemberVoiceState)
	if _, err := authority.ChangeRolePolicy(t.Context(), 3, authorization.RoleChange{Kind: authorization.RoleUpdate, ExpectedRevision: 1, Role: authorization.Role{ID: 20, Name: "Voice moderator", Position: 1}}); err != nil {
		t.Fatal(err)
	}
	send(t, moderator, netproto.MsgMemberVoiceSet, netproto.MemberVoiceSet{ClientID: memberID, ChannelID: 1, Muted: &off})
	readRoleMediaDenial(t, moderator)
	if current, _ := env.state.GetClient(memberID); !current.ServerMuted {
		t.Fatal("revoked moderator undid another moderation action")
	}
}
