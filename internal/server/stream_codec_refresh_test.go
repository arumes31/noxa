package server

import (
	"context"
	"testing"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

func TestCodecRefreshRetainsPublicationAndShareAuthorization(t *testing.T) {
	for _, slot := range []string{"screen", "cam"} {
		t.Run(slot, func(t *testing.T) {
			backend := serverRoleFixture()
			permission := authorization.ShareScreen
			if slot == "cam" {
				permission = authorization.ShareCamera
			}
			backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.Connect, authorization.ViewChannel, permission}
			authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			voice := &singleCodecVoice{}
			env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority; d.Voice = voice })
			t.Cleanup(env.stop)
			env.state.AddChannel(testChannel(1))
			pub, pubID := dialAuthed(t, env.addr, "admin-uid")
			t.Cleanup(func() { _ = pub.Close() })
			other, otherID := dialAuthed(t, env.addr, "user-uid")
			t.Cleanup(func() { _ = other.Close() })
			for _, id := range []string{pubID, otherID} {
				if err := env.state.MoveClient(id, 1); err != nil {
					t.Fatal(err)
				}
				voice.videoRouter().JoinChannel(1, id)
			}
			request := netproto.VideoStreamControl{Action: "publish", Slot: slot, Active: true, QualityMode: "source", Codecs: []string{"vp8"}}
			send(t, pub, netproto.MsgVideoStreamControl, request)
			var initial netproto.VideoStreamResult
			if err := netproto.Decode(readOfType(t, pub, netproto.MsgVideoStreamResult), &initial); err != nil {
				t.Fatal(err)
			}
			if initial.Generation == 0 || initial.Codec != "vp8" {
				t.Fatalf("initial: %+v", initial)
			}
			request.Generation, request.Codecs = initial.Generation, []string{"vp8", "h264"}
			impersonation := request
			impersonation.PublisherID = pubID
			send(t, other, netproto.MsgVideoStreamControl, impersonation)
			readError(t, other)
			for range 2 {
				send(t, pub, netproto.MsgVideoStreamControl, request)
				var result netproto.VideoStreamResult
				if err := netproto.Decode(readOfType(t, pub, netproto.MsgVideoStreamResult), &result); err != nil {
					t.Fatal(err)
				}
				if result.Generation != initial.Generation || result.Codec != "h264" || result.QualityMode != "source" || result.UploadActive == nil {
					t.Fatalf("refresh replaced source: %+v", result)
				}
			}
			// Even an idempotent refresh must reacquire the owner's current Share*
			// mutation lease; an earlier publication is not continuing permission.
			backend.mu.Lock()
			backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.Connect, authorization.ViewChannel}
			backend.policy.Revision++
			backend.mu.Unlock()
			if err := authority.Reload(t.Context()); err != nil {
				t.Fatal(err)
			}
			send(t, pub, netproto.MsgVideoStreamControl, request)
			readRoleMediaDenial(t, pub)
		})
	}
}
