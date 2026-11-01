package main

import (
	"bytes"
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"noxa/internal/e2ee"

	"noxa/internal/netproto"
)

func TestDispatchRulesAndAuthoritativeSubscriptions(t *testing.T) {
	rec := &eventRecorder{}
	cm := newConnManager(context.Background())
	cm.sink = rec

	subscriptions := []byte(`{"channel_ids":[3,9]}`)
	cm.dispatch(&netproto.Frame{Type: uint16(netproto.MsgSubscriptionState), Payload: subscriptions})
	cm.dispatch(&netproto.Frame{Type: uint16(netproto.MsgServerRules), Payload: []byte(`{"text":"be kind","hash":"v1"}`)})

	if cm.lastSubscriptions != string(subscriptions) {
		t.Fatalf("lastSubscriptions = %q, want %q", cm.lastSubscriptions, subscriptions)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.events) != 2 {
		t.Fatalf("events = %+v, want subscriptions and server_rules", rec.events)
	}
	if rec.events[0].name != "subscriptions" || rec.events[1].name != "server_rules" {
		t.Fatalf("event order = %q, %q", rec.events[0].name, rec.events[1].name)
	}
}

func TestAcceptServerRulesWritesExactHash(t *testing.T) {
	got := make(chan netproto.ServerRulesAccept, 1)
	app, _ := newPipedApp(t, func(f *netproto.Frame) (netproto.MessageType, any, bool) {
		if netproto.MessageType(f.Type) != netproto.MsgServerRulesAccept {
			return 0, nil, false
		}
		var accept netproto.ServerRulesAccept
		if err := netproto.Decode(f, &accept); err == nil {
			got <- accept
		}
		return 0, nil, false
	})

	if err := app.AcceptServerRules("rules-v2"); err != "" {
		t.Fatalf("AcceptServerRules returned %q", err)
	}
	if accept := <-got; accept.Hash != "rules-v2" {
		t.Fatalf("accepted hash = %q, want rules-v2", accept.Hash)
	}
}

// A key-identity guest's first publication is rejected by the pending-rules
// gate. Only the successful acceptance response permits the same root again.
func TestRulesAcceptanceRepublishesRejectedGuestIdentity(t *testing.T) {
	pending := true
	var publications atomic.Int32
	initial := make(chan string, 1)
	recovered := make(chan string, 2)
	app, cm := newPipedApp(t, func(f *netproto.Frame) (netproto.MessageType, any, bool) {
		switch netproto.MessageType(f.Type) {
		case netproto.MsgKeyPublish:
			publications.Add(1)
			var publication netproto.KeyPublish
			if err := netproto.Decode(f, &publication); err != nil {
				return 0, nil, false
			}
			if pending {
				initial <- publication.PublicKey
				return netproto.MsgError, netproto.Error{Code: 403, OriginType: uint16(netproto.MsgKeyPublish), Message: "accept the server rules before continuing"}, true
			}
			recovered <- publication.PublicKey
		case netproto.MsgServerRulesAccept:
			var acceptance netproto.ServerRulesAccept
			_ = netproto.Decode(f, &acceptance)
			if acceptance.Hash != "current" {
				return netproto.MsgServerRules, netproto.ServerRules{Text: "updated rules", Hash: "current"}, true
			}
			pending = false
			return netproto.MsgServerRules, netproto.ServerRules{}, true
		}
		return 0, nil, false
	})
	cm.mu.Lock()
	cm.uniqueID, cm.isGuest = "guest-identity", true
	conn, epoch := cm.conn, cm.connEpoch
	cm.mu.Unlock()
	prompt, _ := netproto.Encode(netproto.MsgServerRules, netproto.ServerRules{Text: "rules", Hash: "old"})
	cm.dispatchFrom(conn, epoch, prompt)
	if err := cm.publishE2EKeyConn(conn, "guest-identity"); err != nil {
		t.Fatal(err)
	}
	var first string
	select {
	case first = <-initial:
	case <-time.After(2 * time.Second):
		t.Fatal("initial publication was not attempted")
	}
	if err := app.AcceptServerRules("old"); err != "" {
		t.Fatal(err)
	}
	if err := app.AcceptServerRules("current"); err != "" {
		t.Fatal(err)
	}
	select {
	case root := <-recovered:
		if root != first {
			t.Fatal("acceptance changed the signed root")
		}
		if _, err := e2ee.ParseKeyRecord(root, "guest-identity"); err != nil {
			t.Fatalf("recovered publication is not a valid signed root: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("accepted rules did not recover the rejected root publication")
	}
	if got := publications.Load(); got != 2 {
		t.Fatalf("publication count = %d, want initial attempt and one recovery", got)
	}
	select {
	case <-recovered:
		t.Fatal("a changed rules prompt triggered an extra publication")
	default:
	}
}

type rulesRecoveryCaptureConn struct {
	net.Conn
	data bytes.Buffer
}

func (c *rulesRecoveryCaptureConn) Write(p []byte) (int, error) { return c.data.Write(p) }

func TestRulesRecoveryRequiresCurrentConnectionAcceptance(t *testing.T) {
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	conn := &rulesRecoveryCaptureConn{Conn: client}
	cm := newTestConnManager()
	cm.conn, cm.connEpoch, cm.uniqueID = conn, 2, "guest-identity"
	prompt, _ := netproto.Encode(netproto.MsgServerRules, netproto.ServerRules{Text: "rules", Hash: "current"})
	ack, _ := netproto.Encode(netproto.MsgServerRules, netproto.ServerRules{})
	// An unsolicited empty frame cannot republish a root.
	cm.dispatchFrom(conn, 2, ack)
	cm.dispatchFrom(conn, 2, prompt)
	cm.dispatchFrom(conn, 1, ack)
	cm.dispatchFrom(client, 2, ack)
	cm.dispatchFrom(conn, 2, &netproto.Frame{Type: uint16(netproto.MsgServerRules), Payload: []byte("not JSON")})
	cm.dispatchFrom(conn, 2, &netproto.Frame{Type: uint16(netproto.MsgServerRules), Payload: []byte("null")})
	if conn.data.Len() != 0 {
		t.Fatal("stale or malformed rules acknowledgement published a root")
	}
	cm.dispatchFrom(conn, 2, ack)
	if conn.data.Len() == 0 {
		t.Fatal("current acceptance did not publish a root")
	}
	written := conn.data.Len()
	cm.dispatchFrom(conn, 2, ack)
	if conn.data.Len() != written {
		t.Fatal("duplicate acceptance republished a root")
	}
}
