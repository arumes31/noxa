package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"noxa/internal/netproto"
)

func validVoiceDiagnosticReport() netproto.VoiceTelemetry {
	loss, sample, buffer := 2.5, 5000.0, 34.0
	return netproto.VoiceTelemetry{
		ChannelID: 7, ClientVersion: "untrusted-browser-version", ConnectionState: "connected",
		OutputState: "running", Volume: 125, VoiceLimiter: true,
		Tracks: []netproto.VoiceReceiverDiagnostics{{
			TrackID: "browser-track", PublisherID: "c-alice", Codec: "audio/opus",
			SampleMS: &sample, LossPercent: &loss, BufferMS: &buffer,
		}},
	}
}

// Capture only the control wire write; the embedded pipe supplies normal
// deadline and close behavior without a server or a background reader.
type voiceDiagnosticCaptureConn struct {
	net.Conn
	data bytes.Buffer
}

func (c *voiceDiagnosticCaptureConn) Write(p []byte) (int, error) { return c.data.Write(p) }

func voiceDiagnosticApp(t *testing.T, supported bool) (*App, *connManager, *voiceDiagnosticCaptureConn) {
	t.Helper()
	client, server := net.Pipe()
	conn := &voiceDiagnosticCaptureConn{Conn: client}
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	cm := newTestConnManager()
	cm.conn, cm.supportsVoiceTelemetry = conn, supported
	app := appWithCM(cm)
	app.tabs["a"] = &tabState{cm: cm, info: TabInfo{ID: "a"}}
	app.activeID = "a"
	return app, cm, conn
}

func TestReportVoiceDiagnosticsCapabilityAbsentIsHarmless(t *testing.T) {
	app, _, conn := voiceDiagnosticApp(t, false)
	if err := app.ReportVoiceDiagnosticsForTab("a", validVoiceDiagnosticReport()); err != nil {
		t.Fatalf("old server should not fail a background report: %v", err)
	}
	if conn.data.Len() != 0 {
		t.Fatal("sent voice telemetry without the negotiated capability")
	}
}

func TestReportVoiceDiagnosticsSendsNativeVersionAndPreservesMeasurements(t *testing.T) {
	app, _, conn := voiceDiagnosticApp(t, true)
	other, _, otherConn := voiceDiagnosticApp(t, true)
	app.tabs["b"] = &tabState{cm: other.cmLoad(), info: TabInfo{ID: "b"}}
	report := validVoiceDiagnosticReport()
	if err := app.ReportVoiceDiagnosticsForTab("a", report); err != nil {
		t.Fatal(err)
	}
	frame, err := netproto.ReadFrame(&conn.data)
	if err != nil {
		t.Fatal(err)
	}
	if netproto.MessageType(frame.Type) != netproto.MsgVoiceTelemetry || frame.Type != 188 {
		t.Fatalf("frame type = %d, want voice telemetry 188", frame.Type)
	}
	var got netproto.VoiceTelemetry
	if err := netproto.Decode(frame, &got); err != nil {
		t.Fatal(err)
	}
	want := report
	want.ClientVersion = app.ClientVersionShort()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("wire report = %+v, want %+v", got, want)
	}
	if report.ClientVersion != "untrusted-browser-version" {
		t.Fatal("changed the caller's report")
	}
	if otherConn.data.Len() != 0 || conn.data.Len() != 0 {
		t.Fatal("sent an additional report or wrote to another server tab")
	}
}

func TestReportVoiceDiagnosticsBoundsEscapedPayloadWithoutMutatingCaller(t *testing.T) {
	app, _, conn := voiceDiagnosticApp(t, true)
	report := validVoiceDiagnosticReport()
	report.Tracks = make([]netproto.VoiceReceiverDiagnostics, netproto.MaxVoiceDiagnosticTracks)
	for i := range report.Tracks {
		report.Tracks[i] = validVoiceDiagnosticReport().Tracks[0]
		// These strings meet the field-length limits but expand sixfold when
		// encoded as JSON. A track-count limit alone cannot bound wire bytes.
		report.Tracks[i].TrackID = fmt.Sprintf("%02d", i) + strings.Repeat("<", 158)
		report.Tracks[i].PublisherID = strings.Repeat(">", 80)
		report.Tracks[i].Codec = strings.Repeat("&", 40)
	}
	if !report.Valid() {
		t.Fatal("oversized fixture must be valid before wire-size bounding")
	}
	before, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) <= netproto.MaxVoiceTelemetryBytes {
		t.Fatalf("fixture is only %d bytes, want more than %d", len(before), netproto.MaxVoiceTelemetryBytes)
	}
	if err := app.ReportVoiceDiagnosticsForTab("a", report); err != nil {
		t.Fatalf("valid large diagnostic snapshot should retain a bounded prefix: %v", err)
	}
	frame, err := netproto.ReadFrame(&conn.data)
	if err != nil {
		t.Fatal(err)
	}
	if netproto.MessageType(frame.Type) != netproto.MsgVoiceTelemetry {
		t.Fatalf("frame type = %d, want voice telemetry", frame.Type)
	}
	if len(frame.Payload) > netproto.MaxVoiceTelemetryBytes {
		t.Fatalf("telemetry payload has %d bytes, maximum is %d", len(frame.Payload), netproto.MaxVoiceTelemetryBytes)
	}
	var got netproto.VoiceTelemetry
	if err := netproto.Decode(frame, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Tracks) == 0 || len(got.Tracks) >= len(report.Tracks) || !got.Truncated {
		t.Fatalf("expected a nonempty truncated report: tracks=%d truncated=%v", len(got.Tracks), got.Truncated)
	}
	if !reflect.DeepEqual(got.Tracks, report.Tracks[:len(got.Tracks)]) {
		t.Fatal("trimming changed retained measurements or removed tracks from the middle")
	}
	if got.ClientVersion != app.ClientVersionShort() || !got.Valid() {
		t.Fatal("bounded snapshot lost native version or protocol validity")
	}
	if conn.data.Len() != 0 {
		t.Fatal("large snapshot emitted more than one frame")
	}
	after, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("wire-size bounding mutated the caller's report")
	}
}

func TestReportVoiceDiagnosticsRejectsInvalidReportsBeforeWriting(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*netproto.VoiceTelemetry)
	}{
		{"no channel", func(r *netproto.VoiceTelemetry) { r.ChannelID = 0 }},
		{"volume", func(r *netproto.VoiceTelemetry) { r.Volume = 201 }},
		{"connection state", func(r *netproto.VoiceTelemetry) { r.ConnectionState = "invalid" }},
		{"output state", func(r *netproto.VoiceTelemetry) { r.OutputState = "invalid" }},
		{"NaN", func(r *netproto.VoiceTelemetry) { value := math.NaN(); r.RTTMS = &value }},
		{"infinity", func(r *netproto.VoiceTelemetry) { value := math.Inf(1); r.OutputLatencyMS = &value }},
		{"negative loss", func(r *netproto.VoiceTelemetry) { value := -1.0; r.Tracks[0].LossPercent = &value }},
		{"too many tracks", func(r *netproto.VoiceTelemetry) { r.Tracks = make([]netproto.VoiceReceiverDiagnostics, 65) }},
		{"duplicate track", func(r *netproto.VoiceTelemetry) { r.Tracks = append(r.Tracks, r.Tracks[0]) }},
		{"control characters", func(r *netproto.VoiceTelemetry) { r.Tracks[0].PublisherID = "c-alice\ninvalid" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, _, conn := voiceDiagnosticApp(t, true)
			report := validVoiceDiagnosticReport()
			tc.mutate(&report)
			if err := app.ReportVoiceDiagnosticsForTab("a", report); err == nil {
				t.Fatal("accepted malformed telemetry")
			}
			if conn.data.Len() != 0 {
				t.Fatal("invalid report reached the control connection")
			}
		})
	}
}

func TestReportVoiceDiagnosticsRejectsStaleTabsAndDisconnectedSessions(t *testing.T) {
	app, cm, conn := voiceDiagnosticApp(t, true)
	other, _, otherConn := voiceDiagnosticApp(t, true)
	app.tabs["b"] = &tabState{cm: other.cmLoad(), info: TabInfo{ID: "b"}}
	app.tabsMu.Lock()
	_, _, _, ok := app.activateLocked("b")
	app.tabsMu.Unlock()
	if !ok {
		t.Fatal("activate second tab")
	}
	for _, tabID := range []string{"", "a", "missing"} {
		if err := app.ReportVoiceDiagnosticsForTab(tabID, validVoiceDiagnosticReport()); err == nil {
			t.Fatalf("accepted stale tab %q", tabID)
		}
	}
	app.tabsMu.Lock()
	app.activateLocked("a")
	app.tabsMu.Unlock()
	cm.mu.Lock()
	cm.conn = nil
	cm.mu.Unlock()
	if err := app.ReportVoiceDiagnosticsForTab("a", validVoiceDiagnosticReport()); err == nil {
		t.Fatal("accepted diagnostics for a disconnected socket")
	}
	if conn.data.Len() != 0 || otherConn.data.Len() != 0 {
		t.Fatal("stale diagnostics were redirected to another connection")
	}
}

type voiceDiagnosticGatedConn struct {
	net.Conn
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *voiceDiagnosticGatedConn) Write(p []byte) (int, error) {
	c.once.Do(func() { close(c.entered) })
	<-c.release
	return c.Conn.Write(p)
}

func TestReportVoiceDiagnosticsPendingWriteCannotRetargetReplacement(t *testing.T) {
	app, cm, original := voiceDiagnosticApp(t, true)
	other, _, otherConn := voiceDiagnosticApp(t, true)
	_, _, replacementConn := voiceDiagnosticApp(t, true)
	client, server := net.Pipe()
	gate := &voiceDiagnosticGatedConn{Conn: client, entered: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	unblock := func() { release.Do(func() { close(gate.release) }) }
	t.Cleanup(func() { unblock(); _ = client.Close(); _ = server.Close() })
	cm.conn = gate
	app.tabs["b"] = &tabState{cm: other.cmLoad(), info: TabInfo{ID: "b"}}
	done := make(chan error, 1)
	go func() { done <- app.ReportVoiceDiagnosticsForTab("a", validVoiceDiagnosticReport()) }()
	select {
	case <-gate.entered:
	case <-time.After(time.Second):
		t.Fatal("report did not begin writing")
	}
	cm.mu.Lock()
	cm.conn = replacementConn
	cm.mu.Unlock()
	app.tabsMu.Lock()
	app.activateLocked("b")
	app.tabsMu.Unlock()
	unblock()
	frame := readFrame(t, server)
	if netproto.MessageType(frame.Type) != netproto.MsgVoiceTelemetry {
		t.Fatalf("original connection received %d", frame.Type)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("report did not finish")
	}
	if replacementConn.data.Len() != 0 || otherConn.data.Len() != 0 || original.data.Len() != 0 {
		t.Fatal("pending diagnostics leaked into a replacement socket or tab")
	}
}
