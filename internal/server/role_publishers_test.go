package server

import (
	"context"
	"testing"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"noxa/internal/webrtc"
)

func TestRoleChannelWhisperHidesSourceMetadataFromIneligibleRecipient(t *testing.T) {
	backend := serverRoleFixture()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.Connect, authorization.Speak}
	backend.policy.Channels[0].Overrides = []authorization.RoleOverride{{UserID: 1, Capability: authorization.ViewChannel, Effect: authorization.Deny}}
	backend.policy.Channels = append(backend.policy.Channels, authorization.ChannelPolicy{ChannelID: 2})
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority })
	defer env.stop()
	for _, ch := range []int64{1, 2} {
		env.state.AddChannel(testChannel(ch))
	}
	senderConn, senderID := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = senderConn.Close() }()
	receiverConn, receiverID := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = receiverConn.Close() }()
	if err := env.state.MoveClient(senderID, 1); err != nil {
		t.Fatal(err)
	}
	if err := env.state.MoveClient(receiverID, 2); err != nil {
		t.Fatal(err)
	}
	sender, _ := env.srv.clientByID(senderID)
	if err := env.srv.roleWhisperSet(t.Context(), sender, netproto.WhisperSet{Active: true, ChannelIDs: []int64{2}}); err != nil {
		t.Fatal(err)
	}
	voice := env.srv.deps.Voice.(*fakeVoice)
	voice.mu.Lock()
	guard := voice.publisherGuard
	offerGuard := voice.offerGuard
	voice.mu.Unlock()
	pair := webrtc.PublisherAccess{PublisherID: senderID, SubscriberID: receiverID, ChannelID: 1, SubscriberChannelID: 2}
	if guard(pair) {
		t.Fatal("channel whisper exposed its hidden source")
	}
	if _, err := authority.ChangeRolePolicy(t.Context(), 2, authorization.RoleChange{Kind: authorization.ChannelAccessSet, ExpectedRevision: 1, Channel: authorization.ChannelPolicy{ChannelID: 1}}); err != nil {
		t.Fatal(err)
	}
	if guard(pair) {
		t.Fatal("router predicate retained mutable policy")
	}
	if err := offerGuard(receiverID, func() error {
		voice.mu.Lock()
		current := voice.publisherGuard
		voice.mu.Unlock()
		if !current(pair) {
			t.Error("SDP preparation failed to refresh the role grant")
		}
		if env.srv.roleMetadataMu.TryLock() {
			env.srv.roleMetadataMu.Unlock()
			t.Error("SDP did not pin membership metadata")
		}
		// A slow subscriber must not acquire unrelated senders' packet locks.
		if !sender.roleActionMu.TryLock() {
			t.Error("SDP blocked unrelated audio delivery")
		} else {
			sender.roleActionMu.Unlock()
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := env.srv.guardRoleMedia(webrtc.MediaDelivery{SenderID: senderID, RecipientID: receiverID, ChannelID: 1, RecipientChannelID: 2, Slot: webrtc.SlotMic, Whisper: true}, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := env.srv.setSessionRulesPending(t.Context(), sender, true); err != nil {
		t.Fatal(err)
	}
	voice.mu.Lock()
	blocked := voice.publisherGuard
	voice.mu.Unlock()
	if blocked(pair) {
		t.Fatal("rules-blocked publisher retained SDP eligibility")
	}
	if err := env.srv.setSessionRulesPending(t.Context(), sender, false); err != nil {
		t.Fatal(err)
	}
	send(t, receiverConn, netproto.MsgSetStatus, netproto.SetStatus{Status: "invisible"})
	readRoleMediaDenial(t, receiverConn)
	send(t, senderConn, netproto.MsgSetStatus, netproto.SetStatus{Status: "invisible"})
	send(t, senderConn, netproto.MsgPing, netproto.Ping{})
	readOfType(t, senderConn, netproto.MsgPong)
	source, _ := env.state.GetClient(senderID)
	if source.Status != "invisible" {
		t.Fatal("protected owner could not use invisible status")
	}
	voice.mu.Lock()
	invisible := voice.publisherGuard
	voice.mu.Unlock()
	if invisible(pair) {
		t.Fatal("invisible source leaked metadata to an ordinary member")
	}
	if err := env.srv.guardRoleMedia(webrtc.MediaDelivery{SenderID: senderID, RecipientID: receiverID, ChannelID: 1, RecipientChannelID: 2, Slot: webrtc.SlotMic, Whisper: true}, func() error { t.Fatal("queued packet exposed invisible publisher"); return nil }); err != nil {
		t.Fatal(err)
	}
}
