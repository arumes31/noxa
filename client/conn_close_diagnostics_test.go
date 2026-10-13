package main

import (
	"bytes"
	"errors"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"noxa/internal/netproto"
)

type closeDiagnosticReadConn struct {
	net.Conn
	failure error
}

func (c closeDiagnosticReadConn) Read([]byte) (int, error) { return 0, c.failure }

func captureConnectionCloseLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	writer, flags, prefix := log.Writer(), log.Flags(), log.Prefix()
	var output bytes.Buffer
	log.SetOutput(&output)
	log.SetFlags(0)
	log.SetPrefix("")
	t.Cleanup(func() { log.SetOutput(writer); log.SetFlags(flags); log.SetPrefix(prefix) })
	return &output
}

func TestConnectionReadCloseDiagnostic(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failure error
		reason  string
	}{
		{"eof", io.EOF, "eof"},
		{"timeout", &net.OpError{Op: "secret-operation", Net: "secret-network", Err: os.ErrDeadlineExceeded}, "read_timeout"},
		{"error", errors.New("secret endpoint/token/payload"), "read_error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output := captureConnectionCloseLog(t)
			local, peer := net.Pipe()
			defer peer.Close()
			cm := newTestConnManager()
			conn := closeDiagnosticReadConn{local, tc.failure}
			cm.conn, cm.connEpoch = conn, 17
			cm.readLoop(conn)
			want := "control connection closed reason=" + tc.reason + " epoch=17\n"
			if output.String() != want {
				t.Fatalf("unexpected diagnostic: %q", output.String())
			}
			if cm.connected() || cm.connEpoch != 18 {
				t.Fatal("termination changed ownership behavior")
			}
		})
	}
}

func TestConnectionCloseDiagnosticExactlyOnceAndNoStaleEpoch(t *testing.T) {
	output := captureConnectionCloseLog(t)
	local, peer := net.Pipe()
	defer peer.Close()
	cm := newTestConnManager()
	cm.conn, cm.connEpoch = local, 23
	var winners atomic.Int32
	var workers sync.WaitGroup
	for range 2 {
		workers.Go(func() {
			if cm.terminateConn(local, true, netproto.ControlCloseReadTimeout) {
				winners.Add(1)
			}
		})
	}
	workers.Wait()
	if winners.Load() != 1 || strings.Count(output.String(), "control connection closed") != 1 {
		t.Fatal("duplicate close diagnostic")
	}
	next, nextPeer := net.Pipe()
	defer nextPeer.Close()
	cm.conn = next
	if cm.terminateConn(local, true, netproto.ControlCloseWriteError) {
		t.Fatal("stale connection terminated replacement")
	}
	cm.disconnect()
	cm.disconnect()
	want := "control connection closed reason=read_timeout epoch=23\ncontrol connection closed reason=local_close epoch=24\n"
	if output.String() != want {
		t.Fatalf("unexpected diagnostic: %q", output.String())
	}
}
