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
// NOXA_BROWSER_MEDIA_VIEWERS=1|5|20 selects a shared-host capacity profile.
// NOXA_BROWSER_MEDIA_STAGE_SECONDS=10..60 (default 15) controls each stage.
// NOXA_BROWSER_MEDIA_CODEC=h264|vp8 pins and verifies the capacity test codec.
// NOXA_BROWSER_MEDIA_SLOW_WRITE_MS=1..20 delays only the last viewer's video
// writer during a bounded middle stage. This is not network jitter emulation.
func TestBrowserHighMotionFanout(t *testing.T) {
	if os.Getenv("NOXA_BROWSER_MEDIA_PERF") != "1" {
		t.Skip("set NOXA_BROWSER_MEDIA_PERF=1 on an idle host for the high-motion 1080p60 profile")
	}
	runBrowserHighMotionFanout(t, 1920, 1080, 60)
}

func runBrowserHighMotionFanout(t *testing.T, width, height, fps int) {
	profile, err := parseBrowserFanoutProfile(os.Getenv("NOXA_BROWSER_MEDIA_VIEWERS"), os.Getenv("NOXA_BROWSER_MEDIA_STAGE_SECONDS"), os.Getenv("NOXA_BROWSER_MEDIA_SLOW_WRITE_MS"), os.Getenv("NOXA_BROWSER_MEDIA_CODEC"))
	if err != nil {
		t.Fatal(err)
	}
	budget := 180 * time.Second
	if profile.Capacity {
		budget = time.Duration(profile.Viewers*15+profile.StageSeconds*3+60) * time.Second
	}
	ctx, cancel := context.WithTimeout(t.Context(), budget)
	defer cancel()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("Node is required for the browser media integration test")
	}
	engine, err := NewWithNetwork(testLogger(), nil, false, NetworkConfig{UDPAddr: "127.0.0.1:0", ExternalIPs: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	engines := map[string]*Engine{}
	delay := &browserVideoWriteDelay{ctx: ctx, delay: time.Duration(profile.SlowWriteMS) * time.Millisecond}
	viewerIDs := make([]string, profile.Viewers)
	for index := range viewerIDs {
		viewerIDs[index] = "viewer"
		if index > 0 {
			viewerIDs[index] += strconv.Itoa(index + 1)
		}
		engines[viewerIDs[index]] = engine
	}
	if profile.SlowWriteMS > 0 {
		slow, err := NewWithNetwork(testLogger(), nil, false, NetworkConfig{UDPAddr: "127.0.0.1:0", ExternalIPs: []string{"127.0.0.1"}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = slow.Close() })
		if err := installBrowserVideoWriteDelay(slow, delay); err != nil {
			t.Fatal(err)
		}
		engines[viewerIDs[len(viewerIDs)-1]] = slow
	}
	engines["a"] = engine
	var testCodecs []string
	if profile.Codec == "h264" {
		testCodecs = []string{"h264", "vp8"}
	}
	router := NewRouter(nil)
	for id := range engines {
		router.JoinChannel(1, id)
	}
	newPeer := func(id string) (*PeerConnectionWrapper, error) {
		if peer := engines[id].PeerConnection(id); peer != nil {
			return peer, nil
		}
		peer, err := engines[id].NewPeerConnection(id)
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
			engines[body.ID] == nil {
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
			generation, requestErr = router.PublishVideoWithCodecs(body.ID, body.Slot, generation, body.Active, body.QualityMode, testCodecs)
			result = map[string]any{"generation": strconv.FormatUint(generation, 10), "upload_active": false}
		case "/diagnostics":
			generation, _ := strconv.ParseUint(body.Generation, 10, 64)
			result, requestErr = router.StreamDiagnostics(body.ID, "a", SlotScreen, generation, router.VideoWatchSession(body.ID))
		case "/codec":
			router.mu.RLock()
			result = router.videoCodecs[videoSourceRef{"a", SlotScreen}].MimeType
			router.mu.RUnlock()
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
				_, requestErr = router.WatchVideoWithCodecs(body.ID, publication.PublisherID, publication.Slot, publication.Generation, 1, router.VideoWatchSession(body.ID), true, testCodecs)
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
			peer := engines[body.ID].PeerConnection(body.ID)
			if peer == nil {
				http.Error(w, "unknown test peer", http.StatusBadRequest)
				return
			}
			requestErr = peer.HandleAnswer(body.SDP)
			if requestErr == nil {
				router.RequestKeyframe("a", SlotScreen, "")
			}
		case "/impairment":
			if profile.SlowWriteMS == 0 || body.ID != viewerIDs[len(viewerIDs)-1] {
				http.Error(w, "not the configured slow viewer", http.StatusBadRequest)
				return
			}
			if body.Active {
				delay.until.Store(time.Now().Add(time.Duration(profile.StageSeconds+5) * time.Second).UnixNano())
			} else {
				delay.until.Store(0)
			}
			result = map[string]any{"delayed_packets": delay.packets.Load(), "slow_write_ms": profile.SlowWriteMS, "expires_at_ms": delay.until.Load() / int64(time.Millisecond)}
		case "/watch":
			requestErr = ErrVideoPublication
			if body.Viewer == "a" || engines[body.Viewer] == nil {
				break
			}
			for _, publication := range router.VideoPublications(body.Viewer) {
				if publication.PublisherID == body.ID && publication.Slot == SlotScreen {
					_, requestErr = router.WatchVideoWithCodecs(body.Viewer, body.ID, SlotScreen, publication.Generation, publication.WatchRevision+1, router.VideoWatchSession(body.Viewer), body.Active, testCodecs)
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
	script, err := filepath.Abs("testdata/browser_fanout.mjs")
	if err != nil {
		t.Fatal(err)
	}
	resultPath := filepath.Join(t.TempDir(), "fanout.json")
	if directory := os.Getenv("NOXA_BROWSER_MEDIA_OUTPUT"); directory != "" {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		name := "browser-fanout-stats.json"
		if profile.Capacity {
			name = "browser-fanout-" + strconv.Itoa(profile.Viewers) + "-viewers-" + profile.Codec + "-slow-" + strconv.Itoa(profile.SlowWriteMS) + "ms-stats.json"
		}
		resultPath = filepath.Join(directory, name)
	}
	configuration, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, node, script, server.URL, strconv.Itoa(width), strconv.Itoa(height), strconv.Itoa(fps), resultPath, string(configuration))
	// The script's earlier watchdog writes evidence and closes its owned browser.
	// Bound inherited pipes as a fallback if the hard process deadline fires.
	command.WaitDelay = 5 * time.Second
	output, err := command.CombinedOutput()
	t.Logf("real browser media results:\n%s", output)
	if err != nil {
		t.Fatalf("browser media integration: %v", err)
	}
}
