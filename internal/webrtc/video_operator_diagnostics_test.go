package webrtc

import (
	"fmt"
	"testing"
	"time"

	"noxa/internal/netproto"
)

func TestVideoOperatorDiagnosticsSeparatesWatchOutputAndForwarding(t *testing.T) {
	r := NewRouter(nil)
	for _, client := range []string{"pub", "watcher", "idle", "hidden"} {
		r.JoinChannel(1, client)
	}
	r.JoinChannel(2, "elsewhere")
	r.SetPublisherGuard(func(access PublisherAccess) bool { return access.SubscriberID != "hidden" })
	generation, err := r.PublishVideoWithMode("pub", SlotScreen, 0, true, "source")
	if err != nil {
		t.Fatal(err)
	}
	session := r.VideoWatchSession("watcher")
	if _, err := r.WatchVideo("watcher", "pub", SlotScreen, generation, 1, session, true); err != nil {
		t.Fatal(err)
	}
	initial := r.VideoOperatorDiagnostics()
	if len(initial.Publications) != 1 || len(initial.Publications[0].Viewers) != 2 {
		t.Fatalf("snapshot included hidden/out-of-channel viewer: %+v", initial)
	}
	idle, watcher := initial.Publications[0].Viewers[0], initial.Publications[0].Viewers[1]
	if idle.ClientID != "idle" || idle.Watching || watcher.ClientID != "watcher" || !watcher.Watching || watcher.OutputExists || watcher.Forwarding != nil {
		t.Fatalf("watch intent was confused with output delivery: %+v", initial.Publications[0].Viewers)
	}
	egress := &mediaEgressStream{active: true, pacer: &mediaPacer{bitrate: mediaInitialBitrate}}
	egress.outputSSRC.Store(99)
	r.pubTracks["watcher"] = map[string]*pubTrack{"pub": {video: map[string]*pubSlot{SlotScreen: {egress: egress}}}}
	watch := r.watches[watchKey{"watcher", "pub", SlotScreen}]
	packet := continuityPacket(99, 1, 100, 1, true)
	egress.observeVideo(&packet.Header, packet.MarshalSize(), &mediaTicket{id: 1, videoSource: 42, videoMedia: true,
		delivery: MediaDelivery{Slot: SlotScreen, Publication: generation, WatchEpoch: watch.epoch}}, time.Now())
	active := r.VideoOperatorDiagnostics().Publications[0].Viewers[1]
	if !active.OutputExists || !active.OutputActive || active.OutputSSRC != 99 || active.Pacer == nil || active.Forwarding == nil || active.Forwarding.Stage.Packets != 1 {
		t.Fatalf("active output missing actual forwarding evidence: %+v", active)
	}
	egress.stop()
	if r.VideoOperatorDiagnostics().Publications[0].Viewers[1].OutputActive {
		t.Fatal("retired output reported active")
	}
	if _, err := r.WatchVideo("watcher", "pub", SlotScreen, generation, 2, session, false); err != nil {
		t.Fatal(err)
	}
	stopped := r.VideoOperatorDiagnostics().Publications[0].Viewers[1]
	if stopped.Watching || stopped.Forwarding != nil || stopped.WatchRevision != 2 {
		t.Fatalf("unwatch retained current delivery state: %+v", stopped)
	}
	r.RevokeVideo("pub", SlotScreen)
	if len(r.VideoOperatorDiagnostics().Publications) != 0 {
		t.Fatal("revoked publication retained in snapshot")
	}
}

func TestVideoOperatorDiagnosticsBoundsAndOrdersResults(t *testing.T) {
	r := NewRouter(nil)
	for i := range netproto.MaxVideoOperatorPublications + 2 {
		client := fmt.Sprintf("pub-%02d", i)
		r.JoinChannel(1, client)
		if _, err := r.PublishVideo(client, SlotCam, 0, true); err != nil {
			t.Fatal(err)
		}
	}
	result := r.VideoOperatorDiagnostics()
	if !result.Truncated || len(result.Publications) != netproto.MaxVideoOperatorPublications {
		t.Fatalf("unbounded publication output: %d", len(result.Publications))
	}
	for i, publication := range result.Publications {
		if publication.PublisherID != fmt.Sprintf("pub-%02d", i) || !publication.ViewersTruncated || len(publication.Viewers) != netproto.MaxVideoOperatorViewers {
			t.Fatalf("unordered/unbounded viewer output: %+v", publication)
		}
	}
}
