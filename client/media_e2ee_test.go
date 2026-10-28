package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"noxa/internal/e2ee/admission"
	"noxa/internal/netproto"
)

// Delivered media-key controls reach the frontend as the "media_key" event
// of their tab; every other control kind is not media and is not relayed.
func TestRelayMediaKeyForwardsOnlyMediaKeys(t *testing.T) {
	rec := &eventRecorder{}
	m := newConnManager(nil)
	m.sink = rec
	m.relayMediaKey(RatchetControl{Kind: "other", Data: json.RawMessage(`{"x":1}`)})
	m.relayMediaKey(RatchetControl{Kind: mediaKeyControlKind})
	data := json.RawMessage(`{"v":1,"client":"c1","keys":{}}`)
	m.relayMediaKey(RatchetControl{Sender: "u1", SenderDevice: "d1", Kind: mediaKeyControlKind, Scope: admission.ControlScope{Type: "channel-media", ID: "5"}, Epoch: 7, ExpiresAt: 9, Data: data})
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.events) != 1 || rec.events[0].name != "media_key" {
		t.Fatalf("events: %+v", rec.events)
	}
	ev, err := json.Marshal(mediaKeyEvent{Sender: "u1", SenderDevice: "d1", Kind: mediaKeyControlKind, Data: data})
	if err != nil || !strings.Contains(string(ev), `"data":{"v":1`) {
		t.Fatalf("event JSON keeps data as an object: %s %v", ev, err)
	}
	// The relay is registered from the first session on (never dropped as
	// "no handler registered").
	if len(m.ratchetControls) == 0 {
		t.Fatal("media-key relay not registered at construction")
	}
}

// The frontend's media-key body (media-keys.js buildMediaKeyBody) is an
// admissible Task 1c control.
func TestMediaKeyRequestBuildsAnAdmissibleControl(t *testing.T) {
	now := time.Now().Unix()
	data := `{"v":1,"client":"c-1","session":"abcdefghijklmnopqrstuv","kid":3,"members":"` + strings.Repeat("m", 43) + `","n":4,"keys":{"mic":"` +
		strings.Repeat("A", 43) + `=","cam":"` + strings.Repeat("B", 43) + `=","screen":"` + strings.Repeat("C", 43) + `=","screenaudio":"` + strings.Repeat("D", 43) + `="}}`
	body := admission.ControlBody{Kind: mediaKeyControlKind, Scope: admission.ControlScope{Type: "channel-media", ID: "42"}, Epoch: 1800000000123, ExpiresAt: now + 120, Data: json.RawMessage(data)}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admission.ParseControlBody(raw); err != nil {
		t.Fatalf("media-key body refused: %v", err)
	}
	if got := admission.ControlDisposition(raw, now); got != "delivered" {
		t.Fatalf("disposition %q", got)
	}
	if archived, _ := json.Marshal(admission.ControlArchiveBody(raw)); strings.Contains(string(archived), "keys") {
		t.Fatalf("key data kept at rest: %s", archived)
	}
}

func TestSendMediaKeyRefusesOtherKindsAndEmptyData(t *testing.T) {
	m := newConnManager(nil)
	for _, req := range []MediaKeyRequest{{Kind: "chat", Data: json.RawMessage(`{}`)}, {Kind: mediaKeyControlKind}} {
		if out, err := m.sendMediaKey(req); err == nil || out.Code != "invalid" {
			t.Fatalf("%+v: %+v %v", req, out, err)
		}
	}
}

func TestMediaE2EEStatusExplainsWhyMediaCannotStart(t *testing.T) {
	m := newConnManager(nil)
	if st := m.mediaE2EEStatus(); st.Supported || st.Reason != "server_outdated" {
		t.Fatalf("old server: %+v", st)
	}
	m.supportsMediaE2EE, m.uniqueID = true, "guest:abc"
	if st := m.mediaE2EEStatus(); st.Reason != "keyless_guest" {
		t.Fatalf("keyless guest: %+v", st)
	}
	m.uniqueID = "member"
	if st := m.mediaE2EEStatus(); st.Reason != "no_device" {
		t.Fatalf("no device: %+v", st)
	}
	m.deviceID = "dev"
	if st := m.mediaE2EEStatus(); st.Reason != "secure_unavailable" {
		t.Fatalf("no secure session: %+v", st)
	}
	if !mediaE2EESupported([]string{netproto.CapabilityMediaE2EE}) || mediaE2EESupported(nil) {
		t.Fatal("capability detection")
	}
}

func TestMediaKeyPriorityShortensTheRatchetDrain(t *testing.T) {
	m := newConnManager(nil)
	if got := m.ratchetPoll(5 * time.Second); got != 5*time.Second {
		t.Fatalf("idle poll %v", got)
	}
	m.mediaKeyPriority.Store(true)
	if got := m.ratchetPoll(5 * time.Second); got != mediaKeyPoll {
		t.Fatalf("voice poll %v", got)
	}
}

// The connection benchmark sends unencrypted synthetic RTP: it is declined
// on a server that requires encrypted channel media.
func TestConnectionBenchmarkDeclinedUnderMediaE2EE(t *testing.T) {
	if !strings.HasPrefix(errConnectionBenchmarkE2EE.Error(), "connection_benchmark_unavailable_e2ee") {
		t.Fatal(errConnectionBenchmarkE2EE)
	}
}
