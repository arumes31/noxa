package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/nacl/box"
	"voicx/internal/netproto"
)

// deadlineRecordingConn makes deadline ownership observable without sleeps
// or a live server. Incoming frames and outgoing requests stay in memory.
type deadlineRecordingConn struct {
	input        bytes.Buffer
	output       bytes.Buffer
	readDeadline time.Time
}

func (c *deadlineRecordingConn) Read(p []byte) (int, error)        { return c.input.Read(p) }
func (c *deadlineRecordingConn) Write(p []byte) (int, error)       { return c.output.Write(p) }
func (*deadlineRecordingConn) Close() error                        { return nil }
func (*deadlineRecordingConn) LocalAddr() net.Addr                 { return &net.TCPAddr{} }
func (*deadlineRecordingConn) RemoteAddr() net.Addr                { return &net.TCPAddr{} }
func (c *deadlineRecordingConn) SetDeadline(d time.Time) error     { return c.SetReadDeadline(d) }
func (c *deadlineRecordingConn) SetReadDeadline(d time.Time) error { c.readDeadline = d; return nil }
func (*deadlineRecordingConn) SetWriteDeadline(time.Time) error    { return nil }

func queueDeadlineTestFrame(t *testing.T, c *deadlineRecordingConn, mt netproto.MessageType, msg any) {
	t.Helper()
	f, err := netproto.Encode(mt, msg)
	if err != nil {
		t.Fatal(err)
	}
	if err := netproto.WriteFrame(&c.input, f); err != nil {
		t.Fatal(err)
	}
}

func TestReadEventReleasesDeadlineOnEveryExit(t *testing.T) {
	for _, outcome := range []string{"event", "server_error", "invalid_event", "eof"} {
		t.Run(outcome, func(t *testing.T) {
			conn := &deadlineRecordingConn{}
			switch outcome {
			case "event":
				queueDeadlineTestFrame(t, conn, netproto.MsgEvent, eventEnvelope{Type: "wanted"})
			case "server_error":
				queueDeadlineTestFrame(t, conn, netproto.MsgError, netproto.Error{Code: 5, Message: "unavailable"})
			case "invalid_event":
				if err := netproto.WriteFrame(&conn.input, &netproto.Frame{Type: uint16(netproto.MsgEvent), Payload: []byte("{")}); err != nil {
					t.Fatal(err)
				}
			}
			_, err := readEvent(conn, "wanted", time.Second)
			if (err == nil) != (outcome == "event") {
				t.Fatalf("readEvent error = %v", err)
			}
			if !conn.readDeadline.IsZero() {
				t.Fatal("readEvent left a deadline on the reusable connection")
			}
		})
	}
}

func TestChaosJoinWithEarlyScopeKeyDoesNotLeaveReadDeadline(t *testing.T) {
	conn := &deadlineRecordingConn{}
	cl := &client{conn: conn, clientID: "traffic"}
	if err := initClientKeys(cl); err != nil {
		t.Fatal(err)
	}
	clientsByConn.Store(conn, cl)
	t.Cleanup(func() { clientsByConn.Delete(conn) })
	key := bytes.Repeat([]byte{7}, 32)
	sealed, err := box.SealAnonymous(nil, key, &cl.e2ePub, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	queueDeadlineTestFrame(t, conn, netproto.MsgChannelKey, netproto.ChannelKey{ChannelID: 42, KeyID: 1, SealedKey: base64.StdEncoding.EncodeToString(sealed)})
	queueDeadlineTestFrame(t, conn, netproto.MsgEvent, eventEnvelope{Type: "user_moved", Data: json.RawMessage(`{"client_id":"traffic"}`)})
	if err := chaosJoin(cl, 42); err != nil {
		t.Fatal(err)
	}
	if cl.scopeLatest[42] != 1 {
		t.Fatal("join did not capture the early scope key")
	}
	if !conn.readDeadline.IsZero() {
		t.Fatal("chaos session inherits the expired join deadline when its key arrives before membership")
	}
}
