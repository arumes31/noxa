package main

import (
	"testing"
	"time"

	"noxa/internal/netproto"
)

func TestStreamDiagnosticsCapabilityAndScope(t *testing.T) {
	app, cm, capture := voiceDiagnosticApp(t, false)
	cm.authorizationModel, cm.clientID = netproto.AuthorizationModelRolesV1, "self"
	if app.SupportsStreamDiagnosticsForTab("a") {
		t.Fatal("unsupported capability advertised")
	}
	if _, err := app.StreamDiagnosticsForTab("a", "pub", "screen", "1", "2"); err == nil {
		t.Fatal("unsupported request accepted")
	}
	cm.supportsStreamDiagnostics = true
	if !app.SupportsStreamDiagnosticsForTab("a") || app.SupportsStreamDiagnosticsForTab("missing") {
		t.Fatal("incorrect capability scope")
	}
	for _, args := range [][4]string{{"", "screen", "1", "2"}, {"pub", "mic", "1", "2"}, {"pub", "screen", "0", "2"}, {"pub", "screen", "1", "bad"}} {
		if _, err := app.StreamDiagnosticsForTab("a", args[0], args[1], args[2], args[3]); err == nil {
			t.Fatalf("invalid request accepted: %v", args)
		}
	}
	if capture.data.Len() != 0 {
		t.Fatal("invalid request written")
	}
}

func TestStreamDiagnosticsAcknowledgementLifetime(t *testing.T) {
	for _, wrong := range []string{"", "publisher", "slot", "generation", "session", "missing", "envelope"} {
		t.Run("wrong="+wrong, func(t *testing.T) {
			app, cm := newPipedApp(t, func(frame *netproto.Frame) (netproto.MessageType, any, bool) {
				var got netproto.VideoStreamControl
				if err := netproto.Decode(frame, &got); err != nil || got.Action != "diagnostics" || got.PublisherID != "pub" || got.Slot != "screen" || got.Generation != 9007199254740993 || got.Session != 2 {
					t.Errorf("wrong request: %+v / %v", got, err)
				}
				details := &netproto.VideoStreamDiagnostics{PublisherID: got.PublisherID, Slot: got.Slot, Generation: got.Generation, Session: got.Session, Layers: []netproto.VideoStreamLayerDiagnostics{}}
				reply := netproto.VideoStreamResult{Action: got.Action, PublisherID: got.PublisherID, Slot: got.Slot, Generation: got.Generation, Session: got.Session, Streams: []netproto.VideoStream{}, Diagnostics: details}
				switch wrong {
				case "publisher":
					details.PublisherID = "other"
				case "slot":
					details.Slot = "cam"
				case "generation":
					details.Generation++
				case "session":
					details.Session++
				case "missing":
					reply.Diagnostics = nil
				case "envelope":
					reply.Generation++
				}
				return netproto.MsgVideoStreamResult, reply, true
			})
			cm.mu.Lock()
			cm.authorizationModel, cm.clientID, cm.supportsStreamDiagnostics = netproto.AuthorizationModelRolesV1, "self", true
			cm.mu.Unlock()
			app.tabs, app.activeID = map[string]*tabState{"a": {cm: cm}}, "a"
			if _, err := app.StreamDiagnosticsForTab("missing", "pub", "screen", "9007199254740993", "2"); err == nil {
				t.Fatal("missing tab retargeted active tab")
			}
			_, err := app.StreamDiagnosticsForTab("a", "pub", "screen", "9007199254740993", "2")
			if (err == nil) != (wrong == "") {
				t.Fatalf("ack result = %v", err)
			}
		})
	}
}

func TestOnlyStreamDiagnosticReadCanDrain(t *testing.T) {
	for _, action := range []string{"diagnostics", "watch", "publish", "preview_upload"} {
		if isStreamDiagnosticsRead(netproto.MsgVideoStreamControl, netproto.VideoStreamControl{Action: action}) != (action == "diagnostics") {
			t.Fatalf("wrong timeout behavior for %q", action)
		}
	}
	if isStreamDiagnosticsRead(netproto.MsgVideoQuality, netproto.VideoStreamControl{Action: "diagnostics"}) {
		t.Fatal("mutation classified as diagnostic read")
	}
}

func TestStreamDiagnosticTimeoutPreservesVoiceAndDrainsLateReply(t *testing.T) {
	cm := newTestConnManager()
	peer := attachDrainTestPeer(t, cm)
	done := make(chan requestResult, 1)
	go func() {
		frame, err := cm.request(netproto.MsgVideoStreamControl, netproto.MsgVideoStreamResult, netproto.VideoStreamControl{Action: "diagnostics"}, 25*time.Millisecond)
		done <- requestResult{frame: frame, err: err}
	}()
	peer.next(t, netproto.MsgVideoStreamControl)
	if result := awaitDrainRequest(t, done); result.err == nil {
		t.Fatal("diagnostics did not time out")
	}
	if !cm.connected() {
		t.Fatal("diagnostics timeout interrupted voice")
	}
	peer.send(t, netproto.MsgVideoStreamResult, netproto.VideoStreamResult{Action: "diagnostics"})
	peer.barrier(t)
	cm.mu.Lock()
	_, pending := cm.pending[netproto.MsgVideoStreamResult]
	cm.mu.Unlock()
	if pending || !cm.connected() {
		t.Fatal("late diagnostic acknowledgement was not drained")
	}
}
