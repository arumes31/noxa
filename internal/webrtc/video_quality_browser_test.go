//go:build integration

package webrtc

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	pion "github.com/pion/webrtc/v4"
)

// Requires the frontend's installed Playwright Chromium and Node, not a live
// noXa server. Only synthetic canvas pixels leave isolated browser contexts.
// Run: go test -tags integration ./internal/webrtc -run TestBrowserScreenSingleSource -v
// Set NOXA_BROWSER_MEDIA_PERF=1 for the additional 1080p60 software-encoder profile.
func TestBrowserScreenSingleSource(t *testing.T) {
	for _, profile := range []struct {
		name               string
		width, height, fps int
	}{{"720p30", 1280, 720, 30}, {"1080p60", 1920, 1080, 60}} {
		t.Run(profile.name, func(t *testing.T) {
			if profile.fps == 60 && os.Getenv("NOXA_BROWSER_MEDIA_PERF") != "1" {
				t.Skip("set NOXA_BROWSER_MEDIA_PERF=1 on an idle host for the 1080p60 profile")
			}
			runBrowserScreenSingleSource(t, profile.width, profile.height, profile.fps)
		})
	}
}

func runBrowserScreenSingleSource(t *testing.T, width, height, fps int) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("Node is required for the browser media integration test")
	}
	engine, err := NewWithNetwork(testLogger(), nil, false, NetworkConfig{UDPAddr: "127.0.0.1:0", ExternalIPs: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	var stopLTE func()
	t.Cleanup(func() {
		if stopLTE != nil {
			stopLTE()
		}
	})
	router := NewRouter(nil)
	for _, id := range []string{"a", "b", "viewer", "viewer2"} {
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
			(body.ID != "a" && body.ID != "b" && body.ID != "viewer" && body.ID != "viewer2") {
			http.Error(w, "invalid test request", http.StatusBadRequest)
			return
		}
		var result any = struct{}{}
		var requestErr error
		switch r.URL.Path {
		case "/lte":
			if os.Getenv("NOXA_BROWSER_MEDIA_LTE") != "1" {
				http.NotFound(w, r)
				return
			}
			if stopLTE != nil {
				stopLTE()
				stopLTE = nil
			}
			if body.Active {
				stopLTE = browserLTEEgress()
			}
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
			if body.Viewer != "viewer" && body.Viewer != "viewer2" {
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
	timeout := 110 * time.Second
	if os.Getenv("NOXA_BROWSER_MEDIA_LTE") == "1" {
		timeout += 70 * time.Second
	}
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
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

// Exercise real UDP, including RTCP feedback: browser DevTools network rules
// do not reliably affect WebRTC on every platform. This test-only shaper adds
// 250ms delay, deterministic 2% loss and a 10Mbps per-destination ceiling.
func browserLTEEgress() func() {
	var mu sync.Mutex
	var pending sync.WaitGroup
	active := true
	counts := map[string]uint64{}
	next := map[string]time.Time{}
	shape := func(packet []byte, addr net.Addr, write func([]byte) error) error {
		mu.Lock()
		if !active {
			mu.Unlock()
			return write(packet)
		}
		key := addr.String()
		counts[key]++
		if counts[key]%50 == 0 {
			mu.Unlock()
			return nil
		}
		now := time.Now()
		due := next[key]
		if due.Before(now) {
			due = now
		}
		// Bound the delay queue even if a faulty publisher exceeds the ceiling.
		if due.Sub(now) > time.Second {
			mu.Unlock()
			return nil
		}
		due = due.Add(time.Duration(len(packet)) * time.Second / 1250000)
		next[key] = due
		copyOfPacket := append([]byte(nil), packet...)
		pending.Add(1)
		time.AfterFunc(time.Until(due.Add(250*time.Millisecond)), func() {
			defer pending.Done()
			_ = write(copyOfPacket)
		})
		mu.Unlock()
		return nil
	}
	mediaSocketShape.Store(&shape)
	return func() {
		mediaSocketShape.Store(nil)
		mu.Lock()
		active = false
		mu.Unlock()
		pending.Wait()
	}
}
