//go:build integration

package webrtc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/pion/rtp"
	pion "github.com/pion/webrtc/v4"
)

// Observe the actual sender binding and write a MID-bearing single-encoding
// picture. Check the bound RID IDs separately; the browser must decode the RTP.
type browserExtmapOutbound struct {
	*pion.TrackLocalStaticRTP
	mu         sync.Mutex
	extensions []pion.RTPHeaderExtensionParameter
	sent       int
	boundRIDID int
	sequence   uint16
}

func (track *browserExtmapOutbound) Bind(ctx pion.TrackLocalContext) (pion.RTPCodecParameters, error) {
	codec, err := track.TrackLocalStaticRTP.Bind(ctx)
	if err == nil {
		track.mu.Lock()
		track.extensions = append([]pion.RTPHeaderExtensionParameter(nil), ctx.HeaderExtensions()...)
		track.mu.Unlock()
	}
	return codec, err
}

func (track *browserExtmapOutbound) snapshot() any {
	track.mu.Lock()
	defer track.mu.Unlock()
	return struct {
		Extensions []pion.RTPHeaderExtensionParameter `json:"extensions"`
		Sent       int                                `json:"sent"`
		BoundRIDID int                                `json:"boundRIDID"`
	}{append([]pion.RTPHeaderExtensionParameter(nil), track.extensions...), track.sent, track.boundRIDID}
}

func (track *browserExtmapOutbound) sendProbe(mid string) {
	track.mu.Lock()
	var ridID, midID int
	for _, ext := range track.extensions {
		switch ext.URI {
		case "urn:ietf:params:rtp-hdrext:sdes:rtp-stream-id":
			ridID = ext.ID
		case "urn:ietf:params:rtp-hdrext:sdes:mid":
			midID = ext.ID
		}
	}
	track.sequence++
	sequence := track.sequence
	track.mu.Unlock()
	if ridID == 0 || midID == 0 || mid == "" {
		return
	}
	header := rtp.Header{Version: 2, SequenceNumber: sequence, Timestamp: uint32(sequence) * 4500, Marker: true}
	// The SFU sends one selected encoding, so there is no outbound RID in SDP.
	// Exercise its actual MID-bearing packet path while checking the bound RID IDs too.
	if header.SetExtension(uint8(midID), []byte(mid)) != nil {
		return
	}
	// Pinned LiveKit neutral VP8 picture, covered by media-e2ee-licenses.txt.
	payload := []byte{0x10, 0x10, 0x02, 0x00, 0x9d, 0x01, 0x2a, 0x08, 0x00, 0x08, 0x00, 0x00, 0x47, 0x08, 0x85, 0x85, 0x88, 0x85, 0x84, 0x88, 0x02, 0x02, 0x00, 0x0c, 0x0d, 0x60, 0x00, 0xfe, 0xff, 0xab, 0x50, 0x80}
	if track.WriteRTP(&rtp.Packet{Header: header, Payload: payload}) == nil {
		track.mu.Lock()
		track.sent++
		track.boundRIDID = ridID
		track.mu.Unlock()
	}
}

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
	// This standalone negotiation fixture has no router to issue egress tickets.
	// Use the same media policy with standalone pacing for its direct test track.
	media, interceptors, err := newEngineMedia(false, engine.videoBounds, nil)
	if err != nil {
		t.Fatal(err)
	}
	engine.api = pion.NewAPI(pion.WithMediaEngine(media), pion.WithSettingEngine(engine.settingEngine), pion.WithInterceptorRegistry(interceptors))
	peer, err := engine.NewPeerConnection("browser")
	if err != nil {
		t.Fatal(err)
	}
	local, err := pion.NewTrackLocalStaticRTP(pion.RTPCodecCapability{MimeType: pion.MimeTypeVP8}, "server-video", "server-stream")
	if err != nil {
		t.Fatal(err)
	}
	outgoing := &browserExtmapOutbound{TrackLocalStaticRTP: local}
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
			_ = json.NewEncoder(w).Encode(map[string]any{"receivers": params, "outgoing": outgoing.snapshot()})
			return
		case "/local-offer":
			sdp = peer.pc.LocalDescription().SDP
		case "/reoffer":
			sdp, requestErr = peer.CreateOffer()
		case "/crossing":
			_, requestErr = peer.pc.AddTransceiverFromTrack(outgoing)
			if requestErr == nil {
				sdp, requestErr = peer.CreateOffer()
			}
			if requestErr == nil {
				go func() {
					ticker := time.NewTicker(50 * time.Millisecond)
					defer ticker.Stop()
					for {
						select {
						case <-t.Context().Done():
							return
						case <-ticker.C:
							for _, tr := range peer.pc.GetTransceivers() {
								if tr.Sender() != nil && tr.Sender().Track() == outgoing {
									outgoing.sendProbe(tr.Mid())
								}
							}
						}
					}
				}()
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
