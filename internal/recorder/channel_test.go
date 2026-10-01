package recorder

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pion/rtp"
	"noxa/internal/webrtc"
)

func autoRecordingProcess(_ context.Context, _ string, args ...string) Command {
	cmd := newFakeCommand(args[len(args)-1])
	go func() {
		select {
		case <-cmd.quitRequested:
			_ = cmd.Kill()
		case <-cmd.waitDone:
		}
	}()
	return cmd
}

func commitRecordingPacket(t *testing.T, capture *channelCapture, packet *rtp.Packet, delivery webrtc.MediaDelivery) {
	t.Helper()
	committed := make(chan struct{}, 1)
	deadline := time.After(2 * time.Second)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		checks := 0
		if err := capture.WriteMedia(packet, delivery, func(write func() error) error {
			checks++
			err := write()
			if checks == 2 && err == nil {
				select {
				case committed <- struct{}{}:
				default:
				}
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
		select {
		case <-committed:
			return
		case <-deadline:
			t.Fatal("packet never committed to recording input")
		case <-ticker.C:
		}
	}
}

func TestChannelRecordingCapacityFailureIsExplicit(t *testing.T) {
	cfg := testConfig(privateTempDir(t))
	cfg.MaxConcurrent = 1
	c := NewChannelRecorder(cfg, testLogger())
	c.processes.Exec = autoRecordingProcess
	if _, err := c.Start(context.Background(), 1, &fakeTapRouter{}); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	capture := c.sessions[1]
	c.mu.Unlock()
	for _, publisher := range []string{"alice", "bob"} {
		if err := capture.WriteMedia(&rtp.Packet{Header: rtp.Header{SSRC: 1}, Payload: []byte{1}}, webrtc.MediaDelivery{SenderID: publisher, Slot: webrtc.SlotMic}, func(write func() error) error { return write() }); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-capture.done:
	case <-time.After(2 * time.Second):
		t.Fatal("capacity failure did not clean up")
	}
	if !errors.Is(capture.result, ErrCapacity) {
		t.Fatalf("capacity failure hidden: %v", capture.result)
	}
	if c.processes.SessionCount() != 0 {
		t.Fatal("process budget leaked")
	}
}

func TestChannelRecordingRechecksAtUDPSocket(t *testing.T) {
	listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	tap, err := NewTap(listener.LocalAddr().(*net.UDPAddr).Port)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tap.Close() }()
	now := time.Now()
	s := &channelCapture{ctx: context.Background(), started: now, children: map[captureSource]*captureChild{
		{"alice", webrtc.SlotMic}: {session: &Session{audioTap: tap}, track: &captureTrack{SSRC: 1}, readyAt: now.Add(-time.Second)},
	}}
	calls := 0
	entry := capturePacket{packet: &rtp.Packet{Header: rtp.Header{SSRC: 1}, Payload: []byte{1}}, delivery: webrtc.MediaDelivery{SenderID: "alice", Slot: webrtc.SlotMic}, at: now, commit: func(write func() error) error {
		calls++
		if calls == 1 {
			return write()
		}
		return nil
	}}
	if err := s.consume(entry); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("final authorization checks=%d", calls)
	}
	if err := listener.SetReadDeadline(time.Now().Add(25 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := listener.ReadFromUDP(make([]byte, 1500)); err == nil {
		t.Fatal("revoked packet reached FFmpeg socket")
	}
}

func TestSourceRecordingRequiresCoordinatorRoot(t *testing.T) {
	r := New(testConfig(privateTempDir(t)), testLogger())
	r.Exec = func(context.Context, string, ...string) Command {
		t.Error("process launched in replacement directory")
		return nil
	}
	root, err := openRecordingRoot(privateTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if _, err := r.start(context.Background(), 1, &fakeTapRouter{}, "audio", root); err == nil {
		t.Fatal("different directory identity accepted")
	}
}

func TestRecordingRejectsUnsupportedVideoBeforeAllocation(t *testing.T) {
	s := &channelCapture{children: make(map[captureSource]*captureChild)}
	err := s.consume(capturePacket{packet: &rtp.Packet{}, delivery: webrtc.MediaDelivery{Slot: webrtc.SlotCam, Codec: "video/H264"}, commit: func(write func() error) error { return write() }})
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unsupported codec accepted: %v", err)
	}
}

func TestChannelRecordingRetainsFailedChildStartup(t *testing.T) {
	c := NewChannelRecorder(testConfig(privateTempDir(t)), testLogger())
	c.processes.killWait = 20 * time.Millisecond
	command := newWedgedCommand()
	t.Cleanup(func() {
		closeIfOpen(command.stdin.release)
		closeIfOpen(command.killRelease)
		command.waitOnce.Do(func() { close(command.waitDone) })
	})
	if _, err := c.Start(context.Background(), 1, &fakeTapRouter{}); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	capture := c.sessions[1]
	c.mu.Unlock()
	c.processes.Exec = autoRecordingProcess
	if err := capture.WriteMedia(&rtp.Packet{Header: rtp.Header{SSRC: 1}, Payload: []byte{1}}, webrtc.MediaDelivery{SenderID: "healthy", Slot: webrtc.SlotMic}, func(write func() error) error { return write() }); err != nil {
		t.Fatal(err)
	}
	var healthy *Session
	deadline := time.Now().Add(time.Second)
	for healthy == nil && time.Now().Before(deadline) {
		c.processes.mu.Lock()
		for _, child := range c.processes.sessions {
			healthy = child
		}
		c.processes.mu.Unlock()
		if healthy == nil {
			time.Sleep(time.Millisecond)
		}
	}
	if healthy == nil {
		t.Fatal("healthy sibling not started")
	}
	c.processes.Exec = func(_ context.Context, _ string, args ...string) Command {
		capture.cancel()
		command.output = args[len(args)-1]
		return command
	}
	if err := capture.WriteMedia(&rtp.Packet{Header: rtp.Header{SSRC: 1}, Payload: []byte{1}}, webrtc.MediaDelivery{SenderID: "alice", Slot: webrtc.SlotMic}, func(write func() error) error { return write() }); err != nil {
		t.Fatal(err)
	}
	select {
	case <-command.killStarted:
	case <-time.After(time.Second):
		t.Fatal("child cleanup not reached")
	}
	select {
	case <-healthy.done:
	case <-time.After(time.Second):
		t.Fatal("wedged child prevented healthy sibling shutdown")
	}
	// Let the bounded process startup return its timeout; channel ownership
	// must survive beyond that timeout until the wedged child is reaped.
	time.Sleep(60 * time.Millisecond)
	if _, err := c.Start(context.Background(), 1, &fakeTapRouter{}); !errors.Is(err, ErrAlreadyRecording) {
		t.Fatalf("channel released before child cleanup: %v", err)
	}
	closeIfOpen(command.stdin.release)
	closeIfOpen(command.killRelease)
	select {
	case <-capture.done:
	case <-time.After(time.Second):
		t.Fatal("late child cleanup did not release channel")
	}
	if !errors.Is(capture.result, ErrStartupCleanupTimeout) {
		t.Fatalf("startup timeout missing: %v", capture.result)
	}
}

func TestChannelRecordingCapturesEveryMediaSlot(t *testing.T) {
	c := NewChannelRecorder(testConfig(privateTempDir(t)), testLogger())
	c.processes.Exec = autoRecordingProcess
	session, err := c.Start(context.Background(), 1, &fakeTapRouter{})
	if err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	capture := c.sessions[1]
	c.mu.Unlock()
	for _, slot := range []string{webrtc.SlotMic, webrtc.SlotScreenAudio, webrtc.SlotCam, webrtc.SlotScreen} {
		codec := "audio/opus"
		if slot == webrtc.SlotCam || slot == webrtc.SlotScreen {
			codec = "video/VP8"
		}
		commitRecordingPacket(t, capture, &rtp.Packet{Header: rtp.Header{Version: 2, SSRC: 42},
			Payload: []byte{0x10, 0, 0, 0, 0x9d, 1, 0x2a, 0, 0, 0, 0}},
			webrtc.MediaDelivery{SenderID: "alice", Slot: slot, Codec: codec})
	}
	deadline := time.Now().Add(time.Second)
	for c.processes.SessionCount() != 4 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if c.processes.SessionCount() != 4 {
		t.Fatal("microphone, screen audio, camera and screen did not get independent inputs")
	}
	if err := c.Stop(1); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(session.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest captureManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Tracks) != 4 {
		t.Fatalf("lost media slots: %s", raw)
	}
}
func TestSingleStreamRecordingInputs(t *testing.T) {
	for _, kind := range []string{"audio", "video"} {
		sdp := buildStreamSDP(10000, 10002, kind)
		other := "video"
		if kind == "video" {
			other = "audio"
		}
		if !strings.Contains(sdp, "m="+kind) || strings.Contains(sdp, "m="+other) {
			t.Fatalf("absent input advertised: %s", sdp)
		}
	}
}

func TestChannelRecordingSeparatesParticipantsAndSourceEpochs(t *testing.T) {
	dir := privateTempDir(t)
	c := NewChannelRecorder(testConfig(dir), testLogger())
	c.processes.Exec = autoRecordingProcess
	t.Cleanup(func() { _ = c.Close() })
	session, err := c.Start(context.Background(), 1, &fakeTapRouter{})
	if err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	capture := c.sessions[1]
	c.mu.Unlock()
	for _, publisher := range []string{"alice", "bob"} {
		packet := &rtp.Packet{Header: rtp.Header{Version: 2, SSRC: 42, SequenceNumber: 1}, Payload: []byte{1, 2, 3}}
		commitRecordingPacket(t, capture, packet, webrtc.MediaDelivery{SenderID: publisher, Slot: webrtc.SlotMic, SourceEpoch: 1})
	}
	deadline := time.Now().Add(2 * time.Second)
	for c.processes.SessionCount() != 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if c.processes.SessionCount() != 2 {
		t.Fatal("overlapping RTP SSRCs were collapsed across participants")
	}
	packet := &rtp.Packet{Header: rtp.Header{Version: 2, SSRC: 42, SequenceNumber: 1}, Payload: []byte{1, 2, 3}}
	commitRecordingPacket(t, capture, packet, webrtc.MediaDelivery{SenderID: "alice", Slot: webrtc.SlotMic, SourceEpoch: 2})
	barrier := make(chan struct{}, 1)
	if err := capture.WriteMedia(packet, webrtc.MediaDelivery{SenderID: "bob", Slot: webrtc.SlotMic, SourceEpoch: 1}, func(write func() error) error {
		select {
		case barrier <- struct{}{}:
		default:
		}
		return write()
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-barrier:
	case <-time.After(2 * time.Second):
		t.Fatal("epoch switch stuck")
	}
	if err := c.Stop(1); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(session.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest captureManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Tracks) != 3 || manifest.EndedAt == nil {
		t.Fatalf("wrong manifest: %s", raw)
	}
	files := map[string]bool{}
	for _, track := range manifest.Tracks {
		if files[track.File] {
			t.Fatal("source files overlap")
		}
		files[track.File] = true
		if _, err := os.Stat(filepath.Join(dir, track.File)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestChannelRecordingDeniesBeforeProcessAllocation(t *testing.T) {
	c := NewChannelRecorder(testConfig(privateTempDir(t)), testLogger())
	c.processes.Exec = func(context.Context, string, ...string) Command {
		t.Error("revoked media allocated a subprocess")
		return nil
	}
	if _, err := c.Start(context.Background(), 1, &fakeTapRouter{}); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	capture := c.sessions[1]
	c.mu.Unlock()
	checked := make(chan struct{})
	err := capture.WriteMedia(&rtp.Packet{Header: rtp.Header{SSRC: 1}, Payload: []byte{1}}, webrtc.MediaDelivery{SenderID: "alice", Slot: webrtc.SlotMic}, func(func() error) error { close(checked); return nil })
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-checked:
	case <-time.After(time.Second):
		t.Fatal("buffer did not recheck permission")
	}
	if err := c.Stop(1); err != nil {
		t.Fatal(err)
	}
}
