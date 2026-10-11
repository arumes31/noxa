//go:build integration

package webrtc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	pion "github.com/pion/webrtc/v4"
)

// Real encoders/decoders catch codec-label and packet rewriting errors that
// packet-shaped unit fixtures cannot detect. No screen pixels or public server.
func TestBrowserSingleCodecSwitch(t *testing.T) {
	engine, err := NewWithNetwork(testLogger(), nil, false, NetworkConfig{UDPAddr: "127.0.0.1:0", ExternalIPs: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	repair := newVideoRepairProxy(t, engine.udpMux.GetListenAddresses()[0])
	ingressRepair := newVideoRepairProxy(t, engine.udpMux.GetListenAddresses()[0])
	router := NewRouter(nil)
	router.JoinChannel(1, "pub")
	router.JoinChannel(1, "sub")
	router.JoinChannel(1, "legacy")
	router.SetTrackSlots("pub", map[string]string{"screen": SlotScreen})
	generation, err := router.PublishVideoWithCodecs("pub", SlotScreen, 0, true, "source", []string{"h264", "vp8"})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ID, SDP  string
			Active   bool
			Revision uint64
			Codecs   []string
			Tracks   []struct {
				ID   string `json:"track_id"`
				Slot string `json:"slot"`
			}
		}
		if r.Method != http.MethodPost || json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body) != nil || (body.ID != "pub" && body.ID != "sub" && body.ID != "legacy") {
			http.Error(w, "invalid request", 400)
			return
		}
		var result any = struct{}{}
		var requestErr error
		peer := engine.PeerConnection(body.ID)
		if peer == nil {
			peer, requestErr = engine.NewPeerConnection(body.ID)
			if requestErr == nil {
				requestErr = router.AttachPeer(body.ID, peer)
			}
			if requestErr != nil {
				http.Error(w, requestErr.Error(), 500)
				return
			}
			router.EnsurePublishers(body.ID)
			router.PrepareSubscriber(body.ID)
		}
		switch r.URL.Path {
		case "/offer":
			slots := make(map[string]string)
			for _, track := range body.Tracks {
				slots[track.ID] = track.Slot
			}
			router.SetTrackSlots(body.ID, slots)
			gathered := pion.GatheringCompletePromise(peer.pc)
			_, requestErr = peer.HandleOffer(body.SDP)
			if requestErr == nil {
				select {
				case <-gathered:
					result = ingressRepair.description(peer.pc.LocalDescription())
				case <-r.Context().Done():
					requestErr = r.Context().Err()
				}
			}
		case "/viewer":
			_, requestErr = router.WatchVideoWithCodecs(body.ID, "pub", SlotScreen, generation, body.Revision, router.VideoWatchSession(body.ID), true, body.Codecs)
			if requestErr != nil {
				break
			}
			gathered := pion.GatheringCompletePromise(peer.pc)
			_, requestErr = peer.CreateOffer()
			if requestErr == nil {
				select {
				case <-gathered:
					result = peer.pc.LocalDescription()
					if body.ID == "sub" {
						result = repair.description(peer.pc.LocalDescription())
					}
				case <-r.Context().Done():
					requestErr = r.Context().Err()
				}
			}
		case "/answer":
			requestErr = peer.HandleAnswer(body.SDP)
			router.RequestKeyframe("pub", SlotScreen, "")
		case "/watch":
			_, requestErr = router.WatchVideoWithCodecs(body.ID, "pub", SlotScreen, generation, body.Revision, router.VideoWatchSession(body.ID), body.Active, body.Codecs)
		case "/receiver-reset":
			// Exercise repeated recovery without thirty seconds per attempt.
			// Only this local integration fixture bypasses cooldown; the real
			// cumulative growth budget is preserved across every reset.
			peer.signalMu.Lock()
			peer.videoRecoveryLast = time.Time{}
			peer.signalMu.Unlock()
			requestErr = router.ResetStreamReceiver(body.ID, "pub", SlotScreen, generation, router.VideoWatchSession(body.ID), body.Revision)
			peer.signalMu.Lock()
			result = map[string]int{"transceivers": len(peer.pc.GetTransceivers()), "growth": peer.videoRecoveryGrowth}
			peer.signalMu.Unlock()
		case "/selected":
			for _, stream := range router.VideoPublications("pub") {
				if stream.Slot == SlotScreen {
					result = stream.Codec
				}
			}
		case "/codec":
			router.mu.RLock()
			result = router.videoCodecs[videoSourceRef{"pub", SlotScreen}].MimeType
			router.mu.RUnlock()
		case "/loss":
			router.mu.RLock()
			output := router.pubTracks["sub"]["pub"].video[SlotScreen]
			sender := output.sender
			router.mu.RUnlock()
			encoding := sender.GetParameters().Encodings[0]
			repair.repairSSRC.Store(uint32(encoding.RTX.SSRC))
			repair.dropSSRC.Store(uint32(encoding.SSRC))
			result = map[string]uint32{"dropped": repair.dropped.Load(), "repaired": repair.repaired.Load()}
		case "/loss-state":
			result = map[string]uint32{"dropped": repair.dropped.Load(), "repaired": repair.repaired.Load()}
		case "/ingress-loss":
			for _, receiver := range peer.pc.GetReceivers() {
				if track := receiver.Track(); track != nil && track.Kind() == pion.RTPCodecTypeVideo {
					ingressRepair.repairSSRC.Store(uint32(track.RtxSSRC()))
					ingressRepair.dropSSRC.Store(uint32(track.SSRC()))
					break
				}
			}
			result = map[string]uint32{"dropped": ingressRepair.dropped.Load(), "repaired": ingressRepair.repaired.Load()}
		case "/ingress-loss-state":
			result = map[string]uint32{"dropped": ingressRepair.dropped.Load(), "repaired": ingressRepair.repaired.Load()}
		default:
			http.NotFound(w, r)
			return
		}
		if requestErr != nil {
			http.Error(w, requestErr.Error(), 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	}))
	t.Cleanup(server.Close)
	script, err := filepath.Abs("testdata/browser_codec.mjs")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "node", script, server.URL).CombinedOutput()
	t.Logf("browser codec results:\n%s", output)
	if err != nil {
		t.Fatal(err)
	}
}
