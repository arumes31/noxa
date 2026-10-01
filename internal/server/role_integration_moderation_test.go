//go:build integration

package server

import (
	"context"
	"errors"
	"testing"

	"go.uber.org/zap"
	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/config"
	"noxa/internal/netproto"
	"noxa/internal/state"
	"noxa/internal/webrtc"
)

func TestIntegrationMemberVoiceAdmissionAndMedia(t *testing.T) {
	db := integrationManagementStore(t)
	a := auth.New(db, zap.NewNop())
	create := func(name string) auth.IntegrationPrincipal {
		t.Helper()
		uid, err := a.RegisterUser(t.Context(), name, "integration-password")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET integration_enabled=TRUE WHERE unique_id=$1", uid); err != nil {
			t.Fatal(err)
		}
		p, err := a.AuthenticateIntegration(t.Context(), uid, "integration-password", "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	owner, moderator, member := create("voice-owner"), create("voice-moderator"), create("voice-member")
	backend := serverRoleFixture()
	backend.policy.OwnerID = owner.UserID()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.Connect, authorization.Speak, authorization.ShareCamera, authorization.ShareScreen}
	backend.policy.Roles = append(backend.policy.Roles, authorization.Role{ID: 20, Name: "Voice moderator", Position: 1, Permissions: []authorization.Capability{authorization.MuteMembers}})
	backend.policy.Members = []authorization.RoleMember{{UserID: moderator.UserID(), RoleIDs: []int64{20}}}
	backend.policy.Channels = append(backend.policy.Channels, authorization.ChannelPolicy{ChannelID: 2})
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	sm := state.New(zap.NewNop())
	sm.AddChannel(testChannel(1))
	sm.AddChannel(testChannel(2))
	srv := New(&config.Config{FileRoot: t.TempDir()}, zap.NewNop(), &Deps{Authority: authority, Auth: a, Groups: db, State: sm})
	addSession := func(id string, p auth.IntegrationPrincipal) {
		t.Helper()
		c := &Client{ID: id, Conn: newBlockingTCPConn()}
		c.setIdentity(p.UniqueID(), id, p.UserID(), false)
		srv.clients[id] = c
		sm.AddClient(&state.Client{ClientID: id, UniqueID: p.UniqueID(), UserID: p.UserID(), Nickname: id})
		if err := sm.MoveClient(id, 1); err != nil {
			t.Fatal(err)
		}
	}
	addSession("target", member)
	addSession("owner", owner)
	addSession("moderator", moderator)
	on, off := true, false
	request := netproto.MemberVoiceSet{ClientID: "target", ChannelID: 1, Muted: &on}
	deny := func(p auth.IntegrationPrincipal, req netproto.MemberVoiceSet, want error) {
		t.Helper()
		before, _ := sm.GetClient("target")
		if _, err := srv.SetIntegrationMemberVoice(t.Context(), p, req); !errors.Is(err, want) {
			t.Fatalf("denial: got %v want %v", err, want)
		}
		after, _ := sm.GetClient("target")
		if before.ServerMuted != after.ServerMuted || before.ServerDeafened != after.ServerDeafened || before.VoiceRevision != after.VoiceRevision {
			t.Fatal("denied request changed session voice state")
		}
	}
	deny(moderator, netproto.MemberVoiceSet{ClientID: "target", ChannelID: 1, Muted: &on, Deafened: &on}, authorization.ErrRoleForbidden)
	deny(moderator, netproto.MemberVoiceSet{ClientID: "target", ChannelID: 2, Muted: &on}, authorization.ErrRoleForbidden)
	deny(moderator, netproto.MemberVoiceSet{ClientID: "absent", ChannelID: 1, Muted: &on}, authorization.ErrRoleForbidden)
	deny(moderator, netproto.MemberVoiceSet{ClientID: "owner", ChannelID: 1, Muted: &on}, authorization.ErrRoleForbidden)
	deny(moderator, netproto.MemberVoiceSet{ClientID: "moderator", ChannelID: 1, Muted: &on}, authorization.ErrRoleForbidden)
	sm.SetStatus("target", "invisible", "")
	deny(moderator, request, authorization.ErrRoleForbidden)
	sm.SetStatus("target", "online", "")
	result, err := srv.SetIntegrationMemberVoice(t.Context(), moderator, request)
	if err != nil || result.Revision != 1 || !result.Muted || result.Deafened {
		t.Fatalf("mute: %+v %v", result, err)
	}
	media := func(sender, recipient, slot string, want bool) {
		t.Helper()
		written := false
		err := srv.guardRoleMedia(webrtc.MediaDelivery{SenderID: sender, RecipientID: recipient, ChannelID: 1, RecipientChannelID: 1, Slot: slot}, func() error { written = true; return nil })
		if err != nil || written != want {
			t.Fatalf("%s -> %s %s: delivered=%t error=%v", sender, recipient, slot, written, err)
		}
	}
	media("target", "owner", webrtc.SlotMic, false)
	media("target", "owner", webrtc.SlotScreenAudio, false)
	media("target", "owner", webrtc.SlotCam, true)
	media("owner", "target", webrtc.SlotMic, true)
	request.Muted = &off
	if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET integration_enabled=FALSE WHERE id=$1", moderator.UserID()); err != nil {
		t.Fatal(err)
	}
	deny(moderator, request, auth.ErrIntegrationDenied)
	if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET integration_enabled=TRUE WHERE id=$1", moderator.UserID()); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.ChangeRolePolicy(t.Context(), owner.UserID(), authorization.RoleChange{Kind: authorization.RoleUpdate, ExpectedRevision: 1, Role: authorization.Role{ID: 20, Name: "Voice moderator", Position: 1}}); err != nil {
		t.Fatal(err)
	}
	deny(moderator, request, authorization.ErrRoleForbidden)
	result, err = srv.SetIntegrationMemberVoice(t.Context(), owner, netproto.MemberVoiceSet{ClientID: "target", ChannelID: 1, Muted: &off, Deafened: &on})
	if err != nil || result.Revision != 2 || result.Muted || !result.Deafened {
		t.Fatalf("unmute/deafen: %+v %v", result, err)
	}
	media("target", "owner", webrtc.SlotMic, true)
	media("owner", "target", webrtc.SlotMic, false)
	media("owner", "target", webrtc.SlotScreenAudio, false)
	media("owner", "target", webrtc.SlotCam, true)
	var audits int
	if err := db.DB().QueryRowContext(t.Context(), "SELECT COUNT(*) FROM audit_log WHERE action='member_voice_changed' AND actor_unique_id IN ($1,$2) AND target=$3 AND scope_channel_ids=ARRAY[1]::bigint[]", owner.UniqueID(), moderator.UniqueID(), member.UniqueID()).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 2 {
		t.Fatalf("canonical successful moderation audits: %d", audits)
	}
	// Movement shares the membership lifecycle and keeps session voice flags.
	destination := testChannel(2)
	destination.MaxClients = 1
	sm.AddChannel(destination)
	if err := sm.MoveClient("owner", 2); err != nil {
		t.Fatal(err)
	}
	move := netproto.MoveClient{ClientID: "target", ChannelID: 2}
	if err := srv.MoveIntegrationMember(t.Context(), owner, move); !errors.Is(err, authorization.ErrRoleConflict) {
		t.Fatalf("owner bypassed capacity: %v", err)
	}
	if err := sm.MoveClient("owner", 1); err != nil {
		t.Fatal(err)
	}
	if err := srv.MoveIntegrationMember(t.Context(), moderator, move); !errors.Is(err, authorization.ErrRoleForbidden) {
		t.Fatalf("ungranted movement: %v", err)
	}
	if _, err := authority.ChangeRolePolicy(t.Context(), owner.UserID(), authorization.RoleChange{Kind: authorization.RoleUpdate, ExpectedRevision: 2, Role: authorization.Role{ID: 20, Name: "Mover", Position: 1, Permissions: []authorization.Capability{authorization.MoveMembers}}}); err != nil {
		t.Fatal(err)
	}
	sm.SetStatus("target", "invisible", "")
	if err := srv.MoveIntegrationMember(t.Context(), moderator, move); !errors.Is(err, authorization.ErrRoleForbidden) {
		t.Fatalf("hidden move: %v", err)
	}
	sm.SetStatus("target", "online", "")
	if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET integration_enabled=FALSE WHERE id=$1", moderator.UserID()); err != nil {
		t.Fatal(err)
	}
	if err := srv.MoveIntegrationMember(t.Context(), moderator, move); !errors.Is(err, auth.ErrIntegrationDenied) {
		t.Fatalf("disabled integration moved member: %v", err)
	}
	if current, _ := sm.GetClient("target"); current.ChannelID != 1 {
		t.Fatal("denied movement changed membership")
	}
	if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET integration_enabled=TRUE WHERE id=$1", moderator.UserID()); err != nil {
		t.Fatal(err)
	}
	if err := srv.MoveIntegrationMember(t.Context(), moderator, move); err != nil {
		t.Fatal(err)
	}
	if current, _ := sm.GetClient("target"); current.ChannelID != 2 || !current.ServerDeafened || current.ServerMuted || current.VoiceRevision != 2 {
		t.Fatalf("move lost session voice state: %+v", current)
	}
	media("target", "owner", webrtc.SlotCam, false)
	if err := db.DB().QueryRowContext(t.Context(), "SELECT COUNT(*) FROM audit_log WHERE action='member_moved' AND actor_unique_id=$1 AND target=$2 AND scope_channel_ids=ARRAY[1,2]::bigint[]", moderator.UniqueID(), member.UniqueID()).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Fatalf("canonical move audit: %d", audits)
	}
	disconnect := netproto.MemberDisconnect{ClientID: "target", ChannelID: 2, Reason: "Return to the lobby"}
	if _, err := srv.DisconnectIntegrationMember(t.Context(), moderator, disconnect); !errors.Is(err, authorization.ErrRoleForbidden) {
		t.Fatalf("movement grant allowed disconnection: %v", err)
	}
	if _, err := authority.ChangeRolePolicy(t.Context(), owner.UserID(), authorization.RoleChange{Kind: authorization.RoleUpdate, ExpectedRevision: 3, Role: authorization.Role{ID: 20, Name: "Disconnect moderator", Position: 1, Permissions: []authorization.Capability{authorization.DisconnectMembers}}}); err != nil {
		t.Fatal(err)
	}
	stale := disconnect
	stale.ChannelID = 1
	if _, err := srv.DisconnectIntegrationMember(t.Context(), moderator, stale); !errors.Is(err, authorization.ErrRoleForbidden) {
		t.Fatalf("stale disconnect moved another scope: %v", err)
	}
	if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET integration_enabled=FALSE WHERE id=$1", moderator.UserID()); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.DisconnectIntegrationMember(t.Context(), moderator, disconnect); !errors.Is(err, auth.ErrIntegrationDenied) {
		t.Fatalf("disabled integration disconnected member: %v", err)
	}
	if current, _ := sm.GetClient("target"); current.ChannelID != 2 {
		t.Fatal("denied disconnection changed membership")
	}
	if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET integration_enabled=TRUE WHERE id=$1", moderator.UserID()); err != nil {
		t.Fatal(err)
	}
	sm.Subscribe("target", []int64{2})
	left, err := srv.DisconnectIntegrationMember(t.Context(), moderator, disconnect)
	if err != nil || left.ClientID != "target" || left.ChannelID != 2 {
		t.Fatalf("disconnect acknowledgement: %+v %v", left, err)
	}
	if current, _ := sm.GetClient("target"); current.ChannelID != 0 || !current.ServerDeafened || current.VoiceRevision != 2 {
		t.Fatalf("disconnect changed session voice state: %+v", current)
	}
	if !srv.clients["target"].isAuthed() || !sm.IsSubscribed("target", 2) {
		t.Fatal("channel disconnect revoked the session or explicit chat subscription")
	}
	if _, err := srv.DisconnectIntegrationMember(t.Context(), moderator, disconnect); !errors.Is(err, authorization.ErrRoleForbidden) {
		t.Fatalf("repeated disconnect falsely acknowledged an effect: %v", err)
	}
	if err := db.DB().QueryRowContext(t.Context(), "SELECT COUNT(*) FROM audit_log WHERE action='kick' AND actor_unique_id=$1 AND target=$2 AND scope_channel_ids=ARRAY[2]::bigint[]", moderator.UniqueID(), member.UniqueID()).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Fatalf("canonical disconnect audit: %d", audits)
	}
}
