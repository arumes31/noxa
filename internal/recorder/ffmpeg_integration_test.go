//go:build integration && linux

package recorder

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pion/rtp"
	"noxa/internal/webrtc"
)

// Exercise the actual UDP demux thread, including a quiet interval before stop.
// Mock processes cannot detect a quit command that leaves that thread blocked.
func TestFFmpegFinalizesIdleAudio(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is required")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe is required")
	}
	r := New(testConfig(privateTempDir(t)), testLogger())
	t.Cleanup(func() { _ = r.Close() })
	s, err := r.start(context.Background(), 1, &fakeTapRouter{}, "audio", nil)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	for i := range 150 {
		packet := &rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 111,
			SSRC: 42, SequenceNumber: uint16(i), Timestamp: uint32(i * 960)},
			Payload: []byte{0xf8, 0xff, 0xfe}} // valid Opus silence
		if err := s.audioTap.WriteRTP(packet); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(1500 * time.Millisecond)
	if err := r.Stop(1); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-count_packets",
		"-select_streams", "a:0", "-show_entries", "stream=nb_read_packets",
		"-of", "default=nw=1:nk=1", s.FilePath).CombinedOutput()
	if err != nil {
		t.Fatalf("ffprobe: %v: %s", err, output)
	}
	packets, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil || packets < 100 {
		t.Fatalf("recording lacks expected decodable packets: %q (%v)", output, err)
	}
}

func TestFFmpegDiscardsUnstartedSource(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is required")
	}
	r := New(testConfig(privateTempDir(t)), testLogger())
	t.Cleanup(func() { _ = r.Close() })
	s, err := r.start(context.Background(), 1, &fakeTapRouter{}, "video", nil)
	if err != nil {
		t.Fatal(err)
	}
	s.discardEmpty.Store(true)
	if err := r.Stop(1); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.FilePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty source artifact remains: %v", err)
	}
}

func TestFFmpegChannelStopBeforeFirstFrame(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is required")
	}
	c := NewChannelRecorder(testConfig(privateTempDir(t)), testLogger())
	t.Cleanup(func() { _ = c.Close() })
	if _, err := c.Start(context.Background(), 1, &fakeTapRouter{}); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	capture := c.sessions[1]
	c.mu.Unlock()
	if err := capture.WriteMedia(&rtp.Packet{Header: rtp.Header{SSRC: 42}, Payload: []byte{1}},
		webrtc.MediaDelivery{SenderID: "alice", Slot: webrtc.SlotCam, Codec: "video/VP8"},
		func(write func() error) error { return write() }); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for c.processes.SessionCount() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if c.processes.SessionCount() != 1 {
		t.Fatal("source process did not start")
	}
	if err := c.Stop(1); err != nil {
		t.Fatal(err)
	}
	if c.processes.SessionCount() != 0 {
		t.Fatal("empty source process remains")
	}
}
