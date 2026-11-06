package server

import (
	"context"
	"encoding/binary"
	"net"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"go.uber.org/zap"

	"noxa/internal/config"
	"noxa/internal/metrics"
	"noxa/internal/netproto"
)

type disconnectRecorder struct {
	metrics.Noop
	mu      sync.Mutex
	reasons []string
}

func (r *disconnectRecorder) IncControlDisconnect(transport, reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reasons = append(r.reasons, transport+"/"+reason)
}

func (r *disconnectRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.reasons...)
}

// runDisconnectCase serves one piped connection, lets act drive the peer,
// and returns the recorded close reasons once the handler has exited.
func runDisconnectCase(t *testing.T, act func(s *TCPServer, peer net.Conn)) []string {
	t.Helper()
	recorder := &disconnectRecorder{}
	s := New(&config.Config{}, zap.NewNop(), &Deps{Auth: &fakeAuth{}, Metrics: recorder})
	server, peer := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.handleConn(context.Background(), server)
	}()
	synctest.Wait()
	act(s, peer)
	synctest.Wait()
	_ = peer.Close()
	<-done
	return recorder.snapshot()
}

func writeRawFrameLength(t *testing.T, peer net.Conn, length uint32) {
	t.Helper()
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], length)
	go func() { _, _ = peer.Write(header[:]) }()
}

func TestControlDisconnectMetricReasons(t *testing.T) {
	cases := []struct {
		name string
		act  func(t *testing.T, s *TCPServer, peer net.Conn)
		want string
	}{
		{
			name: "peer closes",
			act:  func(_ *testing.T, _ *TCPServer, peer net.Conn) { _ = peer.Close() },
			want: "tcp/eof",
		},
		{
			name: "authentication deadline",
			act:  func(_ *testing.T, _ *TCPServer, _ net.Conn) { time.Sleep(10 * time.Second) },
			want: "tcp/auth_timeout",
		},
		{
			name: "oversized frame",
			act: func(t *testing.T, _ *TCPServer, peer net.Conn) {
				writeRawFrameLength(t, peer, preauthFrameLimit+1)
			},
			want: "tcp/frame_too_large",
		},
		{
			name: "invalid frame length",
			act:  func(t *testing.T, _ *TCPServer, peer net.Conn) { writeRawFrameLength(t, peer, 1) },
			want: "tcp/protocol_error",
		},
		{
			name: "server-side cause wins over the resulting read error",
			act: func(_ *testing.T, s *TCPServer, _ net.Conn) {
				s.mu.RLock()
				clients := make([]*Client, 0, len(s.clients))
				for _, client := range s.clients {
					clients = append(clients, client)
				}
				s.mu.RUnlock()
				for _, client := range clients {
					client.markCloseCause(netproto.ControlCloseKicked)
					client.markCloseCause(netproto.ControlCloseServerInactivity) // first cause wins
					_ = client.Conn.Close()
				}
			},
			want: "tcp/kicked",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				got := runDisconnectCase(t, func(s *TCPServer, peer net.Conn) { tc.act(t, s, peer) })
				if len(got) != 1 || got[0] != tc.want {
					t.Fatalf("recorded disconnects = %v, want [%s]", got, tc.want)
				}
			})
		})
	}
}

func TestControlCloseReasonLabelsAreBounded(t *testing.T) {
	m := metrics.New()
	m.IncControlDisconnect("tcp", "kicked")
	m.IncControlDisconnect("attacker-transport", "attacker reason")
	families, err := m.Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	labels := map[string]bool{}
	for _, family := range families {
		if family.GetName() != "noxa_control_disconnects_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			pair := ""
			for _, label := range metric.GetLabel() {
				pair += label.GetName() + "=" + label.GetValue() + ";"
			}
			labels[pair] = true
		}
	}
	for _, want := range []string{"reason=kicked;transport=tcp;", "reason=unknown;transport=unknown;"} {
		if !labels[want] {
			t.Errorf("missing label set %q in %v", want, labels)
		}
	}
	if len(labels) != 2 {
		t.Errorf("label sets = %v, want exactly 2", labels)
	}
}
