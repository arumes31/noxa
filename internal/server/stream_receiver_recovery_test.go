package server

import (
	"context"
	"sync"
	"testing"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

type receiverRecoveryVoice struct {
	fakeVoice
	mu         sync.Mutex
	request    netproto.VideoStreamControl
	subscriber string
}

func (v *receiverRecoveryVoice) ResetStreamReceiver(subscriber, publisher, slot string, generation, session, revision uint64) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.subscriber = subscriber
	v.request = netproto.VideoStreamControl{PublisherID: publisher, Slot: slot, Generation: generation, Session: session, Revision: revision}
	return nil
}

func TestReceiverRecoveryControlAcknowledgesExactAuthenticatedScope(t *testing.T) {
	backend := serverRoleFixture()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.Connect, authorization.ViewChannel}
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	voice := &receiverRecoveryVoice{}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority; d.Voice = voice })
	t.Cleanup(env.stop)
	env.state.AddChannel(testChannel(1))
	conn, id := dialAuthed(t, env.addr, "admin-uid")
	t.Cleanup(func() { _ = conn.Close() })
	if err := env.state.MoveClient(id, 1); err != nil {
		t.Fatal(err)
	}
	voice.videoRouter().JoinChannel(1, id)
	request := netproto.VideoStreamControl{Action: "receiver_reset", PublisherID: "pub", Slot: "screen", Generation: 5, Session: 7, Revision: 9, Active: true}
	send(t, conn, netproto.MsgVideoStreamControl, request)
	var result netproto.VideoStreamResult
	if err := netproto.Decode(readOfType(t, conn, netproto.MsgVideoStreamResult), &result); err != nil {
		t.Fatal(err)
	}
	voice.mu.Lock()
	gotSubscriber, gotRequest := voice.subscriber, voice.request
	voice.mu.Unlock()
	if gotSubscriber != id || gotRequest.PublisherID != request.PublisherID || gotRequest.Generation != 5 || gotRequest.Session != 7 || gotRequest.Revision != 9 ||
		result.Action != request.Action || result.PublisherID != request.PublisherID || result.Generation != 5 || result.Session != 7 || result.Revision != 9 || !result.Active {
		t.Fatal("receiver recovery lost authenticated scope or acknowledgement identity")
	}
	for _, invalid := range []netproto.VideoStreamControl{
		{Action: "receiver_reset", PublisherID: "pub", Slot: "screen", Generation: 5, Session: 7, Active: true},
		{Action: "receiver_reset", PublisherID: "pub", Slot: "mic", Generation: 5, Session: 7, Revision: 9, Active: true},
		{Action: "receiver_reset", PublisherID: "pub", Slot: "screen", Generation: 5, Session: 7, Revision: 9},
	} {
		voice.mu.Lock()
		voice.subscriber = ""
		voice.mu.Unlock()
		send(t, conn, netproto.MsgVideoStreamControl, invalid)
		readError(t, conn)
		voice.mu.Lock()
		called := voice.subscriber != ""
		voice.mu.Unlock()
		if called {
			t.Fatal("malformed receiver recovery reached voice backend")
		}
	}
}
