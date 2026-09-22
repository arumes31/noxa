package main

import (
	"crypto/tls"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"noxa/internal/netproto"
	"noxa/internal/tlscert"
)

func TestAuthorizationModelNegotiationBeforeSessionInstallation(t *testing.T) {
	for _, model := range []string{"", netproto.AuthorizationModelRolesV1, "roles-v2"} {
		t.Run(model, func(t *testing.T) {
			cert, _, err := tlscert.Ensure(t.TempDir(), "", "", nil)
			if err != nil {
				t.Fatal(err)
			}
			listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = listener.Close() }()
			var published, advertised atomic.Bool
			done := make(chan struct{})
			go func() {
				defer close(done)
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer func() { _ = conn.Close() }()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				frame, err := netproto.ReadFrame(conn)
				if err != nil {
					return
				}
				var request netproto.Authenticate
				if err := netproto.Decode(frame, &request); err != nil {
					t.Error(err)
					return
				}
				advertised.Store(slices.Contains(request.AuthorizationModels, netproto.AuthorizationModelRolesV1))
				response := netproto.AuthResponse{OK: true, AuthorizationModel: model, ClientID: "c1", UniqueID: "u1", MOTD: "fixture"}
				if err := netproto.WriteFrame(conn, mustEncode(netproto.MsgAuthResponse, response)); err != nil {
					return
				}
				for {
					frame, err := netproto.ReadFrame(conn)
					if err != nil {
						return
					}
					if frame.Type == uint16(netproto.MsgKeyPublish) {
						published.Store(true)
					}
				}
			}()
			cm, _ := newTestBackend(t)
			defer cm.disconnect()
			reason := cm.connect(listener.Addr().String(), "alice", "pw", "")
			unknown := model != netproto.AuthorizationModelRolesV1
			if unknown {
				if !strings.Contains(reason, "upgrade") || cm.connected() || cm.clientIDSnapshot() != "" || cm.motdSnapshot() != "" {
					t.Fatalf("unknown model installed session: %q", reason)
				}
			} else if reason != "" {
				t.Fatal(reason)
			}
			if cm.usesRoleAuthorization() != (model == netproto.AuthorizationModelRolesV1) {
				t.Fatal("chat acknowledgement mode does not match authenticated model")
			}
			cm.disconnect()
			if cm.usesRoleAuthorization() {
				t.Fatal("disconnected manager retained chat acknowledgement mode")
			}
			select {
			case <-done:
			case <-time.After(6 * time.Second):
				t.Fatal("server did not finish")
			}
			if !advertised.Load() {
				t.Fatal("client omitted roles-v1 support")
			}
			if unknown && published.Load() {
				t.Fatal("incompatible model accepted before key publication")
			}
		})
	}
}
