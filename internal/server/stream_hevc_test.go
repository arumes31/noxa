package server

import (
	"context"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"testing"
)

type hevcTestVoice struct{ singleCodecVoice }

func (v *hevcTestVoice) PublishVideoWithFormats(publisher, slot string, generation uint64, active bool, mode string, codecs []string, level uint8, revision ...uint64) (uint64, error) {
	return v.videoRouter().PublishVideoWithFormats(publisher, slot, generation, active, mode, codecs, level, revision...)
}
func (v *hevcTestVoice) WatchVideoWithFormats(subscriber, publisher, slot string, generation, revision, session uint64, active bool, codecs []string, level uint8) (bool, error) {
	return v.videoRouter().WatchVideoWithFormats(subscriber, publisher, slot, generation, revision, session, active, codecs, level)
}
func TestHEVCControlPreservesOnePublicationAndLegacyFallback(t *testing.T) {
	backend := serverRoleFixture()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.Connect, authorization.ViewChannel, authorization.ShareScreen}
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	voice := &hevcTestVoice{}
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
	send(t, pub, netproto.MsgVideoStreamControl, netproto.VideoStreamControl{Action: "publish", Slot: "screen", Active: true, QualityMode: "source", Codecs: []string{"h265", "h264", "vp8"}, HEVCLevelID: 123, CodecRevision: 1})
	publication := decode(readOfType(t, pub, netproto.MsgVideoStreamResult))
	if publication.Generation == 0 || publication.Codec != "h265" || publication.HEVCLevelID != 123 || publication.CodecRevision != 1 {
		t.Fatalf("HEVC not selected: %+v", publication)
	}
	send(t, sub, netproto.MsgVideoStreamControl, netproto.VideoStreamControl{Action: "list"})
	catalog := decode(readOfType(t, sub, netproto.MsgVideoStreamResult))
	if len(catalog.Streams) != 1 || catalog.Streams[0].Codec != "" || catalog.Streams[0].HEVCLevelID != 0 || catalog.Streams[0].CodecRevision != 0 {
		t.Fatalf("capabilities disclosed: %+v", catalog)
	}
	for i, test := range []struct {
		codecs []string
		level  uint8
		want   string
	}{
		{[]string{"h265", "h264", "vp8"}, 123, "h265"},
		{[]string{"h265", "h264", "vp8"}, 120, "h264"},
		{nil, 0, "vp8"},
		{[]string{"h265", "h264", "vp8"}, 153, "h265"},
	} {
		send(t, sub, netproto.MsgVideoStreamControl, netproto.VideoStreamControl{Action: "watch", PublisherID: pubID, Slot: "screen", Generation: publication.Generation, Session: catalog.Session, Revision: uint64(i + 1), Active: true, Codecs: test.codecs, HEVCLevelID: test.level})
		readOfType(t, sub, netproto.MsgVideoStreamResult)
		readEventOfType(t, pub, eventStreamUploadChanged)
		send(t, pub, netproto.MsgVideoStreamControl, netproto.VideoStreamControl{Action: "list"})
		own := decode(readOfType(t, pub, netproto.MsgVideoStreamResult))
		level := uint8(0)
		if test.want == "h265" {
			level = 123
		}
		if len(own.Streams) != 1 || own.Streams[0].Codec != test.want || own.Streams[0].HEVCLevelID != level || own.Streams[0].CodecRevision != 1 || own.Streams[0].Generation != publication.Generation {
			t.Fatalf("codec/level or lifetime incorrect: %+v", own)
		}
	}
	for _, invalid := range []netproto.VideoStreamControl{
		{Action: "list", HEVCLevelID: 123},
		{Action: "watch", PublisherID: pubID, Slot: "screen", Generation: publication.Generation, Session: catalog.Session, Revision: 5, Active: true, Codecs: []string{"h265", "vp8"}},
		{Action: "watch", PublisherID: pubID, Slot: "screen", Generation: publication.Generation, Session: catalog.Session, Revision: 5, Active: true, Codecs: []string{"vp8"}, HEVCLevelID: 123},
	} {
		send(t, sub, netproto.MsgVideoStreamControl, invalid)
		readError(t, sub)
	}
}
func TestHEVCMetadataFailsClosedWithOlderBackend(t *testing.T) {
	voice := &singleCodecVoice{}
	request := netproto.VideoStreamControl{Active: true, Slot: "screen", Codecs: []string{"h265", "vp8"}, HEVCLevelID: 123}
	if _, err := publishVideoWithMode(voice, "pub", request); err == nil {
		t.Fatal("old backend ignored HEVC level")
	}
	if _, err := watchVideoWithCodecs(voice, "sub", request); err == nil {
		t.Fatal("old backend ignored receiver level")
	}
}
