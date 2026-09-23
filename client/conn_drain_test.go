package main

import (
	"net"
	"strings"
	"testing"
	"time"

	"noxa/internal/netproto"
)

type drainTestPeer struct {
	conn   net.Conn
	frames chan *netproto.Frame
}

func attachDrainTestPeer(t *testing.T, cm *connManager) drainTestPeer {
	t.Helper()
	client, server := net.Pipe()
	cm.mu.Lock()
	cm.conn = client
	cm.connEpoch++
	cm.closed = false
	cm.mu.Unlock()
	peer := drainTestPeer{conn: server, frames: make(chan *netproto.Frame, 16)}
	go cm.readLoop(client)
	go func() {
		for {
			frame, err := netproto.ReadFrame(server)
			if err != nil {
				return
			}
			peer.frames <- frame
		}
	}()
	t.Cleanup(func() { cm.disconnect(); _ = server.Close() })
	return peer
}

func (p drainTestPeer) next(t *testing.T, want netproto.MessageType) {
	t.Helper()
	select {
	case frame := <-p.frames:
		if netproto.MessageType(frame.Type) != want {
			t.Fatalf("sent %s, want %s", netproto.MessageType(frame.Type), want)
		}
	case <-time.After(time.Second):
		t.Fatalf("no outgoing %s", want)
	}
}

func (p drainTestPeer) send(t *testing.T, kind netproto.MessageType, body any) {
	t.Helper()
	_ = p.conn.SetWriteDeadline(time.Now().Add(time.Second))
	if err := netproto.WriteFrame(p.conn, mustEncode(kind, body)); err != nil {
		t.Fatal(err)
	}
	_ = p.conn.SetWriteDeadline(time.Time{})
}

// A pong proves the read loop dispatched every frame sent before this ping.
func (p drainTestPeer) barrier(t *testing.T) {
	t.Helper()
	p.send(t, netproto.MsgPing, netproto.Ping{})
	p.next(t, netproto.MsgPong)
}

func drainRequest(cm *connManager, send, reply netproto.MessageType, timeout time.Duration) <-chan requestResult {
	done := make(chan requestResult, 1)
	go func() {
		frame, err := cm.request(send, reply, struct{}{}, timeout)
		done <- requestResult{frame: frame, err: err}
	}()
	return done
}

func awaitDrainRequest(t *testing.T, done <-chan requestResult) requestResult {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(time.Second):
		t.Fatal("request did not return promptly")
		return requestResult{}
	}
}

func TestReadOnlyRequestTimeoutPreservesConnection(t *testing.T) {
	for _, query := range []struct{ send, reply netproto.MessageType }{
		{netproto.MsgAvatarGet, netproto.MsgAvatarData},
		{netproto.MsgClientInfoQuery, netproto.MsgClientInfoResponse},
	} {
		t.Run(query.send.String(), func(t *testing.T) {
			cm := newTestConnManager()
			peer := attachDrainTestPeer(t, cm)
			done := drainRequest(cm, query.send, query.reply, 25*time.Millisecond)
			peer.next(t, query.send)
			if result := awaitDrainRequest(t, done); result.err == nil || !strings.Contains(result.err.Error(), "timeout") {
				t.Fatalf("expected timeout, got %v", result.err)
			}
			if !cm.connected() {
				t.Fatal("read-only query timeout disconnected the control channel")
			}
			peer.barrier(t)
		})
	}
}

func TestReadOnlyRequestDrainRejectsRetryAndAllowsOtherQueries(t *testing.T) {
	cm := newTestConnManager()
	peer := attachDrainTestPeer(t, cm)
	first := drainRequest(cm, netproto.MsgAvatarGet, netproto.MsgAvatarData, 25*time.Millisecond)
	peer.next(t, netproto.MsgAvatarGet)
	if result := awaitDrainRequest(t, first); result.err == nil {
		t.Fatal("first query did not time out")
	}
	if !cm.connected() {
		t.Fatal("query timeout disconnected control channel")
	}

	retry := drainRequest(cm, netproto.MsgAvatarGet, netproto.MsgAvatarData, time.Second)
	select {
	case frame := <-peer.frames:
		t.Fatalf("same-type retry was sent before old reply drained: %s", netproto.MessageType(frame.Type))
	case result := <-retry:
		if result.err == nil {
			t.Fatal("same-type retry succeeded without a reply")
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("same-type retry blocked instead of returning promptly")
	}
	select {
	case frame := <-peer.frames:
		t.Fatalf("rejected retry still sent %s", netproto.MessageType(frame.Type))
	default:
	}

	unrelated := drainRequest(cm, netproto.MsgClientInfoQuery, netproto.MsgClientInfoResponse, time.Second)
	peer.next(t, netproto.MsgClientInfoQuery)
	peer.send(t, netproto.MsgClientInfoResponse, struct{}{})
	if result := awaitDrainRequest(t, unrelated); result.err != nil {
		t.Fatalf("unrelated query failed: %v", result.err)
	}
	peer.send(t, netproto.MsgAvatarData, netproto.AvatarData{})
	peer.barrier(t)
	if !cm.connected() {
		t.Fatal("drained reply disconnected control channel")
	}
}

func TestReadOnlyRequestLateReplyOrErrorDrainsSlot(t *testing.T) {
	for _, useError := range []bool{false, true} {
		name := "typed reply"
		if useError {
			name = "origin error"
		}
		t.Run(name, func(t *testing.T) {
			cm := newTestConnManager()
			peer := attachDrainTestPeer(t, cm)
			first := drainRequest(cm, netproto.MsgAvatarGet, netproto.MsgAvatarData, 25*time.Millisecond)
			peer.next(t, netproto.MsgAvatarGet)
			if result := awaitDrainRequest(t, first); result.err == nil {
				t.Fatal("first query did not time out")
			}
			if !cm.connected() {
				t.Fatal("query timeout disconnected control channel")
			}
			if useError {
				peer.send(t, netproto.MsgError, netproto.Error{OriginType: uint16(netproto.MsgAvatarGet), Message: "not found"})
			} else {
				peer.send(t, netproto.MsgAvatarData, netproto.AvatarData{UniqueID: "old"})
			}
			peer.barrier(t)
			next := drainRequest(cm, netproto.MsgAvatarGet, netproto.MsgAvatarData, time.Second)
			peer.next(t, netproto.MsgAvatarGet)
			peer.send(t, netproto.MsgAvatarData, netproto.AvatarData{UniqueID: "new"})
			result := awaitDrainRequest(t, next)
			if result.err != nil {
				t.Fatalf("query after drain failed: %v", result.err)
			}
			var avatar netproto.AvatarData
			if err := netproto.Decode(result.frame, &avatar); err != nil {
				t.Fatal(err)
			}
			if avatar.UniqueID != "new" {
				t.Fatalf("received stale avatar %q", avatar.UniqueID)
			}
		})
	}
}

func TestReadOnlyRequestUnresolvedDrainDisconnectsOnce(t *testing.T) {
	cm := newTestConnManager()
	cm.requestDrainTimeout = 50 * time.Millisecond
	sink := &recordingSink{}
	cm.sink = sink
	peer := attachDrainTestPeer(t, cm)
	first := drainRequest(cm, netproto.MsgAvatarGet, netproto.MsgAvatarData, 25*time.Millisecond)
	peer.next(t, netproto.MsgAvatarGet)
	if result := awaitDrainRequest(t, first); result.err == nil {
		t.Fatal("first query did not time out")
	}
	if !cm.connected() {
		t.Fatal("query timeout disconnected before drain grace")
	}
	waiter := drainRequest(cm, netproto.MsgClientInfoQuery, netproto.MsgClientInfoResponse, time.Second)
	peer.next(t, netproto.MsgClientInfoQuery)
	if result := awaitDrainRequest(t, waiter); result.err == nil {
		t.Fatal("grace expiry did not wake unrelated waiter")
	}
	if cm.connected() {
		t.Fatal("unresolved query survived drain grace")
	}
	time.Sleep(25 * time.Millisecond)
	if got := sink.count("disconnected"); got != 1 {
		t.Fatalf("disconnected events = %d, want 1", got)
	}
}

func TestReadOnlyRequestDrainedTimerCannotCloseNextRequest(t *testing.T) {
	cm := newTestConnManager()
	cm.requestDrainTimeout = 100 * time.Millisecond
	peer := attachDrainTestPeer(t, cm)
	first := drainRequest(cm, netproto.MsgAvatarGet, netproto.MsgAvatarData, 25*time.Millisecond)
	peer.next(t, netproto.MsgAvatarGet)
	if result := awaitDrainRequest(t, first); result.err == nil {
		t.Fatal("first query did not time out")
	}
	if !cm.connected() {
		t.Fatal("query timeout disconnected control channel")
	}
	peer.send(t, netproto.MsgAvatarData, netproto.AvatarData{})
	peer.barrier(t)
	next := drainRequest(cm, netproto.MsgAvatarGet, netproto.MsgAvatarData, time.Second)
	peer.next(t, netproto.MsgAvatarGet)
	time.Sleep(150 * time.Millisecond)
	if !cm.connected() {
		t.Fatal("drained query's timer closed the next request")
	}
	peer.send(t, netproto.MsgAvatarData, netproto.AvatarData{})
	if result := awaitDrainRequest(t, next); result.err != nil {
		t.Fatalf("next query failed: %v", result.err)
	}
}

func TestReadOnlyRequestOldDrainCannotCloseReplacement(t *testing.T) {
	cm := newTestConnManager()
	cm.requestDrainTimeout = 100 * time.Millisecond
	old := attachDrainTestPeer(t, cm)
	first := drainRequest(cm, netproto.MsgAvatarGet, netproto.MsgAvatarData, 25*time.Millisecond)
	old.next(t, netproto.MsgAvatarGet)
	if result := awaitDrainRequest(t, first); result.err == nil {
		t.Fatal("first query did not time out")
	}
	if !cm.connected() {
		t.Fatal("query timeout disconnected control channel")
	}
	cm.disconnect()
	replacement := attachDrainTestPeer(t, cm)
	next := drainRequest(cm, netproto.MsgAvatarGet, netproto.MsgAvatarData, time.Second)
	replacement.next(t, netproto.MsgAvatarGet)
	time.Sleep(150 * time.Millisecond)
	if !cm.connected() {
		t.Fatal("old query's drain timer closed replacement connection")
	}
	replacement.send(t, netproto.MsgAvatarData, netproto.AvatarData{})
	if result := awaitDrainRequest(t, next); result.err != nil {
		t.Fatalf("replacement query failed: %v", result.err)
	}
}

func TestMutationRequestTimeoutStillDisconnects(t *testing.T) {
	cm := newTestConnManager()
	peer := attachDrainTestPeer(t, cm)
	done := drainRequest(cm, netproto.MsgVideoStreamControl, netproto.MsgVideoStreamResult, 25*time.Millisecond)
	peer.next(t, netproto.MsgVideoStreamControl)
	if result := awaitDrainRequest(t, done); result.err == nil || !strings.Contains(result.err.Error(), "timeout") {
		t.Fatalf("expected timeout, got %v", result.err)
	}
	if cm.connected() {
		t.Fatal("timed-out stateful operation kept the connection alive")
	}
}
