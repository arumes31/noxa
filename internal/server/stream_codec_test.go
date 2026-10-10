package server

import (
	"context"
	"testing"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

type singleCodecVoice struct{ sourceQualityVoice }

func (v *singleCodecVoice) PublishVideoWithCodecs(publisher, slot string, generation uint64, active bool, mode string, codecs []string) (uint64, error) {
	return v.videoRouter().PublishVideoWithCodecs(publisher, slot, generation, active, mode, codecs)
}

func (v *singleCodecVoice) WatchVideoWithCodecs(subscriber, publisher, slot string, generation, revision, session uint64, active bool, codecs []string) (bool, error) {
	return v.videoRouter().WatchVideoWithCodecs(subscriber, publisher, slot, generation, revision, session, active, codecs)
}

func TestSingleCodecPublicationControl(t *testing.T) {
	backend := serverRoleFixture()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.Connect, authorization.ViewChannel, authorization.ShareScreen}
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	voice := &singleCodecVoice{}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority; d.Voice = voice })
	t.Cleanup(env.stop)
	env.state.AddChannel(testChannel(1))
	pub, pubID := dialAuthed(t, env.addr, "user-uid")
	t.Cleanup(func() { _ = pub.Close() })
	sub, subID := dialAuthed(t, env.addr, "admin-uid")
	t.Cleanup(func() { _ = sub.Close() })
	for _, id := range []string{pubID, subID} {
		if err := env.state.MoveClient(id, 1); err != nil {
			t.Fatal(err)
		}
		voice.videoRouter().JoinChannel(1, id)
	}
	decode := func(frame *netproto.Frame) netproto.VideoStreamResult {
		t.Helper()
		var result netproto.VideoStreamResult
		if err := netproto.Decode(frame, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	send(t, pub, netproto.MsgVideoStreamControl, netproto.VideoStreamControl{Action: "publish", Slot: "screen", Active: true, QualityMode: "source", Codecs: []string{"h264", "vp8"}})
	publication := decode(readOfType(t, pub, netproto.MsgVideoStreamResult))
	if publication.Generation == 0 || publication.Codec != "h264" {
		t.Fatalf("hardware publication not selected: %+v", publication)
	}
	send(t, sub, netproto.MsgVideoStreamControl, netproto.VideoStreamControl{Action: "list"})
	catalog := decode(readOfType(t, sub, netproto.MsgVideoStreamResult))
	if len(catalog.Streams) != 1 || catalog.Streams[0].Codec != "" {
		t.Fatalf("publisher-only codec feedback disclosed: %+v", catalog)
	}
	for i, codecs := range [][]string{{"h264", "vp8"}, nil, {"h264", "vp8"}} {
		send(t, sub, netproto.MsgVideoStreamControl, netproto.VideoStreamControl{Action: "watch", PublisherID: pubID, Slot: "screen", Generation: publication.Generation, Session: catalog.Session, Revision: uint64(i + 1), Active: true, Codecs: codecs})
		readOfType(t, sub, netproto.MsgVideoStreamResult)
		readEventOfType(t, pub, eventStreamUploadChanged)
		send(t, pub, netproto.MsgVideoStreamControl, netproto.VideoStreamControl{Action: "list"})
		own := decode(readOfType(t, pub, netproto.MsgVideoStreamResult))
		want := "h264"
		if codecs == nil {
			want = "vp8"
		}
		if len(own.Streams) != 1 || own.Streams[0].Codec != want || own.Streams[0].Generation != publication.Generation {
			t.Fatalf("common codec changed publication or ignored viewer: %+v", own)
		}
	}
	for _, invalid := range []netproto.VideoStreamControl{
		{Action: "list", Codecs: []string{"vp8"}},
		{Action: "watch", PublisherID: pubID, Slot: "screen", Generation: publication.Generation, Session: catalog.Session, Revision: 4, Active: true, Codecs: []string{"h265", "vp8"}},
	} {
		send(t, sub, netproto.MsgVideoStreamControl, invalid)
		readError(t, sub)
	}
}
