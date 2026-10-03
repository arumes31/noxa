package server

import (
	"context"
	"testing"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

type echoTestVoice struct {
	*fakeVoice
	channelID int64
}

func (v *echoTestVoice) EchoChannel() int64 { return v.channelID }

func TestServerInfoOnlyExposesActiveVisibleEchoChannel(t *testing.T) {
	for _, channelID := range []int64{0, 1, 999} {
		backend := serverRoleFixture()
		backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel}
		backend.policy.Channels[0].Overrides = []authorization.RoleOverride{{RoleID: 10, Capability: authorization.ViewChannel, Effect: authorization.Deny}}
		authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
		if err != nil {
			t.Fatal(err)
		}
		env := startTestEnvDeps(t, nil, nil, func(d *Deps) {
			d.Authority = authority
			d.Voice = &echoTestVoice{fakeVoice: &fakeVoice{}, channelID: channelID}
		})
		env.state.AddChannel(testChannel(1))
		for _, uid := range []string{"user-uid", "admin-uid"} {
			conn, _ := dialAuthed(t, env.addr, uid)
			send(t, conn, netproto.MsgServerInfoQuery, netproto.ServerInfoQuery{})
			var response struct {
				EchoChannelID int64 `json:"echo_channel_id"`
			}
			if err := netproto.Decode(readOfType(t, conn, netproto.MsgServerInfoResponse), &response); err != nil {
				t.Fatal(err)
			}
			_ = conn.Close()
			want := int64(0)
			if channelID == 1 && uid == "user-uid" {
				want = 1
			}
			if response.EchoChannelID != want {
				t.Errorf("configured=%d identity=%s echo=%d, want %d", channelID, uid, response.EchoChannelID, want)
			}
		}
		env.stop()
	}
}
