package main

import (
	"encoding/json"
	"fmt"
	"testing"

	"noxa/internal/e2ee/durable"
	"noxa/internal/netproto"
)

// 1c-iii-b item 3: control routing. A control never reaches the chat
// projections; only a newly committed, delivered control from another device
// is handed to the control handlers, exactly once.
func TestRatchetControlRouting(t *testing.T) {
	c, err := newTestRatchetConsumer(t, func(netproto.OlmAdmissionRequest) (json.RawMessage, error) { return nil, nil }, 300)
	if err != nil {
		t.Fatal(err)
	}
	projected, handed := 0, []RatchetControl{}
	c.project(func(RatchetRecord, bool) { projected++ })
	c.onControl(func(ctl RatchetControl) { handed = append(handed, ctl) })
	now := c.now()
	body := json.RawMessage(fmt.Sprintf(`{"kind":"media-key","scope":{"type":"channel","id":"7"},"epoch":2,"expires_at":%d,"data":{"k":"v"}}`, now+60))
	rec := RatchetRecord{Protocol: ratchetProtocol, Sender: "bob", SenderDevice: "b1", LogicalID: "ctl", Scope: "direct", Peer: "bob", Purpose: "control",
		Body: body, Status: ratchetControlDisposition(body, now), Transport: RatchetTransport{Delivery: "d1"}}
	if rec.Status != "delivered" {
		t.Fatal(rec.Status)
	}
	if err := c.emit(rec, true); err != nil {
		t.Fatal(err)
	}
	if err := c.emit(rec, false); err != nil { // replay after a lost ACK
		t.Fatal(err)
	}
	own := rec
	own.SenderDevice, own.Transport = c.d.Namespace.Device, RatchetTransport{Attempt: "1:1"}
	expired := rec
	expired.Status, expired.Body = "expired", nil
	for _, r := range []RatchetRecord{own, expired} {
		if err := c.emit(r, true); err != nil {
			t.Fatal(err)
		}
	}
	if projected != 0 {
		t.Fatalf("controls reached the chat projections %d times", projected)
	}
	if len(handed) != 1 || handed[0].Kind != "media-key" || handed[0].Scope.ID != "7" || handed[0].Epoch != 2 || string(handed[0].Data) != `{"k":"v"}` || handed[0].Delivery != "d1" {
		t.Fatalf("handed out: %+v", handed)
	}
	// Chat still projects.
	if err := c.emit(RatchetRecord{Protocol: ratchetProtocol, Purpose: "chat", Body: json.RawMessage(`{"text":"x"}`)}, true); err != nil || projected != 1 {
		t.Fatal(projected, err)
	}
	for body, want := range map[string]string{
		fmt.Sprintf(`{"kind":"media-key","scope":{"type":"call","id":"c"},"epoch":0,"expires_at":%d}`, now-1): "expired",
		`{"kind":"media-key"}`: "invalid",
		`"text"`:               "invalid",
	} {
		if got := ratchetControlDisposition(json.RawMessage(body), now); got != want {
			t.Fatalf("%s: %s, want %s", body, got, want)
		}
	}
}

// A control names one other device and a valid, short-lived body; it is
// refused locally before any network call otherwise.
func TestRatchetControlSealValidation(t *testing.T) {
	calls := 0
	c, err := newTestRatchetConsumer(t, func(netproto.OlmAdmissionRequest) (json.RawMessage, error) { calls++; return nil, nil }, 300)
	if err != nil {
		t.Fatal(err)
	}
	now := c.now()
	good := json.RawMessage(fmt.Sprintf(`{"kind":"media-key","scope":{"type":"channel","id":"7"},"epoch":1,"expires_at":%d}`, now+60))
	a := durable.AccountAuthority{AccountID: "acct", Epoch: 1}
	for name, in := range map[string]ratchetIntent{
		"no device":      {LogicalID: "c1", Scope: "direct", Peer: "bob", Purpose: "control", Body: good},
		"own device":     {LogicalID: "c1", Scope: "direct", Peer: "alice", Device: "a1", Purpose: "control", Body: good},
		"group scope":    {LogicalID: "c1", Scope: "group", Conversation: "g", Device: "b1", Purpose: "control", Body: good},
		"reference":      {LogicalID: "c1", Scope: "direct", Peer: "bob", Device: "b1", Purpose: "control", Reference: "m", Body: good},
		"expired":        {LogicalID: "c1", Scope: "direct", Peer: "bob", Device: "b1", Purpose: "control", Body: json.RawMessage(fmt.Sprintf(`{"kind":"media-key","scope":{"type":"channel","id":"7"},"epoch":1,"expires_at":%d}`, now))},
		"too short":      {LogicalID: "c1", Scope: "direct", Peer: "bob", Device: "b1", Purpose: "control", Body: json.RawMessage(fmt.Sprintf(`{"kind":"media-key","scope":{"type":"channel","id":"7"},"epoch":1,"expires_at":%d}`, now+20))},
		"chat w/ device": {LogicalID: "c1", Scope: "direct", Peer: "bob", Device: "b1", Purpose: "chat", Body: json.RawMessage(`{"text":"x"}`)},
		"malformed body": {LogicalID: "c1", Scope: "direct", Peer: "bob", Device: "b1", Purpose: "control", Body: json.RawMessage(`{"kind":"x"}`)},
	} {
		if _, err := c.seal(a, in, false); ratchetCode(err) != "invalid" {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if calls != 0 {
		t.Fatalf("network calls before validation: %d", calls)
	}
}

func TestRatchetControlRequestsTargetedSnapshot(t *testing.T) {
	var requests []netproto.OlmAdmissionRequest
	c, err := newTestRatchetConsumer(t, func(r netproto.OlmAdmissionRequest) (json.RawMessage, error) {
		requests = append(requests, r)
		return nil, rfail("unavailable")
	}, 300)
	if err != nil {
		t.Fatal(err)
	}
	a, err := c.authority()
	if err != nil {
		t.Fatal(err)
	}
	body := json.RawMessage(fmt.Sprintf(`{"kind":"media-key","scope":{"type":"channel","id":"7"},"epoch":1,"expires_at":%d}`, c.now()+120))
	for _, in := range []ratchetIntent{
		{LogicalID: "control", Scope: "direct", Peer: "bob", Device: "b1", Purpose: "control", Body: body},
		{LogicalID: "chat", Scope: "direct", Peer: "bob", Purpose: "chat", Body: json.RawMessage(`{"text":"hi"}`)},
	} {
		if _, err = c.seal(a, in, false); ratchetCode(err) != "unavailable" {
			t.Fatalf("snapshot refusal: %v", err)
		}
	}
	if len(requests) != 2 || requests[0].Action != "snapshot" || requests[0].Scope != "direct" || requests[0].Peer != "bob" || requests[0].TargetDevice != "b1" || requests[0].Conversation != "" || requests[1].TargetDevice != "" {
		t.Fatalf("snapshot requests: %+v", requests)
	}
}
