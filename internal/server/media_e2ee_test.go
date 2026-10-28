package server

import (
	"net"
	"slices"
	"strings"
	"testing"

	"noxa/internal/config"
	"noxa/internal/netproto"
)

// Encrypted channel media is the production default: New() requires it and
// the shared fixtures only switch it off to exercise pre-E2EE semantics.
func startMediaE2EEEnv(t *testing.T, directory bool) *testEnv {
	t.Helper()
	mutate := func(d *Deps) {}
	if directory {
		mutate = func(d *Deps) { d.Devices = &olmFakeDevices{} }
	}
	env := startTestEnvLogger(t, nil, nil, mutate, nil, func(s *TCPServer) { s.mediaE2EE = true })
	t.Cleanup(env.stop)
	return env
}

func protocolError(t *testing.T, conn net.Conn) netproto.Error {
	t.Helper()
	var e netproto.Error
	if err := netproto.Decode(readOfType(t, conn, netproto.MsgError), &e); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestMediaE2EEIsTheDefaultAndAdvertisedWithTheDirectory(t *testing.T) {
	if !New(&config.Config{}, nil, &Deps{}).mediaE2EE {
		t.Fatal("encrypted channel media must be required by default")
	}
	env := startMediaE2EEEnv(t, true)
	conn, response := keyLogin(t, env.addr, newLinkTestKeys(t))
	t.Cleanup(func() { _ = conn.Close() })
	if !response.OK || !slices.Contains(response.Capabilities, netproto.CapabilityMediaE2EE) {
		t.Fatalf("capabilities: %+v", response.Capabilities)
	}
	if slices.Contains(response.Capabilities, netproto.CapabilityStreamHEVC) {
		t.Fatal("HEVC publication must not be offered while media is encrypted")
	}
}

func TestMediaE2EEOfferRefusalsAreExplicitAndNeverReachTheSFU(t *testing.T) {
	env := startMediaE2EEEnv(t, true)
	conn, userID := dialAuthed(t, env.addr, "user-uid")
	t.Cleanup(func() { _ = conn.Close() })
	placeVoiceTestClients(t, env, 1, userID)

	// An old client (no declaration) gets the update reason.
	send(t, conn, netproto.MsgWebRTCOffer, netproto.WebRTCOffer{SDP: "plain-offer"})
	if e := protocolError(t, conn); e.Code != errCodePermissionDenied || !strings.HasPrefix(e.Message, netproto.MediaE2EEReasonClientOutdated+": ") {
		t.Fatalf("old client: %+v", e)
	}
	// A declaring session without a secure device cannot receive keys.
	send(t, conn, netproto.MsgWebRTCOffer, netproto.WebRTCOffer{SDP: "secure-offer", MediaE2EE: netproto.MediaE2EEVersion})
	if e := protocolError(t, conn); !strings.HasPrefix(e.Message, netproto.MediaE2EEReasonUnavailable+": ") {
		t.Fatalf("no device: %+v", e)
	}
	// A wrong version is an old/foreign client too.
	send(t, conn, netproto.MsgWebRTCOffer, netproto.WebRTCOffer{SDP: "x", MediaE2EE: "noxa-media-e2ee-v0"})
	if e := protocolError(t, conn); !strings.HasPrefix(e.Message, netproto.MediaE2EEReasonClientOutdated) {
		t.Fatalf("wrong version: %+v", e)
	}
	env.voice.mu.Lock()
	offers := len(env.voice.offers)
	env.voice.mu.Unlock()
	if offers != 0 {
		t.Fatalf("refused offers reached the voice backend: %d", offers)
	}

	// Keyless guests get their own explanation.
	guest, response := dialGuest(t, env.addr, "visitor", "")
	t.Cleanup(func() { _ = guest.Close() })
	if !response.OK {
		t.Skipf("guest login disabled in fixture: %s", response.Reason)
	}
	placeVoiceTestClients(t, env, 1, response.ClientID)
	send(t, guest, netproto.MsgWebRTCOffer, netproto.WebRTCOffer{SDP: "guest-offer", MediaE2EE: netproto.MediaE2EEVersion})
	if e := protocolError(t, guest); !strings.HasPrefix(e.Message, netproto.MediaE2EEReasonKeylessGuest+": ") {
		t.Fatalf("keyless guest: %+v", e)
	}
}

func TestMediaE2EEOfferWithDeviceIsAnsweredAndAnnouncesTheDevice(t *testing.T) {
	env := startMediaE2EEEnv(t, true)
	env.voice.answerSDP = "server-answer"
	conn, userID := dialAuthed(t, env.addr, "user-uid")
	t.Cleanup(func() { _ = conn.Close() })
	watcher, watcherID := dialAuthed(t, env.addr, "admin-uid")
	t.Cleanup(func() { _ = watcher.Close() })
	placeVoiceTestClients(t, env, 1, userID, watcherID)
	client, ok := env.srv.clientByID(userID)
	if !ok {
		t.Fatal("client missing")
	}
	client.setDeviceID("dev-1")
	send(t, conn, netproto.MsgWebRTCOffer, netproto.WebRTCOffer{SDP: "secure-offer", MediaE2EE: netproto.MediaE2EEVersion})
	var answer netproto.WebRTCAnswer
	if err := netproto.Decode(readOfType(t, conn, netproto.MsgWebRTCAnswer), &answer); err != nil || answer.SDP != "server-answer" {
		t.Fatalf("answer %+v %v", answer, err)
	}
	waitFor(t, "media device recorded", func() bool {
		c, ok := env.state.GetClient(userID)
		return ok && c.MediaDevice == "dev-1"
	})
	// Other members learn it from their (role-filtered) snapshot.
	waitFor(t, "media device in a member snapshot", func() bool {
		f := readOfType(t, watcher, netproto.MsgSnapshot)
		return strings.Contains(string(f.Payload), `"media_device":"dev-1"`)
	})
}

func TestMediaE2EEUnavailableWithoutTheDeviceDirectory(t *testing.T) {
	env := startMediaE2EEEnv(t, false)
	conn, userID := dialAuthed(t, env.addr, "user-uid")
	t.Cleanup(func() { _ = conn.Close() })
	placeVoiceTestClients(t, env, 1, userID)
	send(t, conn, netproto.MsgWebRTCOffer, netproto.WebRTCOffer{SDP: "secure-offer", MediaE2EE: netproto.MediaE2EEVersion})
	if e := protocolError(t, conn); !strings.HasPrefix(e.Message, netproto.MediaE2EEReasonUnavailable+": ") {
		t.Fatalf("no directory: %+v", e)
	}
}

func TestMediaE2EERefusesServerRecordingButStopsAnExistingOne(t *testing.T) {
	env := startMediaE2EEEnv(t, true)
	admin, _ := dialAuthed(t, env.addr, "admin-uid")
	t.Cleanup(func() { _ = admin.Close() })
	env.state.AddChannel(testChannel(1))
	send(t, admin, netproto.MsgRecordingControl, netproto.RecordingControl{ChannelID: 1, Action: "start"})
	if e := protocolError(t, admin); !strings.HasPrefix(e.Message, netproto.MediaE2EEReasonRecording+": ") || !strings.Contains(e.Message, "local recording") {
		t.Fatalf("recording start: %+v", e)
	}
	if got := env.recorder.startedCount(); got != 0 {
		t.Fatalf("recorder started %d times: no media keys may reach a recorder", got)
	}
	// A recording that predates the requirement is stopped and finalized
	// through the ordinary stop path (files and retention unchanged).
	send(t, admin, netproto.MsgRecordingControl, netproto.RecordingControl{ChannelID: 1, Action: "stop"})
	waitFor(t, "existing recording stopped", func() bool {
		env.recorder.mu.Lock()
		defer env.recorder.mu.Unlock()
		return len(env.recorder.stopped) == 1 && env.recorder.stopped[0] == 1
	})
}

func TestMediaE2EERefusesWhisperAndPlaintextPreviews(t *testing.T) {
	env := startMediaE2EEEnv(t, true)
	conn, userID := dialAuthed(t, env.addr, "user-uid")
	t.Cleanup(func() { _ = conn.Close() })
	placeVoiceTestClients(t, env, 1, userID)
	send(t, conn, netproto.MsgWhisperSet, netproto.WhisperSet{ChannelIDs: []int64{2}, Active: true})
	if e := protocolError(t, conn); !strings.HasPrefix(e.Message, netproto.MediaE2EEReasonWhisper+": ") {
		t.Fatalf("whisper: %+v", e)
	}
	env.voice.mu.Lock()
	whispers := len(env.voice.whispers)
	env.voice.mu.Unlock()
	if whispers != 0 {
		t.Fatal("whisper routing reached the voice backend")
	}
	for _, action := range []string{"preview_upload", "preview"} {
		send(t, conn, netproto.MsgVideoStreamControl, netproto.VideoStreamControl{Action: action, Slot: "cam", PublisherID: userID, Generation: 1, JPEG: []byte{0xff, 0xd8}})
		if e := protocolError(t, conn); !strings.HasPrefix(e.Message, netproto.MediaE2EEReasonPreview+": ") {
			t.Fatalf("%s: %+v", action, e)
		}
	}
}

func TestMediaE2EEKeepsOnlyVP8InStreamCodecDeclarations(t *testing.T) {
	s := &TCPServer{mediaE2EE: true}
	msg := netproto.VideoStreamControl{Action: "publish", Codecs: []string{"h265", "h264", "VP8"}, HEVCLevelID: 93}
	s.mediaE2EECodecs(&msg)
	if !slices.Equal(msg.Codecs, []string{"VP8"}) || msg.HEVCLevelID != 0 {
		t.Fatalf("codecs %+v level %d", msg.Codecs, msg.HEVCLevelID)
	}
}
