//go:build integration

package webrtc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	pion "github.com/pion/webrtc/v4"
)

// Requires the frontend's installed Playwright Chromium and Node, not a live
// noXa server. Only synthetic canvas pixels leave isolated browser contexts.
// Run: go test -tags integration ./internal/webrtc -run TestBrowserScreenSimulcast -v
// Set NOXA_BROWSER_MEDIA_PERF=1 for the additional 1080p60 software-encoder profile.
func TestBrowserScreenSimulcast(t *testing.T) {
	for _, profile := range []struct {
		name               string
		width, height, fps int
	}{{"720p30", 1280, 720, 30}, {"1080p60", 1920, 1080, 60}} {
		t.Run(profile.name, func(t *testing.T) {
			if profile.fps == 60 && os.Getenv("NOXA_BROWSER_MEDIA_PERF") != "1" {
				t.Skip("set NOXA_BROWSER_MEDIA_PERF=1 on an idle host for the 1080p60 profile")
			}
			runBrowserScreenSimulcast(t, profile.width, profile.height, profile.fps)
		})
	}
}

func runBrowserScreenSimulcast(t *testing.T, width, height, fps int) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("Node is required for the browser media integration test")
	}
	engine, err := NewWithNetwork(testLogger(), nil, false, NetworkConfig{UDPAddr: "127.0.0.1:0", ExternalIPs: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	router := NewRouter(nil)
	for _, id := range []string{"a", "b", "viewer"} {
		router.JoinChannel(1, id)
	}
	newPeer := func(id string) (*PeerConnectionWrapper, error) {
		peer, err := engine.NewPeerConnection(id)
		if err != nil {
			return nil, err
		}
		if err := router.AttachPeer(id, peer); err != nil {
			return nil, err
		}
		router.EnsurePublishers(id)
		router.PrepareSubscriber(id)
		return peer, nil
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ID, SDP, Slot, Generation, Quality string
			Active                             bool
			Tracks                             []struct {
				ID   string `json:"track_id"`
				Slot string `json:"slot"`
			}
		}
		if r.Method != http.MethodPost || json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body) != nil ||
			(body.ID != "a" && body.ID != "b" && body.ID != "viewer") {
			http.Error(w, "invalid test request", http.StatusBadRequest)
			return
		}
		var result any = struct{}{}
		var requestErr error
		switch r.URL.Path {
		case "/offer":
			slots := make(map[string]string)
			for _, track := range body.Tracks {
				slots[track.ID] = track.Slot
			}
			router.SetTrackSlots(body.ID, slots)
			peer, err := newPeer(body.ID)
			if err != nil {
				requestErr = err
				break
			}
			gathered := pion.GatheringCompletePromise(peer.pc)
			_, requestErr = peer.HandleOffer(body.SDP)
			if requestErr == nil {
				select {
				case <-gathered:
					result = peer.pc.LocalDescription()
				case <-r.Context().Done():
					requestErr = r.Context().Err()
				}
			}
		case "/publication":
			generation, _ := strconv.ParseUint(body.Generation, 10, 64)
			generation, requestErr = router.PublishVideo(body.ID, body.Slot, generation, body.Active)
			result = map[string]string{"generation": strconv.FormatUint(generation, 10)}
		case "/viewer":
			peer, err := newPeer("viewer")
			if err != nil {
				requestErr = err
				break
			}
			for _, publication := range router.VideoPublications("viewer") {
				_, requestErr = router.WatchVideo("viewer", publication.PublisherID, publication.Slot, publication.Generation, 1, router.VideoWatchSession("viewer"), true)
				if requestErr != nil {
					break
				}
			}
			if requestErr != nil {
				break
			}
			gathered := pion.GatheringCompletePromise(peer.pc)
			_, requestErr = peer.CreateOffer()
			if requestErr == nil {
				select {
				case <-gathered:
					result = peer.pc.LocalDescription()
				case <-r.Context().Done():
					requestErr = r.Context().Err()
				}
			}
		case "/answer":
			requestErr = engine.PeerConnection("viewer").HandleAnswer(body.SDP)
		case "/quality":
			requestErr = ErrVideoPublication
			for _, publication := range router.VideoPublications("viewer") {
				if publication.PublisherID == body.ID && publication.Slot == SlotScreen {
					requestErr = router.SetStreamVideoQuality("viewer", body.ID, SlotScreen, publication.Generation, router.VideoWatchSession("viewer"), body.Quality)
				}
			}
		default:
			http.NotFound(w, r)
			return
		}
		if requestErr != nil {
			http.Error(w, requestErr.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	}))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Second)
	defer cancel()
	script, err := filepath.Abs("testdata/browser_simulcast.mjs")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, node, script, server.URL, strconv.Itoa(width), strconv.Itoa(height), strconv.Itoa(fps))
	output, err := command.CombinedOutput()
	t.Logf("real browser media results:\n%s", output)
	if err != nil {
		t.Fatalf("browser media integration: %v", err)
	}
}
