package server

import (
	"context"
	"encoding/json"
	"testing"

	"go.uber.org/zap"
	"noxa/internal/authorization"
	"noxa/internal/config"
	"noxa/internal/netproto"
)

type sourceQualityVoice struct{ fakeVoice }

func (v *sourceQualityVoice) PublishVideoWithMode(publisher, slot string, generation uint64, active bool, mode string) (uint64, error) {
	return v.videoRouter().PublishVideoWithMode(publisher, slot, generation, active, mode)
}

func TestSourceQualityPublicationFeedbackIsOwnOnlyAndExplicit(t *testing.T) {
	backend := serverRoleFixture()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.Connect, authorization.ViewChannel, authorization.ShareScreen}
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	voice := &sourceQualityVoice{}
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
	send(t, pub, netproto.MsgVideoStreamControl, netproto.VideoStreamControl{Action: "publish", Slot: "screen", Active: true, QualityMode: "source"})
	publication := decode(readOfType(t, pub, netproto.MsgVideoStreamResult))
	if publication.Generation == 0 || publication.QualityMode != "source" || publication.UploadActive == nil || *publication.UploadActive {
		t.Fatalf("publish did not return explicit initial audience: %+v", publication)
	}
	send(t, sub, netproto.MsgVideoStreamControl, netproto.VideoStreamControl{Action: "list"})
	catalog := decode(readOfType(t, sub, netproto.MsgVideoStreamResult))
	if len(catalog.Streams) != 1 || catalog.Streams[0].QualityMode != "source" || catalog.Streams[0].UploadActive != nil {
		t.Fatalf("viewer catalog leaked publisher feedback: %+v", catalog)
	}
	for revision := uint64(1); revision <= 2; revision++ {
		active := revision == 1
		send(t, sub, netproto.MsgVideoStreamControl, netproto.VideoStreamControl{Action: "watch", PublisherID: pubID, Slot: "screen", Generation: publication.Generation, Session: catalog.Session, Revision: revision, Active: active})
		readOfType(t, sub, netproto.MsgVideoStreamResult)
		event := readEventOfType(t, pub, eventStreamUploadChanged)
		var data map[string]json.RawMessage
		if err := json.Unmarshal(event, &data); err != nil || len(data) != 3 || data["upload_active"] != nil {
			t.Fatalf("refresh hint contains replayable state: %s, %v", event, err)
		}
		send(t, pub, netproto.MsgVideoStreamControl, netproto.VideoStreamControl{Action: "list"})
		own := decode(readOfType(t, pub, netproto.MsgVideoStreamResult))
		if len(own.Streams) != 1 || own.Streams[0].UploadActive == nil || *own.Streams[0].UploadActive != active {
			t.Fatalf("wrong current upload feedback: %+v", own)
		}
	}
	for _, invalid := range []netproto.VideoStreamControl{
		{Action: "publish", Slot: "cam", Active: true, QualityMode: "arbitrary"},
		{Action: "list", QualityMode: "source"},
		{Action: "watch", PublisherID: pubID, Slot: "screen", Generation: publication.Generation, Session: catalog.Session, Revision: 3, Active: true, QualityMode: "source"},
	} {
		send(t, sub, netproto.MsgVideoStreamControl, invalid)
		readError(t, sub)
	}
}

func TestSourceUploadHintRechecksPublisherAndGenerationAtWrite(t *testing.T) {
	backend := serverRoleFixture()
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	voice := &sourceQualityVoice{}
	voice.videoRouter().JoinChannel(1, "pub")
	generation, err := voice.PublishVideoWithMode("pub", "screen", 0, true, "source")
	if err != nil {
		t.Fatal(err)
	}
	srv := New(&config.Config{}, zap.NewNop(), &Deps{Authority: authority, Voice: voice})
	writes := 0
	conn := &watchNotificationConn{blockingTCPConn: newBlockingTCPConn(), onWrite: func() {
		writes++
		if srv.roleMetadataMu.TryLock() {
			srv.roleMetadataMu.Unlock()
			t.Error("publication lease released before upload hint delivery")
		}
	}}
	payload, err := eventEnvelope(eventStreamUploadChanged, streamWatchStartedEvent{"pub", "screen", generation})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.writeRoleBroadcast(&Client{ID: "pub", Conn: conn}, payload); err != nil || writes == 0 {
		t.Fatalf("current hint not delivered: writes=%d, %v", writes, err)
	}
	writes = 0
	if err := srv.writeRoleBroadcast(&Client{ID: "other", Conn: conn}, payload); err != nil {
		t.Fatal(err)
	}
	if _, err := voice.PublishVideoWithMode("pub", "screen", generation, false, ""); err != nil {
		t.Fatal(err)
	}
	if err := srv.writeRoleBroadcast(&Client{ID: "pub", Conn: conn}, payload); err != nil || writes != 0 {
		t.Fatalf("stale/wrong recipient hint delivered: writes=%d, %v", writes, err)
	}
}
