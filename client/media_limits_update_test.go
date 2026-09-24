package main

import (
	"crypto/tls"
	"errors"
	"net"
	"testing"
	"time"

	"noxa/internal/netproto"
	"noxa/internal/tlscert"
)

func mediaUpdateFrame(revision uint64, limits netproto.MediaLimits) *netproto.Frame {
	return mustEncode(netproto.MsgMediaLimitsChanged, netproto.MediaLimitsChanged{Revision: revision, MediaLimits: limits})
}

func TestMediaLimitsChangedRetainsNewestConnectionState(t *testing.T) {
	cm := newTestConnManager()
	sink := &recordingSink{}
	cm.sink = sink
	client, server := net.Pipe()
	defer func() { _ = server.Close(); cm.disconnect() }()
	cm.conn, cm.connEpoch = client, 5
	first := netproto.MediaLimits{VideoMaxBitrate: 1_000_000, VideoMaxWidth: 640, VideoMaxHeight: 360}
	second := netproto.MediaLimits{VideoMaxBitrate: 2_000_000, VideoMaxWidth: 1280, VideoMaxHeight: 720}
	cm.dispatchFrom(client, 5, mediaUpdateFrame(2, first))
	if cm.mediaLimitsSnapshot() != first || sink.count("media_limits_changed") != 1 {
		t.Fatal("valid update not retained and notified")
	}
	cm.dispatchFrom(client, 5, mediaUpdateFrame(1, second))
	cm.dispatchFrom(client, 5, mediaUpdateFrame(2, second))
	if cm.mediaLimitsSnapshot() != first || sink.count("media_limits_changed") != 1 {
		t.Fatal("older or duplicate revision replaced state")
	}
	cm.dispatchFrom(client, 5, mediaUpdateFrame(3, first))
	if cm.mediaLimitsRevision != 3 || sink.count("media_limits_changed") != 1 {
		t.Fatal("same values failed to advance revision or unnecessarily notified")
	}
	cm.dispatchFrom(client, 5, mediaUpdateFrame(4, second))
	if cm.mediaLimitsSnapshot() != second || sink.count("media_limits_changed") != 2 {
		t.Fatal("latest update not retained")
	}
	cm.dispatchFrom(client, 5, mediaUpdateFrame(5, netproto.MediaLimits{}))
	if cm.mediaLimitsSnapshot() != (netproto.MediaLimits{}) || sink.count("media_limits_changed") != 3 {
		t.Fatal("explicit unlimited update not applied")
	}
	cm.dispatchFrom(client, 5, mediaUpdateFrame(^uint64(0), first))
	cm.dispatchFrom(client, 5, mediaUpdateFrame(^uint64(0)-1, second))
	if cm.mediaLimitsRevision != ^uint64(0) || cm.mediaLimitsSnapshot() != first {
		t.Fatal("full-width revision lost precision")
	}
	cm.disconnect()
	if cm.mediaLimitsRevision != 0 || cm.mediaLimitsSnapshot() != (netproto.MediaLimits{}) {
		t.Fatal("disconnect retained old limits or revision")
	}
}

func TestMediaLimitsChangedRejectsMalformedUpdates(t *testing.T) {
	for _, tc := range []struct{ name, payload string }{
		{"missing fields", `{"revision":"1"}`},
		{"missing revision", `{"video_max_bitrate":0,"video_max_width":0,"video_max_height":0}`},
		{"zero revision", `{"revision":"0","video_max_bitrate":0,"video_max_width":0,"video_max_height":0}`},
		{"numeric revision", `{"revision":1,"video_max_bitrate":0,"video_max_width":0,"video_max_height":0}`},
		{"overflow revision", `{"revision":"18446744073709551616","video_max_bitrate":0,"video_max_width":0,"video_max_height":0}`},
		{"null limit", `{"revision":"1","video_max_bitrate":null,"video_max_width":0,"video_max_height":0}`},
		{"partial bounds", `{"revision":"1","video_max_bitrate":0,"video_max_width":640,"video_max_height":0}`},
		{"negative bitrate", `{"revision":"1","video_max_bitrate":-1,"video_max_width":0,"video_max_height":0}`},
		{"excess bitrate", `{"revision":"1","video_max_bitrate":100000001,"video_max_width":0,"video_max_height":0}`},
		{"excess bounds", `{"revision":"1","video_max_bitrate":0,"video_max_width":16384,"video_max_height":720}`},
		{"array", `[]`}, {"null", `null`}, {"invalid json", `{`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cm := newTestConnManager()
			sink := &recordingSink{}
			cm.sink = sink
			client, server := net.Pipe()
			defer func() { _ = server.Close(); cm.disconnect() }()
			cm.conn, cm.connEpoch = client, 1
			waiter := make(chan requestResult, 1)
			cm.pending[netproto.MsgAvatarData] = pendingRequest{request: netproto.MsgAvatarGet, result: waiter}
			cm.dispatchFrom(client, 1, &netproto.Frame{Type: uint16(netproto.MsgMediaLimitsChanged), Payload: []byte(tc.payload)})
			if cm.connected() || sink.count("disconnected") != 1 || sink.count("media_limits_changed") != 0 {
				t.Fatal("malformed limits did not terminate their source transport")
			}
			select {
			case reply := <-waiter:
				if !errors.Is(reply.err, net.ErrClosed) {
					t.Fatalf("waiter result = %v", reply.err)
				}
			default:
				t.Fatal("malformed update left pending requests waiting")
			}
		})
	}
}

func TestMediaLimitsChangedSourceRecheckedBeforeEffects(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		t.Run(map[bool]string{false: "valid", true: "malformed"}[malformed], func(t *testing.T) {
			cm := newTestConnManager()
			sink := &recordingSink{}
			cm.sink = sink
			oldClient, oldServer := net.Pipe()
			newClient, newServer := net.Pipe()
			defer func() { _ = oldClient.Close(); _ = oldServer.Close(); _ = newServer.Close(); cm.disconnect() }()
			cm.conn, cm.connEpoch = oldClient, 1
			parsed, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			cm.beforeDispatchLock = func() { close(parsed); <-release }
			frame := mediaUpdateFrame(100, netproto.MediaLimits{VideoMaxBitrate: 1})
			if malformed {
				frame.Payload = []byte(`{}`)
			}
			go func() { defer close(done); cm.dispatchFrom(oldClient, 1, frame) }()
			<-parsed
			cm.mu.Lock()
			cm.conn, cm.connEpoch = newClient, 2
			cm.mediaLimits = netproto.MediaLimits{VideoMaxBitrate: 2_000_000}
			cm.mediaLimitsRevision = 7
			cm.mu.Unlock()
			close(release)
			<-done
			if !cm.connected() || cm.mediaLimitsSnapshot().VideoMaxBitrate != 2_000_000 || cm.mediaLimitsRevision != 7 || sink.count("media_limits_changed") != 0 || sink.count("disconnected") != 0 {
				t.Fatal("obsolete update affected replacement connection")
			}
		})
	}
}

func TestMediaLimitsChangedOverAuthenticatedTLS(t *testing.T) {
	cert, _, err := tlscert.Ensure(t.TempDir(), "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	initial := netproto.MediaLimits{VideoMaxBitrate: 2_000_000}
	updated := netproto.MediaLimits{VideoMaxBitrate: 1_000_000, VideoMaxWidth: 640, VideoMaxHeight: 360}
	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := netproto.ReadFrame(conn); err != nil {
			done <- err
			return
		}
		response := netproto.AuthResponse{OK: true, AuthorizationModel: netproto.AuthorizationModelRolesV1, ClientID: "self", UniqueID: "user", MediaLimits: &initial, MediaLimitsRevision: 13}
		if err := netproto.WriteFrame(conn, mustEncode(netproto.MsgAuthResponse, response)); err != nil {
			done <- err
			return
		}
		if _, err := netproto.ReadFrame(conn); err != nil {
			done <- err
			return
		}
		for _, frame := range []*netproto.Frame{
			mediaUpdateFrame(14, updated), mediaUpdateFrame(13, initial),
			mustEncode(netproto.MsgAvatarData, netproto.AvatarData{}),
		} {
			if err := netproto.WriteFrame(conn, frame); err != nil {
				done <- err
				return
			}
		}
		// Keep the transport open until the client explicitly disconnects.
		_, _ = netproto.ReadFrame(conn)
		done <- nil
	}()
	cm, rec := newTestBackend(t)
	defer cm.disconnect()
	if reason := cm.connect(listener.Addr().String(), "alice", "pw", ""); reason != "" {
		t.Fatal(reason)
	}
	rec.waitFor(t, "avatar", nil, 5*time.Second, "frames after media updates")
	if cm.mediaLimitsSnapshot() != updated {
		t.Fatal("ordered TLS updates did not preserve the newer revision")
	}
	rec.mu.Lock()
	var updates int
	for _, event := range rec.events {
		if event.name == "media_limits_changed" {
			updates++
			if event.payload != "" {
				t.Error("invalidation included a potentially stale snapshot")
			}
		}
	}
	rec.mu.Unlock()
	if updates != 1 {
		t.Fatalf("update notifications = %d, want 1", updates)
	}
	cm.disconnect()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("TLS fixture did not finish")
	}
}

func TestMediaLimitsChangedBackgroundTabRetainsCacheWithoutUIEvent(t *testing.T) {
	first, second := newTestConnManager(), newTestConnManager()
	app := &App{activeID: "a", tabs: map[string]*tabState{"a": {cm: first}, "b": {cm: second}}}
	app.cmStore(first)
	first.sink = tabSink{app: app, tabID: "a"}
	second.sink = tabSink{app: app, tabID: "b"}
	var updates int
	app.eventEmit = func(name string, _ any) {
		if name != "media_limits_changed" {
			return
		}
		updates++
		// The event must be emitted after unlocking connection state, allowing
		// immediate reentrant reads of the authoritative current snapshot.
		if got, err := app.GetMediaLimitsForTab("a"); err != nil || got.VideoMaxBitrate != 1_000_000 {
			t.Errorf("event preceded native snapshot: %+v, %v", got, err)
		}
	}
	first.dispatch(mediaUpdateFrame(1, netproto.MediaLimits{VideoMaxBitrate: 1_000_000}))
	second.dispatch(mediaUpdateFrame(2, netproto.MediaLimits{VideoMaxBitrate: 2_000_000}))
	if updates != 1 || len(app.tabs["b"].journal) != 0 || second.mediaLimitsSnapshot().VideoMaxBitrate != 2_000_000 {
		t.Fatal("background update was lost, replayed as stale state or affected active UI")
	}
	app.tabsMu.Lock()
	_, _, _, activated := app.activateLocked("b")
	app.tabsMu.Unlock()
	if !activated {
		t.Fatal("tab activation failed")
	}
	if got, err := app.GetMediaLimitsForTab("b"); err != nil || got.VideoMaxBitrate != 2_000_000 {
		t.Fatalf("activated tab limits = %+v, %v", got, err)
	}
}
