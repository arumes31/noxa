package server

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"noxa/internal/config"
	"noxa/internal/netproto"
)

func TestServeExternalConnRejectsBeforeStart(t *testing.T) {
	srv := New(&config.Config{TCPAddr: "127.0.0.1:0"}, testLogger(), nil)
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	if err := srv.ServeExternalConn(t.Context(), server); !errors.Is(err, ErrConnectionRejected) {
		t.Fatalf("ServeExternalConn before Start = %v, want ErrConnectionRejected", err)
	}
	if _, err := client.Write([]byte{0}); err == nil {
		t.Fatal("rejected connection stayed open")
	}
}

func TestServeExternalConnRunsControlProtocol(t *testing.T) {
	srv, _, startErr := startPlainTCPServer(t)
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	served := make(chan error, 1)
	go func() { served <- srv.ServeExternalConn(context.Background(), server) }()

	ping, err := netproto.Encode(netproto.MsgPing, netproto.Ping{})
	if err != nil {
		t.Fatalf("encode ping: %v", err)
	}
	_ = client.SetDeadline(time.Now().Add(3 * time.Second))
	if err := netproto.WriteFrame(client, ping); err != nil {
		t.Fatalf("write ping: %v", err)
	}
	resp, err := netproto.ReadFrame(client)
	if err != nil {
		t.Fatalf("read pong: %v", err)
	}
	if netproto.MessageType(resp.Type) != netproto.MsgPong {
		t.Fatalf("response type = %s, want Pong", netproto.MessageType(resp.Type))
	}

	if err := srv.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if err := <-served; err != nil {
		t.Fatalf("ServeExternalConn: %v", err)
	}
	if err := <-startErr; err != nil {
		t.Fatalf("Start: %v", err)
	}
}

func TestServeExternalConnSharesPreauthLimit(t *testing.T) {
	srv, _, startErr := startPlainTCPServer(t)
	defer func() {
		_ = srv.Shutdown(context.Background())
		<-startErr
	}()
	srv.lifecycleMu.Lock()
	srv.preauthByIP["pipe"] = maxPreauthPerIP
	srv.lifecycleMu.Unlock()

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	if err := srv.ServeExternalConn(t.Context(), server); !errors.Is(err, ErrConnectionRejected) {
		t.Fatalf("ServeExternalConn over the per-IP limit = %v, want ErrConnectionRejected", err)
	}
}
