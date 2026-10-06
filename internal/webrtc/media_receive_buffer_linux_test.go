//go:build linux

package webrtc

import (
	"net"
	"testing"

	"go.uber.org/zap"
)

func TestMediaReceiveBufferReportsKernelAllocation(t *testing.T) {
	var config net.ListenConfig
	conn, err := config.ListenPacket(t.Context(), "udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	configureMediaReceiveBuffer(conn, zap.NewNop())
	actual, err := effectiveMediaReceiveBuffer(conn)
	if err != nil || actual <= 0 {
		t.Fatalf("actual kernel receive buffer = %d, error = %v", actual, err)
	}
	t.Logf("requested %d bytes, actual kernel allocation %d bytes", mediaReceiveBufferBytes, actual)
}
