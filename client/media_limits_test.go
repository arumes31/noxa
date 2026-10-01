package main

import (
	"crypto/tls"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"noxa/internal/netproto"
	"noxa/internal/tlscert"
)

func TestMediaLimitsAuthenticationAndCleanup(t *testing.T) {
	bounded := &netproto.MediaLimits{VideoMaxBitrate: 1000000, VideoMaxWidth: 640, VideoMaxHeight: 360}
	for _, tc := range []struct {
		name      string
		limits    *netproto.MediaLimits
		invalid   bool
		revision  uint64
		rawLimits string
	}{
		{"bounded", bounded, false, 0, ""}, {"omitted", nil, false, 0, ""}, {"unlimited", &netproto.MediaLimits{}, false, 0, ""},
		{"revisioned", bounded, false, 9, ""}, {"large revision", bounded, false, 18446744073709551615, ""},
		{"missing revisioned limits", nil, true, 1, ""},
		{"partial dimensions", &netproto.MediaLimits{VideoMaxWidth: 640}, true, 0, ""},
		{"negative bitrate", &netproto.MediaLimits{VideoMaxBitrate: -1}, true, 0, ""},
		{"excessive bitrate", &netproto.MediaLimits{VideoMaxBitrate: 100000001}, true, 0, ""},
		{"excessive dimensions", &netproto.MediaLimits{VideoMaxWidth: 16384, VideoMaxHeight: 1}, true, 0, ""},
		{"empty revisioned limits", bounded, true, 9, `{}`},
		{"null revisioned limits", bounded, true, 9, `null`},
		{"missing bitrate", bounded, true, 9, `{"video_max_width":0,"video_max_height":0}`},
		{"missing width", bounded, true, 9, `{"video_max_bitrate":0,"video_max_height":0}`},
		{"missing height", bounded, true, 9, `{"video_max_bitrate":0,"video_max_width":0}`},
		{"null bitrate", bounded, true, 9, `{"video_max_bitrate":null,"video_max_width":0,"video_max_height":0}`},
		{"legacy partial object", &netproto.MediaLimits{}, false, 0, `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cert, _, err := tlscert.Ensure(t.TempDir(), "", "", nil)
			if err != nil {
				t.Fatal(err)
			}
			listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = listener.Close() }()
			var published atomic.Bool
			done := make(chan struct{})
			go func() {
				defer close(done)
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer func() { _ = conn.Close() }()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				if _, err := netproto.ReadFrame(conn); err != nil {
					return
				}
				response := netproto.AuthResponse{OK: true, AuthorizationModel: netproto.AuthorizationModelRolesV1, ClientID: "c1", UniqueID: "u1", MediaLimits: tc.limits, MediaLimitsRevision: tc.revision}
				frame := mustEncode(netproto.MsgAuthResponse, response)
				if tc.rawLimits != "" {
					var fields map[string]json.RawMessage
					if err := json.Unmarshal(frame.Payload, &fields); err != nil {
						t.Error(err)
						return
					}
					fields["media_limits"] = json.RawMessage(tc.rawLimits)
					frame.Payload, err = json.Marshal(fields)
					if err != nil {
						t.Error(err)
						return
					}
				}
				if err := netproto.WriteFrame(conn, frame); err != nil {
					return
				}
				for {
					frame, err := netproto.ReadFrame(conn)
					if err != nil {
						return
					}
					if netproto.MessageType(frame.Type) == netproto.MsgKeyPublish {
						published.Store(true)
					}
				}
			}()
			cm, _ := newTestBackend(t)
			defer cm.disconnect()
			reason := cm.connect(listener.Addr().String(), "alice", "pw", "")
			if tc.invalid {
				if reason != "invalid server media limits" {
					t.Fatalf("connect = %q", reason)
				}
			} else {
				if reason != "" {
					t.Fatal(reason)
				}
				want := netproto.MediaLimits{}
				if tc.limits != nil {
					want = *tc.limits
				}
				app := appWithCM(cm)
				if got := app.GetMediaLimits(); got != want {
					t.Fatalf("limits = %+v, want %+v", got, want)
				}
				if cm.mediaLimitsRevision != tc.revision {
					t.Fatal("authentication lost the media revision baseline")
				}
				if tc.revision > 0 {
					cm.mu.Lock()
					conn, epoch := cm.conn, cm.connEpoch
					cm.mu.Unlock()
					cm.dispatchFrom(conn, epoch, mediaUpdateFrame(tc.revision, netproto.MediaLimits{}))
					if cm.mediaLimitsSnapshot() != want {
						t.Fatal("duplicate event replaced authenticated limits")
					}
				}
				other, _ := newTestBackend(t)
				app.cm.Store(other)
				if got := app.GetMediaLimits(); got != (netproto.MediaLimits{}) {
					t.Fatalf("limits leaked between tabs: %+v", got)
				}
			}
			cm.disconnect()
			if got := cm.mediaLimitsSnapshot(); got != (netproto.MediaLimits{}) || cm.mediaLimitsRevision != 0 {
				t.Fatalf("disconnect retained limits: %+v", got)
			}
			select {
			case <-done:
			case <-time.After(6 * time.Second):
				t.Fatal("server did not finish")
			}
			if tc.invalid && published.Load() {
				t.Fatal("invalid limits accepted before key publication")
			}
		})
	}
	if got := (&App{}).GetMediaLimits(); got != (netproto.MediaLimits{}) {
		t.Fatalf("offline limits: %+v", got)
	}
}
