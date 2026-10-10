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

// High-motion late joins require an idle host and installed Playwright Chromium.
// Only synthetic canvas pixels leave isolated browser contexts, to a local SFU.
// Run: go test -tags integration ./internal/webrtc -run TestBrowserHighMotionFanout -v
// Set NOXA_BROWSER_MEDIA_PERF=1 to enable this CPU-intensive regression.
// NOXA_BROWSER_MEDIA_OUTPUT optionally preserves the complete stats artifact.
func TestBrowserHighMotionFanout(t *testing.T) {
	if os.Getenv("NOXA_BROWSER_MEDIA_PERF") != "1" {
		t.Skip("set NOXA_BROWSER_MEDIA_PERF=1 on an idle host for the high-motion 1080p60 profile")
	}
	runBrowserHighMotionFanout(t, 1920, 1080, 60)
}

func runBrowserHighMotionFanout(t *testing.T, width, height, fps int) {
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
	for _, id := range []string{"a", "viewer", "viewer2", "viewer3"} {
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
			ID, SDP, Slot, Generation, Viewer string
			QualityMode                       string `json:"quality_mode"`
			Active                            bool
			Tracks                            []struct {
				ID   string `json:"track_id"`
				Slot string `json:"slot"`
			}
		}
		if r.Method != http.MethodPost || json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body) != nil ||
			(body.ID != "a" && body.ID != "viewer" && body.ID != "viewer2" && body.ID != "viewer3") {
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
			generation, requestErr = router.PublishVideoWithMode(body.ID, body.Slot, generation, body.Active, body.QualityMode)
			result = map[string]any{"generation": strconv.FormatUint(generation, 10), "upload_active": false}
		case "/diagnostics":
			generation, _ := strconv.ParseUint(body.Generation, 10, 64)
			result, requestErr = router.StreamDiagnostics(body.ID, "a", SlotScreen, generation, router.VideoWatchSession(body.ID))
		case "/pacer":
			result = nil
			for _, publication := range router.VideoOperatorDiagnostics().Publications {
				if publication.PublisherID != "a" || publication.Slot != SlotScreen {
					continue
				}
				for _, viewer := range publication.Viewers {
					if viewer.ClientID == body.ID {
						result = viewer
					}
				}
			}
		case "/catalog":
			streams := []map[string]any{}
			for _, publication := range router.VideoPublications(body.ID) {
				streams = append(streams, map[string]any{"publisher_id": publication.PublisherID, "slot": publication.Slot,
					"generation": strconv.FormatUint(publication.Generation, 10), "upload_active": publication.UploadActive,
					"quality_mode": publication.QualityMode, "viewer_count": publication.ViewerCount})
			}
			result = map[string]any{"streams": streams}
		case "/viewer":
			peer, err := newPeer(body.ID)
			if err != nil {
				requestErr = err
				break
			}
			for _, publication := range router.VideoPublications(body.ID) {
				_, requestErr = router.WatchVideo(body.ID, publication.PublisherID, publication.Slot, publication.Generation, 1, router.VideoWatchSession(body.ID), true)
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
			requestErr = engine.PeerConnection(body.ID).HandleAnswer(body.SDP)
		case "/watch":
			requestErr = ErrVideoPublication
			if body.Viewer != "viewer" && body.Viewer != "viewer2" && body.Viewer != "viewer3" {
				break
			}
			for _, publication := range router.VideoPublications(body.Viewer) {
				if publication.PublisherID == body.ID && publication.Slot == SlotScreen {
					_, requestErr = router.WatchVideo(body.Viewer, body.ID, SlotScreen, publication.Generation, publication.WatchRevision+1, router.VideoWatchSession(body.Viewer), body.Active)
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
	ctx, cancel := context.WithTimeout(t.Context(), 180*time.Second)
	defer cancel()
	script, err := filepath.Abs("testdata/browser_fanout.mjs")
	if err != nil {
		t.Fatal(err)
	}
	resultPath := filepath.Join(t.TempDir(), "fanout.json")
	if directory := os.Getenv("NOXA_BROWSER_MEDIA_OUTPUT"); directory != "" {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		resultPath = filepath.Join(directory, "browser-fanout-stats.json")
	}
	command := exec.CommandContext(ctx, node, script, server.URL, strconv.Itoa(width), strconv.Itoa(height), strconv.Itoa(fps), resultPath)
	output, err := command.CombinedOutput()
	t.Logf("real browser media results:\n%s", output)
	if err != nil {
		t.Fatalf("browser media integration: %v", err)
	}
}
