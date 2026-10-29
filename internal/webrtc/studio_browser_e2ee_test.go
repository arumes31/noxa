//go:build integration

package webrtc

import (
	"bufio"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Desktop Studio output (Task 3) through the ACTUAL SFU with mandatory media
// E2EE: a headless Chromium "desktop" page runs the real Studio compositor
// (studio-compositor.js, studio-captures.js, studio-state.js, studio-audio.js)
// and publishes its program with the real video-publication.js
// startStudioShare -> startShare -> encrypted screen publication path
// (channel-media.js + video-negotiation.js, Task 2 worker). A second desktop
// page receives, decrypts and samples the decoded pixels. Captures are
// synthetic colored canvases; no capture hardware is involved.
//
// Proven: the receiver decodes the program only after decryption with the
// sender's key; draft changes never reach it before Take; Take (cut) and
// crossfade change the decoded picture without replacing the published
// track; a revoked source turns neutral at the receiver; the recorder-style
// tap sees only library-encrypted frames.
//
//	go test -tags integration -run TestBrowserStudioOutputThroughSFU -v ./internal/webrtc/
func TestBrowserStudioOutputThroughSFU(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required")
	}
	frontend, _ := filepath.Abs("../../client/frontend")
	if _, err := os.Stat(filepath.Join(frontend, "node_modules", "vite")); err != nil {
		t.Skip("client/frontend node_modules are not installed")
	}
	h := newE2EEHarness(t)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	cmd := exec.Command(node, filepath.Join("testdata", "browser_studio.mjs"), srv.URL)
	cmd.Dir, _ = filepath.Abs(".")
	cmd.Env = append(os.Environ(), "NOXA_FRONTEND="+frontend)
	out, _ := cmd.StdoutPipe()
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var lines []string
	var linesMu sync.Mutex
	done := make(chan struct{})
	go func() {
		scanner := bufio.NewScanner(out)
		scanner.Buffer(make([]byte, 1<<20), 1<<20)
		for scanner.Scan() {
			linesMu.Lock()
			lines = append(lines, scanner.Text())
			linesMu.Unlock()
			t.Log(scanner.Text())
		}
		close(done)
	}()
	waitErr := cmd.Wait()
	<-done
	if waitErr != nil {
		t.Fatalf("studio scenario failed: %v", waitErr)
	}
	linesMu.Lock()
	passed := len(lines) > 0 && strings.Contains(lines[len(lines)-1], "STUDIO SCENARIO PASSED")
	linesMu.Unlock()
	if !passed {
		t.Fatal("studio scenario did not report success")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	t.Logf("tap: audio packets %d (encrypted %d), video frames %d (encrypted %d)", h.tapAudio, h.tapAudioEncrypted, h.tapVideoFrames, h.tapVideoEncrypted)
	// The Studio program (screen slot) and its audio reached the recorder
	// position only as library-encrypted frames.
	if h.tapVideoFrames == 0 || h.tapAudio == 0 || h.tapVideoFrames != h.tapVideoEncrypted || h.tapAudio != h.tapAudioEncrypted {
		t.Fatalf("tap saw non-encrypted studio media: audio %d/%d video %d/%d", h.tapAudioEncrypted, h.tapAudio, h.tapVideoEncrypted, h.tapVideoFrames)
	}
	for _, answer := range h.answers {
		lower := strings.ToLower(answer)
		if strings.Contains(lower, "h264/") || strings.Contains(lower, "h265/") || strings.Contains(lower, "vp9/") || strings.Contains(lower, "av1/") {
			t.Fatalf("SFU answered an unproven codec while media is encrypted:\n%s", answer)
		}
	}
}
