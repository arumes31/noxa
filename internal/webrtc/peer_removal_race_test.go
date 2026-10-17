package webrtc

import (
	"fmt"
	"sync"
	"testing"

	pion "github.com/pion/webrtc/v4"
)

// newRemovalRaceClient returns a connected-by-SDP client and server peer whose
// initial client offer has been answered, so later exchanges are renegotiations.
func newRemovalRaceClient(t *testing.T) (*Engine, *PeerConnectionWrapper, *pion.PeerConnection) {
	t.Helper()
	e, err := New(testLogger(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	media := &pion.MediaEngine{}
	if err := media.RegisterDefaultCodecs(); err != nil {
		t.Fatal(err)
	}
	client, err := pion.NewAPI(pion.WithMediaEngine(media)).NewPeerConnection(pion.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if _, err := client.AddTransceiverFromKind(pion.RTPCodecTypeAudio, pion.RTPTransceiverInit{Direction: pion.RTPTransceiverDirectionRecvonly}); err != nil {
		t.Fatal(err)
	}
	peer, err := e.NewPeerConnection("listener")
	if err != nil {
		t.Fatal(err)
	}
	offer, err := client.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	answer, err := peer.HandleOffer(offer.SDP)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.SetRemoteDescription(pion.SessionDescription{Type: pion.SDPTypeAnswer, SDP: answer}); err != nil {
		t.Fatal(err)
	}
	return e, peer, client
}

func newRemovalRaceTrack(t *testing.T, id string) *pion.TrackLocalStaticRTP {
	t.Helper()
	track, err := pion.NewTrackLocalStaticRTP(pion.RTPCodecCapability{MimeType: pion.MimeTypeOpus, ClockRate: 48000, Channels: 2}, id, "noxa-"+id)
	if err != nil {
		t.Fatal(err)
	}
	return track
}

// Speakers' outputs are added, offered, and removed again while the
// listener's answer is being applied (join-then-leave, or churn across
// overlapping renegotiations). Pion's RemoveTrack nils the sender's track
// before detaching the sender, while applying an answer starts every
// negotiated sender from a transceiver snapshot; a removal landing in between
// failed the whole SetRemoteDescription with "Sender Track has been removed
// or replaced to nil" and left the listener's other new outputs unstarted
// (silent pairs). Removals must never run concurrently with an exchange.
// The pion window is nanoseconds wide, so this stress test rarely trips it
// without the fix; it guards the serialization contract and the deferred
// removal bookkeeping. The voice chaos suite under load is what surfaced it.
func TestRemovalRacingServerOfferAnswerNeverBreaksIt(t *testing.T) {
	_, peer, client := newRemovalRaceClient(t)
	const rounds = 60
	for round := range rounds {
		var senders []*pion.RTPSender
		for i := range 6 {
			sender, err := peer.AddTrack(newRemovalRaceTrack(t, fmt.Sprintf("r%d-%d", round, i)))
			if err != nil {
				t.Fatal(err)
			}
			senders = append(senders, sender)
		}
		offer, err := peer.CreateOffer()
		if err != nil {
			t.Fatalf("round %d: offer: %v", round, err)
		}
		if err := client.SetRemoteDescription(pion.SessionDescription{Type: pion.SDPTypeOffer, SDP: offer}); err != nil {
			t.Fatal(err)
		}
		answer, err := client.CreateAnswer(nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := client.SetLocalDescription(answer); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		start := make(chan struct{})
		for _, sender := range senders[1:] {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				peer.removeSender(sender)
			}()
		}
		close(start)
		answerErr := peer.HandleAnswer(answer.SDP)
		wg.Wait()
		if answerErr != nil {
			t.Fatalf("round %d: answer racing removals failed: %v", round, answerErr)
		}
		for _, sender := range senders[1:] {
			if sender.Track() != nil {
				t.Fatalf("round %d: deferred removal was not applied once the exchange settled", round)
			}
		}
		if senders[0].Track() == nil {
			t.Fatalf("round %d: the surviving output lost its track", round)
		}
		peer.removeSender(senders[0])
	}
}

// The same race on a client-initiated offer (join, ICE restart): a removal
// that lands while the server is answering must not fail the local answer.
func TestRemovalDuringClientOfferIsDeferred(t *testing.T) {
	_, peer, client := newRemovalRaceClient(t)
	gone, err := peer.AddTrack(newRemovalRaceTrack(t, "gone"))
	if err != nil {
		t.Fatal(err)
	}
	// Pretend an answer is being built: the exchange is busy.
	peer.beginSignaling()
	peer.removeSender(gone)
	if gone.Track() == nil {
		t.Fatal("removal during an in-flight exchange must be deferred")
	}
	peer.endSignaling()
	if gone.Track() != nil {
		t.Fatal("deferred removal was not applied once stable")
	}
	if _, err := client.CreateOffer(nil); err != nil {
		t.Fatal(err)
	}
}

// Outside any exchange a removal applies immediately.
func TestRemovalWhenStableIsImmediate(t *testing.T) {
	_, peer, _ := newRemovalRaceClient(t)
	sender, err := peer.AddTrack(newRemovalRaceTrack(t, "now"))
	if err != nil {
		t.Fatal(err)
	}
	peer.removeSender(sender)
	if sender.Track() != nil {
		t.Fatal("stable removal was deferred")
	}
}
