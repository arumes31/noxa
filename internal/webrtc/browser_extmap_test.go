//go:build integration

package webrtc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"testing"
	"time"

	pion "github.com/pion/webrtc/v4"
)

// Chromium retains the extension IDs allocated by a rolled-back first-video
// offer. A crossing server offer must not assign those IDs to another URI.
func TestBrowserFirstVideoOfferCollisionPreservesExtensions(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required")
	}
	engine, err := NewWithNetwork(testLogger(), nil, false, NetworkConfig{UDPAddr: "127.0.0.1:0", ExternalIPs: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	peer, err := engine.NewPeerConnection("browser")
	if err != nil {
		t.Fatal(err)
	}
	received := make(chan string, 4)
	peer.OnTrack(func(track *pion.TrackRemote, receiver *pion.RTPReceiver) {
		if track.Kind() != pion.RTPCodecTypeVideo {
			return
		}
		packet, _, err := track.ReadRTP()
		if err != nil {
			return
		}
		for _, ext := range receiver.GetParameters().HeaderExtensions {
			if ext.URI == "urn:ietf:params:rtp-hdrext:sdes:rtp-stream-id" && track.RID() != "" && string(packet.GetExtension(uint8(ext.ID))) == track.RID() {
				select {
				case received <- track.RID():
				default:
				}
			}
		}
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ SDP string }
		if r.Method != http.MethodPost || json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body) != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		var sdp string
		var requestErr error
		switch r.URL.Path {
		case "/initial", "/offer":
			gathered := pion.GatheringCompletePromise(peer.pc)
			sdp, requestErr = peer.HandleOffer(body.SDP)
			if requestErr == nil && r.URL.Path == "/initial" {
				select {
				case <-gathered:
					sdp = peer.pc.LocalDescription().SDP
				case <-r.Context().Done():
					requestErr = r.Context().Err()
				}
			}
		case "/received":
			seen := map[string]bool{}
			timer := time.NewTimer(8 * time.Second)
			defer timer.Stop()
			for !seen["f"] && !seen["h"] {
				select {
				case rid := <-received:
					seen[rid] = true
				case <-timer.C:
					http.Error(w, fmt.Sprintf("missing RID RTP: %v", seen), 500)
					return
				case <-r.Context().Done():
					return
				}
			}
			_ = json.NewEncoder(w).Encode(seen)
			return
		case "/parameters":
			var params []pion.RTPHeaderExtensionParameter
			for _, receiver := range peer.pc.GetReceivers() {
				if receiver.Track() == nil || receiver.Track().Kind() == pion.RTPCodecTypeVideo {
					params = append(params, receiver.GetParameters().HeaderExtensions...)
				}
			}
			_ = json.NewEncoder(w).Encode(params)
			return
		case "/reoffer":
			sdp, requestErr = peer.CreateOffer()
		case "/crossing":
			_, requestErr = peer.pc.AddTransceiverFromKind(pion.RTPCodecTypeVideo)
			if requestErr == nil {
				sdp, requestErr = peer.CreateOffer()
			}
		case "/answer":
			requestErr = peer.HandleAnswer(body.SDP)
		default:
			http.NotFound(w, r)
			return
		}
		if requestErr != nil {
			http.Error(w, requestErr.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"sdp": sdp})
	}))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, node, "testdata/browser_extmap.mjs", server.URL)
	output, err := command.CombinedOutput()
	t.Logf("browser extmap negotiation: %s", output)
	if err != nil {
		t.Fatalf("browser extension integration: %v", err)
	}
}
