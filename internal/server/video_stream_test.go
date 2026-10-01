package server

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"go.uber.org/zap"

	"noxa/internal/authorization"
	"noxa/internal/config"
	"noxa/internal/netproto"
	"noxa/internal/webrtc"
)

type publicationResetVoice struct {
	fakeVoice
	reset bool
}

func (v *publicationResetVoice) HandleOffer(clientID, sdp string, candidate func(string, string, uint16)) (string, error) {
	if v.reset {
		v.videoRouter().DetachPeerKeepChannel(clientID)
	}
	return v.fakeVoice.HandleOffer(clientID, sdp, candidate)
}

func TestVideoSharingMetadataFollowsPeerPublicationLifetime(t *testing.T) {
	for _, scenario := range []string{"renegotiate", "rebuild", "failed rebuild"} {
		t.Run(scenario, func(t *testing.T) {
			backend := serverRoleFixture()
			backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.Connect, authorization.ViewChannel, authorization.ShareScreen}
			authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			voice := &publicationResetVoice{reset: scenario != "renegotiate"}
			voice.answerSDP = "answer"
			if scenario == "failed rebuild" {
				voice.offerErr = errors.New("rebuild failed")
			}
			env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority; d.Voice = voice })
			defer env.stop()
			env.state.AddChannel(testChannel(1))
			conn, id := dialAuthed(t, env.addr, "admin-uid")
			defer func() { _ = conn.Close() }()
			if err := env.state.MoveClient(id, 1); err != nil {
				t.Fatal(err)
			}
			voice.videoRouter().JoinChannel(1, id)
			if _, err := voice.PublishVideo(id, webrtc.SlotScreen, 0, true); err != nil {
				t.Fatal(err)
			}
			env.state.SetSharing(id, true)
			send(t, conn, netproto.MsgWebRTCOffer, netproto.WebRTCOffer{SDP: "offer"})
			if voice.offerErr != nil {
				readError(t, conn)
			} else {
				readOfType(t, conn, netproto.MsgWebRTCAnswer)
			}
			member, _ := env.state.GetClient(id)
			if member.Sharing != !voice.reset {
				t.Fatalf("stale sharing flag after %s: %v", scenario, member.Sharing)
			}
		})
	}
}

type watchNotificationConn struct {
	*blockingTCPConn
	onWrite func()
}

func (c *watchNotificationConn) Write(p []byte) (int, error) { c.onWrite(); return len(p), nil }

func TestVideoWatchNotificationPinsCurrentPublicationThroughWrite(t *testing.T) {
	backend := serverRoleFixture()
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	voice := &fakeVoice{}
	voice.videoRouter().JoinChannel(1, "pub")
	generation, err := voice.PublishVideo("pub", webrtc.SlotScreen, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	srv := New(&config.Config{}, zap.NewNop(), &Deps{Authority: authority, Voice: voice})
	writes := 0
	conn := &watchNotificationConn{blockingTCPConn: newBlockingTCPConn(), onWrite: func() {
		writes++
		if srv.roleMetadataMu.TryLock() {
			srv.roleMetadataMu.Unlock()
			t.Error("publication lifecycle barrier released before delivery")
		}
	}}
	client := &Client{ID: "pub", Conn: conn}
	payload, err := eventEnvelope(eventStreamWatchStarted, streamWatchStartedEvent{"pub", webrtc.SlotScreen, generation})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.writeRoleBroadcast(client, payload); err != nil {
		t.Fatal(err)
	}
	if writes == 0 {
		t.Fatal("current publication notification dropped")
	}
	writes = 0
	if err := srv.writeRoleBroadcast(&Client{ID: "other", Conn: conn}, payload); err != nil {
		t.Fatal(err)
	}
	if _, err := voice.PublishVideo("pub", webrtc.SlotScreen, generation, false); err != nil {
		t.Fatal(err)
	}
	if err := srv.writeRoleBroadcast(client, payload); err != nil {
		t.Fatal(err)
	}
	if writes != 0 {
		t.Fatal("stale or wrongly addressed notification delivered")
	}
}

func TestVideoStreamControlRequiresScopedConsent(t *testing.T) {
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
	pub, pubID := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = pub.Close() }()
	sub, subID := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = sub.Close() }()
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
	send(t, pub, netproto.MsgVideoStreamControl, netproto.VideoStreamControl{Action: "publish", Slot: webrtc.SlotCam, Active: true})
	readRoleMediaDenial(t, pub)
	send(t, pub, netproto.MsgVideoStreamControl, netproto.VideoStreamControl{Action: "publish", Slot: webrtc.SlotScreen, Active: true})
	publication := decode(readOfType(t, pub, netproto.MsgVideoStreamResult))
	if publication.PublisherID != pubID || publication.Generation == 0 || !publication.Active {
		t.Fatalf("bad publish ACK: %+v", publication)
	}
	send(t, sub, netproto.MsgVideoStreamControl, netproto.VideoStreamControl{Action: "list"})
	catalog := decode(readOfType(t, sub, netproto.MsgVideoStreamResult))
	if len(catalog.Streams) != 1 || catalog.Session == 0 {
		t.Fatalf("bad catalog: %+v", catalog)
	}
	watch := netproto.VideoStreamControl{Action: "watch", PublisherID: pubID, Slot: webrtc.SlotScreen, Generation: publication.Generation, Revision: 1, Session: catalog.Session, Active: true}
	send(t, sub, netproto.MsgVideoStreamControl, watch)
	saved := decode(readOfType(t, sub, netproto.MsgVideoStreamResult))
	if !saved.Active || saved.Session != watch.Session || saved.Revision != watch.Revision {
		t.Fatalf("bad watch ACK: %+v", saved)
	}
	var event struct {
		Type string                  `json:"type"`
		Data streamWatchStartedEvent `json:"data"`
	}
	if err := json.Unmarshal(readOfType(t, pub, netproto.MsgEvent).Payload, &event); err != nil {
		t.Fatal(err)
	}
	if event.Type != eventStreamWatchStarted || event.Data.PublisherID != pubID || event.Data.Generation != publication.Generation || event.Data.Slot != webrtc.SlotScreen {
		t.Fatalf("bad publisher notification: %+v", event)
	}
	watch.Active, watch.Revision = false, 2
	send(t, sub, netproto.MsgVideoStreamControl, watch)
	if saved := decode(readOfType(t, sub, netproto.MsgVideoStreamResult)); saved.Active {
		t.Fatal("stop not acknowledged")
	}
	watch.Active, watch.Revision = true, 1
	send(t, sub, netproto.MsgVideoStreamControl, watch)
	if reply := readError(t, sub); reply.Code != errCodeMalformed {
		t.Fatalf("stale watch accepted: %+v", reply)
	}
	voice.videoRouter().JoinChannel(2, subID)
	send(t, sub, netproto.MsgVideoStreamControl, netproto.VideoStreamControl{Action: "list"})
	if got := decode(readOfType(t, sub, netproto.MsgVideoStreamResult)); len(got.Streams) != 0 {
		t.Fatal("catalog escaped source channel")
	}
}
