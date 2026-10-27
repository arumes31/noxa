package server

import (
	"context"
	"errors"
	"go.uber.org/zap"
	"net"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"testing"
	"time"
)

func TestOlmAdmissionDefaultOff(t *testing.T) {
	server, peer := net.Pipe()
	defer server.Close()
	defer peer.Close()
	s := &TCPServer{logger: zap.NewNop()}
	c := &Client{Conn: server}
	done := make(chan error, 1)
	go func() { done <- s.handleOlmAdmission(t.Context(), c, &netproto.Frame{Payload: []byte(`{}`)}) }()
	f, e := netproto.ReadFrame(peer)
	if e != nil {
		t.Fatal(e)
	}
	if netproto.MessageType(f.Type) != netproto.MsgError {
		t.Fatal("inactive admission was reachable")
	}
	if e = <-done; e != nil {
		t.Fatal(e)
	}
}

func TestOlmAdmissionAuthorizationReplyBoundAndCorrelated(t *testing.T) {
	for _, denied := range []bool{false, true} {
		name := map[bool]string{false: "unavailable", true: "denied"}[denied]
		for _, queued := range []bool{false, true} {
			t.Run(name+map[bool]string{false: "/reply", true: "/queued"}[queued], func(t *testing.T) {
				server, peer := net.Pipe()
				defer server.Close()
				defer peer.Close()
				s := &TCPServer{logger: zap.NewNop(), olmAdmissionEnabled: true}
				c := &Client{Conn: server}
				want := "unavailable"
				if denied {
					authority, e := authorization.NewAuthority(t.Context(), serverRoleFixture(), func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
					if e != nil {
						t.Fatal(e)
					}
					s.deps = &Deps{Authority: authority}
					c.revokeSession()
					want = "authority-required"
				}
				ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
				defer cancel()
				if queued {
					c.wmu.Lock()
				}
				done := make(chan error, 1)
				go func() {
					done <- s.handleOlmAdmission(ctx, c, &netproto.Frame{Payload: []byte(`{"request_id":"auth-review","action":"snapshot","account_epoch":1,"scope":"direct","peer":"bob"}`)})
				}()
				if queued {
					select {
					case e := <-done:
						c.wmu.Unlock()
						if !errors.Is(e, context.DeadlineExceeded) {
							t.Fatalf("deadline reply: %v", e)
						}
					case <-time.After(250 * time.Millisecond):
						c.wmu.Unlock()
						go func() { _, _ = netproto.ReadFrame(peer) }()
						<-done
						t.Fatal("authorization reply bypassed admission deadline")
					}
					return
				}
				if e := peer.SetReadDeadline(time.Now().Add(time.Second)); e != nil {
					t.Fatal(e)
				}
				f, e := netproto.ReadFrame(peer)
				if e != nil {
					t.Fatal(e)
				}
				var reply netproto.OlmAdmissionResult
				if netproto.MessageType(f.Type) != netproto.MsgOlmAdmissionResult {
					t.Fatalf("uncorrelated frame %v", f.Type)
				}
				if e = netproto.Decode(f, &reply); e != nil {
					t.Fatal(e)
				}
				if reply.RequestID != "auth-review" || reply.Action != "snapshot" || reply.Error != want {
					t.Fatalf("wrong authorization reply: %+v", reply)
				}
				if e := <-done; e != nil {
					t.Fatal(e)
				}
			})
		}
	}
}
func TestOlmAdmissionWriteDeadlineLegacyAndQueued(t *testing.T) {
	for _, queued := range []bool{false, true} {
		t.Run(map[bool]string{false: "socket", true: "mutex"}[queued], func(t *testing.T) {
			server, peer := net.Pipe()
			defer server.Close()
			defer peer.Close()
			s := &TCPServer{logger: zap.NewNop(), olmAdmissionEnabled: true}
			c := &Client{Conn: server}
			if queued {
				c.wmu.Lock()
				defer c.wmu.Unlock()
			}
			ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
			defer cancel()
			start := time.Now()
			e := s.handleOlmAdmission(ctx, c, &netproto.Frame{Payload: []byte(`{}`)})
			if e == nil || time.Since(start) > time.Second {
				t.Fatalf("unbounded write %v", e)
			}
		})
	}
}
