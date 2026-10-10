package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

type streamDiagnosticVoice struct{ fakeVoice }

func (v *streamDiagnosticVoice) StreamDiagnostics(subscriber, publisher, slot string, generation, session uint64) (*netproto.VideoStreamDiagnostics, error) {
	result, err := v.videoRouter().StreamDiagnostics(subscriber, publisher, slot, generation, session)
	if err == nil {
		result.Layers = []netproto.VideoStreamLayerDiagnostics{{SSRC: 123, RID: "f", StartedAt: time.Now().Add(-time.Second).UnixMilli()}}
	}
	return result, err
}

func TestStreamDiagnosticsMemberAccessRechecksPublicationSessionAndPermission(t *testing.T) {
	backend := serverRoleFixture()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.Connect, authorization.ViewChannel, authorization.ShareScreen}
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	voice := &streamDiagnosticVoice{}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority; d.Voice = voice })
	t.Cleanup(env.stop)
	env.state.AddChannel(testChannel(1))
	pub, pubID := dialAuthed(t, env.addr, "user-uid")
	t.Cleanup(func() { _ = pub.Close() })
	sub, subID := dialAuthed(t, env.addr, "admin-uid") // Ordinary member, despite the fixture's nickname.
	t.Cleanup(func() { _ = sub.Close() })
	for _, id := range []string{pubID, subID} {
		if err := env.state.MoveClient(id, 1); err != nil {
			t.Fatal(err)
		}
		voice.videoRouter().JoinChannel(1, id)
	}
	generation, err := voice.PublishVideo(pubID, "screen", 0, true)
	if err != nil {
		t.Fatal(err)
	}
	request := netproto.VideoStreamControl{Action: "diagnostics", PublisherID: pubID, Slot: "screen", Generation: generation, Session: voice.VideoWatchSession(subID)}
	report := voiceTelemetryFixture()
	report.VideoSenders = []netproto.VideoSenderDiagnostics{{SSRC: 123, RID: "f", Slot: "screen", Generation: generation}}
	send(t, pub, netproto.MsgVoiceTelemetry, report)
	voiceTelemetryBarrier(t, pub)
	send(t, sub, netproto.MsgVideoStreamControl, request)
	var result netproto.VideoStreamResult
	if err := netproto.Decode(readOfType(t, sub, netproto.MsgVideoStreamResult), &result); err != nil {
		t.Fatal(err)
	}
	if result.Action != "diagnostics" || result.Diagnostics == nil || result.Diagnostics.SenderReport == nil || len(result.Diagnostics.SenderReport.Rows) != 1 || result.Diagnostics.Forwarding != nil {
		t.Fatalf("eligible member cannot inspect unwatched publication: %+v", result)
	}
	if got := queryVoiceDiagnostics(t, sub, pubID); got != nil {
		t.Fatal("stream access must not grant private voice diagnostics")
	}
	// A leave/rejoin invalidates telemetry even if SSRC and publication remain
	// unchanged in the fixture's independent SFU state.
	env.state.AddChannel(testChannel(2))
	for _, channel := range []int64{2, 1} {
		if err := env.state.MoveClient(pubID, channel); err != nil {
			t.Fatal(err)
		}
		if channel == 2 {
			send(t, sub, netproto.MsgVideoStreamControl, request)
			readError(t, sub)
		}
	}
	send(t, sub, netproto.MsgVideoStreamControl, request)
	if err := netproto.Decode(readOfType(t, sub, netproto.MsgVideoStreamResult), &result); err != nil {
		t.Fatal(err)
	}
	if result.Diagnostics == nil || result.Diagnostics.SenderReport != nil {
		t.Fatal("telemetry escaped publisher channel epoch")
	}
	for _, alter := range []func(*netproto.VideoStreamControl){
		func(m *netproto.VideoStreamControl) { m.Generation++ },
		func(m *netproto.VideoStreamControl) { m.Session++ },
		func(m *netproto.VideoStreamControl) { m.PublisherID = "missing" },
		func(m *netproto.VideoStreamControl) { m.Slot = "cam" },
		func(m *netproto.VideoStreamControl) { m.JPEG = []byte("unexpected media") },
	} {
		invalid := request
		alter(&invalid)
		send(t, sub, netproto.MsgVideoStreamControl, invalid)
		readError(t, sub)
	}
	frame, err := netproto.Encode(netproto.MsgVideoStreamControl, request)
	if err != nil {
		t.Fatal(err)
	}
	frame.Payload = append(frame.Payload, []byte(strings.Repeat(" ", 4096))...)
	if err := netproto.WriteFrame(sub, frame); err != nil {
		t.Fatal(err)
	}
	readError(t, sub)
	voice.videoRouter().JoinChannel(2, subID)
	send(t, sub, netproto.MsgVideoStreamControl, request)
	readError(t, sub)
	voice.videoRouter().JoinChannel(1, subID)
	request.Session = voice.VideoWatchSession(subID)
	backend.mu.Lock()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.Connect}
	backend.policy.Revision++
	backend.mu.Unlock()
	if err := authority.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	send(t, sub, netproto.MsgVideoStreamControl, request)
	readError(t, sub)
}

func TestStreamDiagnosticsAndTelemetryRequireAuthentication(t *testing.T) {
	env := startTestEnv(t, nil)
	t.Cleanup(env.stop)
	conn := dialRetry(t, env.addr)
	t.Cleanup(func() { _ = conn.Close() })
	send(t, conn, netproto.MsgVideoStreamControl, netproto.VideoStreamControl{Action: "diagnostics", PublisherID: "other", Slot: "screen", Generation: 1, Session: 1})
	if got := readError(t, conn); got.Code != errCodeNotAuthenticated {
		t.Fatalf("unauthenticated stream diagnostics: %+v", got)
	}
	report := voiceTelemetryFixture()
	report.VideoSenders = []netproto.VideoSenderDiagnostics{{SSRC: 123, RID: "f", Slot: "screen", Generation: 1}}
	send(t, conn, netproto.MsgVoiceTelemetry, report)
	if got := readError(t, conn); got.Code != errCodeNotAuthenticated {
		t.Fatalf("unauthenticated sender telemetry: %+v", got)
	}
}

func TestStreamSenderReportRejectsStaleOrReplacedSources(t *testing.T) {
	now := time.Now()
	for _, scenario := range []string{"fresh", "old report", "previous source", "wrong generation", "wrong slot", "wrong rid", "wrong ssrc", "no source"} {
		t.Run(scenario, func(t *testing.T) {
			result := &netproto.VideoStreamDiagnostics{Slot: "screen", Generation: 7, Layers: []netproto.VideoStreamLayerDiagnostics{{SSRC: 123, RID: "f", StartedAt: now.Add(-time.Second).UnixMilli()}}}
			row := netproto.VideoSenderDiagnostics{SSRC: 123, RID: "f", Slot: "screen", Generation: 7}
			received := now
			switch scenario {
			case "old report":
				received = now.Add(-16 * time.Second)
			case "previous source":
				received = now.Add(-2 * time.Second)
			case "wrong generation":
				row.Generation++
			case "wrong slot":
				row.Slot = "cam"
			case "wrong rid":
				row.RID = "h"
			case "wrong ssrc":
				row.SSRC++
			case "no source":
				result.Layers = nil
			}
			attachVideoSenderReport(result, []netproto.VideoSenderDiagnostics{row}, received, now)
			if (result.SenderReport != nil) != (scenario == "fresh") {
				t.Fatalf("incorrect source correlation: %+v", result.SenderReport)
			}
		})
	}
}
