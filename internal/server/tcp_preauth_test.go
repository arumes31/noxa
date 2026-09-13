package server

import (
	"context"
	"crypto/tls"
	"net"
	"testing"
	"testing/synctest"
	"time"

	"go.uber.org/zap"

	"voicx/internal/auth"
	"voicx/internal/config"
	"voicx/internal/netproto"
)

// Virtual time and net.Pipe exercise connection deadlines without wall-clock
// sleeps or a listener, including release of the occupied admission slot.
func TestTCPPreauthIdleDeadline(t *testing.T) {
	for _, tc := range []struct {
		name              string
		inactivitySeconds int
		deadline          time.Duration
	}{
		{name: "inactivity disabled", deadline: 10 * time.Second},
		{name: "long inactivity timeout", inactivitySeconds: 120, deadline: 10 * time.Second},
		{name: "short inactivity timeout", inactivitySeconds: 2, deadline: 2 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, peer, done := startPreauthPipe(t, tc.inactivitySeconds, false)
				defer finishPreauthPipe(peer, done)
				time.Sleep(tc.deadline - time.Nanosecond)
				synctest.Wait()
				if s.clientCount() != 1 {
					t.Fatal("connection closed before the authentication deadline")
				}
				time.Sleep(time.Nanosecond)
				assertPreauthExpired(t, s, done)
			})
		})
	}
}

func TestTCPPreauthPartialTLSHandshakeDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, peer, done := startPreauthPipe(t, 120, true)
		defer finishPreauthPipe(peer, done)
		// A handshake record header with one payload byte leaves the server
		// waiting for the remaining ClientHello bytes.
		if _, err := peer.Write([]byte{22, 3, 3, 0, 32, 1}); err != nil {
			t.Fatalf("write partial TLS record: %v", err)
		}
		synctest.Wait()
		if s.clientCount() != 1 {
			t.Fatal("partial TLS handshake did not remain pending")
		}
		time.Sleep(10 * time.Second)
		assertPreauthExpired(t, s, done)
	})
}

func TestTCPPreauthPingCannotExtendDeadline(t *testing.T) {
	for _, tc := range []struct {
		name              string
		inactivitySeconds int
		interval          time.Duration
	}{
		{name: "default authentication deadline", inactivitySeconds: 120, interval: 3 * time.Second},
		{name: "short inactivity timeout", inactivitySeconds: 2, interval: time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, peer, done := startPreauthPipe(t, tc.inactivitySeconds, false)
				defer finishPreauthPipe(peer, done)
				deadline := min(10*time.Second, time.Duration(tc.inactivitySeconds)*time.Second)
				for elapsed := time.Duration(0); elapsed < deadline; {
					ping, err := netproto.Encode(netproto.MsgPing, netproto.Ping{})
					if err != nil {
						t.Fatal(err)
					}
					if err := netproto.WriteFrame(peer, ping); err != nil {
						t.Fatalf("ping before authentication deadline: %v", err)
					}
					pong, err := netproto.ReadFrame(peer)
					if err != nil {
						t.Fatalf("read pong: %v", err)
					}
					if pong.Type != uint16(netproto.MsgPong) {
						t.Fatalf("reply type = %d, want Pong", pong.Type)
					}
					step := min(tc.interval, deadline-elapsed)
					time.Sleep(step)
					elapsed += step
				}
				assertPreauthExpired(t, s, done)
			})
		})
	}
}

func TestTCPPreauthBlockedReplyDeadline(t *testing.T) {
	for _, tc := range []struct {
		name        string
		messageType netproto.MessageType
	}{
		{name: "pong", messageType: netproto.MsgPing},
		{name: "authentication required error", messageType: netproto.MsgPong},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, peer, done := startPreauthPipe(t, 120, false)
				defer finishPreauthPipe(peer, done)
				frame := &netproto.Frame{Type: uint16(tc.messageType)}
				if err := netproto.WriteFrame(peer, frame); err != nil {
					t.Fatalf("send request: %v", err)
				}
				// Deliberately never read the reply. The server must bound both
				// the read and write sides of an unauthenticated connection.
				synctest.Wait()
				if s.clientCount() != 1 {
					t.Fatal("connection closed before its reply could block")
				}
				time.Sleep(10 * time.Second)
				assertPreauthExpired(t, s, done)
			})
		})
	}
}

func TestTCPPreauthSuccessfulAuthenticationRestoresInactivityPolicy(t *testing.T) {
	for _, tc := range []struct {
		name              string
		inactivitySeconds int
	}{
		{name: "inactivity disabled"},
		{name: "ordinary inactivity enabled", inactivitySeconds: 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, peer, done := startPreauthPipe(t, tc.inactivitySeconds, false)
				defer finishPreauthPipe(peer, done)
				time.Sleep(9 * time.Second)
				frame, err := netproto.Encode(netproto.MsgAuthenticate, netproto.Authenticate{Anonymous: true, Nickname: "guest"})
				if err != nil {
					t.Fatal(err)
				}
				if err := netproto.WriteFrame(peer, frame); err != nil {
					t.Fatalf("authenticate before deadline: %v", err)
				}
				responseFrame, err := netproto.ReadFrame(peer)
				if err != nil {
					t.Fatalf("read authentication response: %v", err)
				}
				var response netproto.AuthResponse
				if responseFrame.Type != uint16(netproto.MsgAuthResponse) {
					t.Fatalf("response type = %d, want AuthResponse", responseFrame.Type)
				}
				if err := netproto.Decode(responseFrame, &response); err != nil || !response.OK {
					t.Fatalf("authentication response = %+v, decode error = %v", response, err)
				}
				// Drain snapshots and server keepalives so writes cannot hide an
				// incorrectly retained authentication deadline.
				go func() {
					for {
						if _, err := netproto.ReadFrame(peer); err != nil {
							return
						}
					}
				}()
				synctest.Wait()
				time.Sleep(2 * time.Second)
				synctest.Wait()
				if s.clientCount() != 1 {
					t.Fatal("successfully authenticated connection closed at preauthentication deadline")
				}
				// The ordinary 15-second keepalive sweep sees 21 seconds of
				// inactivity at t=30. A zero inactivity setting keeps it open.
				time.Sleep(19 * time.Second)
				synctest.Wait()
				want := 0
				if tc.inactivitySeconds == 0 {
					want = 1
				}
				if got := s.clientCount(); got != want {
					t.Errorf("registered connections after ordinary inactivity sweep = %d, want %d", got, want)
				}
			})
		})
	}
}

func TestTCPPreauthBackendDeadline(t *testing.T) {
	for _, tc := range []struct {
		name              string
		admission         bool
		inactivitySeconds int
		deadline          time.Duration
	}{
		{name: "authentication", deadline: 10 * time.Second},
		{name: "authentication short timeout", inactivitySeconds: 2, deadline: 2 * time.Second},
		{name: "admission settings", admission: true, deadline: 10 * time.Second},
		{name: "admission settings short timeout", admission: true, inactivitySeconds: 2, deadline: 2 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				entered := make(chan struct{})
				deps := &Deps{Auth: &preauthWaitingAuth{AuthBackend: &fakeAuth{}, entered: entered}}
				if tc.admission {
					deps.Chat = &preauthWaitingSettings{entered: entered}
				}
				s := New(&config.Config{ClientTimeoutSeconds: tc.inactivitySeconds}, zap.NewNop(), deps)
				server, peer := net.Pipe()
				ctx, cancel := context.WithCancel(context.Background())
				done := make(chan struct{})
				go func() {
					defer close(done)
					s.handleConn(ctx, server)
				}()
				defer func() {
					// Cancel the lifetime context only during cleanup, allowing a
					// missing authentication context deadline to fail cleanly.
					cancel()
					finishPreauthPipe(peer, done)
				}()
				if !tc.admission {
					frame, err := netproto.Encode(netproto.MsgAuthenticate, netproto.Authenticate{Username: "user", Password: "pw"})
					if err != nil {
						t.Fatal(err)
					}
					if err := netproto.WriteFrame(peer, frame); err != nil {
						t.Fatalf("send authentication: %v", err)
					}
				}
				<-entered
				synctest.Wait()
				if s.clientCount() != 1 {
					t.Fatal("connection did not retain its slot while backend work was pending")
				}
				time.Sleep(tc.deadline)
				assertPreauthExpired(t, s, done)
			})
		})
	}
}

type preauthWaitingAuth struct {
	AuthBackend
	entered chan struct{}
}

func (a *preauthWaitingAuth) AuthenticateIdentifier(ctx context.Context, _, _ string) (*auth.User, error) {
	close(a.entered)
	<-ctx.Done()
	return nil, ctx.Err()
}

type preauthWaitingSettings struct {
	ChatStore
	entered chan struct{}
}

func (s *preauthWaitingSettings) GetServerSetting(ctx context.Context, _ string) (string, uint32, error) {
	close(s.entered)
	<-ctx.Done()
	return "", 0, ctx.Err()
}

func startPreauthPipe(t *testing.T, inactivitySeconds int, useTLS bool) (*TCPServer, net.Conn, <-chan struct{}) {
	t.Helper()
	s := New(&config.Config{ClientTimeoutSeconds: inactivitySeconds}, zap.NewNop(), &Deps{Auth: &fakeAuth{}})
	server, peer := net.Pipe()
	conn := server
	if useTLS {
		conn = tls.Server(server, &tls.Config{MinVersion: tls.VersionTLS12})
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.handleConn(context.Background(), conn)
	}()
	synctest.Wait()
	if s.clientCount() != 1 {
		_ = peer.Close()
		<-done
		t.Fatal("connection did not acquire an admission slot")
	}
	return s, peer, done
}

func finishPreauthPipe(peer net.Conn, done <-chan struct{}) {
	_ = peer.Close()
	<-done
}

func assertPreauthExpired(t *testing.T, s *TCPServer, done <-chan struct{}) {
	t.Helper()
	synctest.Wait()
	select {
	case <-done:
	default:
		t.Error("unauthenticated connection survived its absolute deadline")
	}
	if got := s.clientCount(); got != 0 {
		t.Errorf("registered connections after authentication deadline = %d, want 0", got)
	}
}
