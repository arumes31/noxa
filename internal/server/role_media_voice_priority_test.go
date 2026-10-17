package server

import (
	"context"
	"testing"
	"time"

	"noxa/internal/authorization"
	"noxa/internal/webrtc"
)

// Every renegotiation (e.g. someone starting a screen share) runs the offer
// guard for each subscriber and writes the offer to that client, bounded only
// by the TCP write ceiling. Voice to and from that client must keep flowing
// for the whole write, even when a member action and a policy reload queue up
// behind it. Once a member action actually holds the barrier, packets for that
// client are still excluded, so revocation stays linearized.
func TestVoiceFlowsDuringSlowRenegotiationOfferWithQueuedWriters(t *testing.T) {
	backend := serverRoleFixture()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.Connect, authorization.Speak}
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority })
	defer env.stop()
	env.state.AddChannel(testChannel(1))
	speakerConn, speakerID := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = speakerConn.Close() }()
	listenerConn, listenerID := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = listenerConn.Close() }()
	for _, id := range []string{speakerID, listenerID} {
		if err := env.state.MoveClient(id, 1); err != nil {
			t.Fatal(err)
		}
	}
	listener, _ := env.srv.clientByID(listenerID)
	voice := env.srv.deps.Voice.(*fakeVoice)
	voice.mu.Lock()
	offerGuard := voice.offerGuard
	voice.mu.Unlock()
	toListener := webrtc.MediaDelivery{SenderID: speakerID, RecipientID: listenerID, ChannelID: 1, RecipientChannelID: 1, Slot: webrtc.SlotMic}
	fromListener := webrtc.MediaDelivery{SenderID: listenerID, RecipientID: speakerID, ChannelID: 1, RecipientChannelID: 1, Slot: webrtc.SlotMic}
	delivered := func(d webrtc.MediaDelivery) bool {
		written := false
		if err := env.srv.guardRoleMedia(d, func() error { written = true; return nil }); err != nil {
			return false
		}
		return written
	}

	inOffer, releaseOffer, offerDone := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		offerDone <- offerGuard(listenerID, func() error {
			close(inOffer)
			<-releaseOffer // A slow client's offer write.
			return nil
		})
	}()
	select {
	case <-inOffer:
	case <-time.After(5 * time.Second):
		t.Fatal("offer guard did not start")
	}
	memberHeld, memberRelease, memberDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(memberDone)
		listener.lockRoleAction() // e.g. a moderation mute queued behind the offer
		close(memberHeld)
		<-memberRelease
		listener.unlockRoleAction()
	}()
	reloadDone := make(chan error, 1)
	go func() { reloadDone <- authority.Reload(context.Background()) }() // e.g. a role edit
	time.Sleep(30 * time.Millisecond)                                    // Let both writers queue.

	for i := range 50 {
		if !delivered(toListener) || !delivered(fromListener) {
			t.Fatalf("voice packet %d dropped during a renegotiation offer write", i)
		}
	}
	close(releaseOffer)
	if err := <-offerDone; err != nil {
		t.Fatal(err)
	}
	if err := <-reloadDone; err != nil {
		t.Fatal(err)
	}
	select {
	case <-memberHeld:
	case <-time.After(5 * time.Second):
		t.Fatal("queued member action never acquired the barrier")
	}
	if delivered(toListener) || delivered(fromListener) {
		t.Fatal("packet bypassed an in-progress member action on its recipient/sender")
	}
	close(memberRelease)
	<-memberDone
	if !delivered(toListener) || !delivered(fromListener) {
		t.Fatal("voice did not resume after the member action")
	}
}
