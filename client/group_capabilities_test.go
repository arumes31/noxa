package main

import (
	"crypto/tls"
	"testing"
	"time"

	"noxa/internal/netproto"
	"noxa/internal/tlscert"
)

func TestGroupAssignNegotiatesAcknowledgement(t *testing.T) {
	for _, supported := range []bool{false, true} {
		name := "legacy"
		if supported {
			name = "acknowledged"
		}
		t.Run(name, func(t *testing.T) {
			cert, _, err := tlscert.Ensure(t.TempDir(), "", "", nil)
			if err != nil {
				t.Fatal(err)
			}
			listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = listener.Close() }()
			assignments := make(chan netproto.GroupAssign, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer func() { _ = conn.Close() }()
				if _, err := netproto.ReadFrame(conn); err != nil {
					return
				}
				caps := []string{"future_unknown_capability"}
				if supported {
					caps = append(caps, "group_assign_ack")
				}
				if err := netproto.WriteFrame(conn, mustEncode(netproto.MsgAuthResponse, map[string]any{"ok": true, "client_id": "c1", "unique_id": "u1", "capabilities": caps})); err != nil {
					return
				}
				for {
					frame, err := netproto.ReadFrame(conn)
					if err != nil {
						return
					}
					switch netproto.MessageType(frame.Type) {
					case netproto.MsgGroupAssign:
						var msg netproto.GroupAssign
						if err := netproto.Decode(frame, &msg); err != nil {
							return
						}
						assignments <- msg
						if supported {
							_ = netproto.WriteFrame(conn, mustEncode(netproto.MsgGroupAssign, msg))
						}
					case netproto.MsgGroupList:
						_ = netproto.WriteFrame(conn, mustEncode(netproto.MsgGroupListResponse, netproto.GroupListResponse{}))
					}
				}
			}()
			cm, _ := newTestBackend(t)
			defer cm.disconnect()
			if reason := cm.connect(listener.Addr().String(), "alice", "pw", ""); reason != "" {
				t.Fatal(reason)
			}
			app := appWithCM(cm)
			if result := app.GroupAssign("server", 2, "target", 0, 0); result != "" {
				t.Fatalf("assignment failed: %s", result)
			}
			select {
			case msg := <-assignments:
				if msg.AckRequested != supported {
					t.Fatalf("AckRequested = %v, want %v", msg.AckRequested, supported)
				}
			case <-time.After(time.Second):
				t.Fatal("server received no assignment")
			}
			if _, err := app.GroupList("server"); err != nil {
				t.Fatalf("transport closed after successful assignment: %v", err)
			}
		})
	}
}
