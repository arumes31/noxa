package server

import (
	"context"
	"testing"

	"go.uber.org/zap"
	"noxa/internal/authorization"
	"noxa/internal/config"
	"noxa/internal/netproto"
)

func TestMediaLimitsManagementUsesCurrentRoleGrant(t *testing.T) {
	backend := serverRoleFixture()
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	voice := &committingMediaVoice{fakeVoice: &fakeVoice{}}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) {
		d.Authority = authority
		d.Voice = voice
	})
	defer env.stop()
	member, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = member.Close() }()
	want := netproto.MediaLimits{VideoMaxBitrate: 800000, VideoMaxWidth: 640, VideoMaxHeight: 360}
	send(t, member, netproto.MsgMediaLimitsSet, want)
	if got := readError(t, member); got.Code != errCodePermissionDenied || got.OriginType != uint16(netproto.MsgMediaLimitsSet) {
		t.Fatalf("ungranted save = %+v", got)
	}
	if _, err := authority.ChangeRolePolicy(t.Context(), 2, authorization.RoleChange{Kind: authorization.RoleUpdate, ExpectedRevision: 1,
		Role: authorization.Role{ID: 10, Name: "@everyone", Permissions: []authorization.Capability{authorization.ManageServer}}}); err != nil {
		t.Fatal(err)
	}
	send(t, member, netproto.MsgMediaLimitsSet, want)
	var saved netproto.MediaLimitsSaved
	if err := netproto.Decode(readOfType(t, member, netproto.MsgMediaLimitsSaved), &saved); err != nil || saved.MediaLimits != want || saved.Revision != 1 {
		t.Fatalf("delegated save = %+v, %v", saved, err)
	}
	if voice.limits != want || env.srv.mediaLimitsSnapshot() != (netproto.MediaLimitsChanged{Revision: 1, MediaLimits: want}) {
		t.Fatalf("runtime/publication diverged: voice=%+v published=%+v", voice.limits, env.srv.mediaLimitsSnapshot())
	}
	if _, err := authority.ChangeRolePolicy(t.Context(), 2, authorization.RoleChange{Kind: authorization.RoleUpdate, ExpectedRevision: 2,
		Role: authorization.Role{ID: 10, Name: "@everyone"}}); err != nil {
		t.Fatal(err)
	}
	next := netproto.MediaLimits{VideoMaxBitrate: 1600000, VideoMaxWidth: 1280, VideoMaxHeight: 720}
	send(t, member, netproto.MsgMediaLimitsSet, next)
	if got := readError(t, member); got.Code != errCodePermissionDenied {
		t.Fatalf("revoked save = %+v", got)
	}
	if voice.limits != want {
		t.Fatal("revoked save changed runtime")
	}
}

func TestServerConfigAdvertisesMediaLimitsManagementOnlyWhenAvailable(t *testing.T) {
	srv := New(&config.Config{}, zap.NewNop(), &Deps{})
	if srv.serverConfig().MediaLimitsManagement {
		t.Fatal("advertised unavailable media management")
	}
	srv.deps.Chat = newFakeChat()
	srv.deps.Voice = &committingMediaVoice{fakeVoice: &fakeVoice{}}
	if srv.serverConfig().MediaLimitsManagement {
		t.Fatal("advertised media management without roles-v1 authority")
	}
	backend := serverRoleFixture()
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	srv.deps.Authority = authority
	if !srv.serverConfig().MediaLimitsManagement {
		t.Fatal("did not advertise available media management")
	}
}

func TestMediaLimitsManagementValidatesCompletePayload(t *testing.T) {
	voice := &committingMediaVoice{fakeVoice: &fakeVoice{}}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Voice = voice })
	defer env.stop()
	admin, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = admin.Close() }()
	for _, payload := range []string{
		`{"video_max_bitrate":800000,"video_max_width":640}`,
		`{"video_max_bitrate":800000,"video_max_width":640,"video_max_height":0}`,
		`{"video_max_bitrate":-1,"video_max_width":0,"video_max_height":0}`,
	} {
		if err := netproto.WriteFrame(admin, &netproto.Frame{Type: uint16(netproto.MsgMediaLimitsSet), Payload: []byte(payload)}); err != nil {
			t.Fatal(err)
		}
		if got := readError(t, admin); got.Code != errCodeMalformed || got.OriginType != uint16(netproto.MsgMediaLimitsSet) {
			t.Fatalf("invalid payload %s = %+v", payload, got)
		}
	}
}
