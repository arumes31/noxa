package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"noxa/internal/e2ee/admission"
	"noxa/internal/e2ee/ratchet"
	"strings"
	"testing"
	"time"
)

func TestOlmAdmissionComplete160AndLastClaimRollback(t *testing.T) {
	s := pollTestStore(t)
	ctx := t.Context()
	var a OlmSource
	devices := []OlmSource{}
	const group = "00000000-0000-4000-8000-000000000160"
	if _, e := s.db.Exec(`INSERT INTO private_conversations(id,name,owner_uid) VALUES($1,'160 device group','member-00')`, group); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 16; i++ {
		p := fmt.Sprintf("member-%02d", i)
		if _, e := s.db.Exec(`INSERT INTO users(unique_id,nickname) VALUES($1,$1)`, p); e != nil {
			t.Fatal(e)
		}
		if _, e := s.db.Exec(`INSERT INTO private_conversation_members(conversation_id,unique_id,pending,joined_epoch) VALUES($1,$2,FALSE,1)`, group, p); e != nil {
			t.Fatal(e)
		}
		var first string
		for j := 0; j < 10; j++ {
			d, _, b := olmFixture(t, s, p)
			if j == 0 {
				first = d.DeviceID
			} else {
				if _, e := s.EndorseDevice(ctx, p, d.DeviceID, first, []byte("fixture verified signature")); e != nil {
					t.Fatal(e)
				}
			}
			if e := s.PublishOlmBundle(ctx, p, d.DeviceID, b.Server, b); e != nil {
				t.Fatal(e)
			}
			v := OlmSource{Server: b.Server, Principal: p, Device: d.DeviceID, Signing: d.Ed25519Public, AccountEpoch: 1, AllowGroups: true}
			devices = append(devices, v)
		}
	}
	a = devices[0]
	snap, e := s.OlmSnapshot(ctx, a, "group", "", group)
	if e != nil || len(snap.Targets) != 160 || snap.Revision != 1 || snap.Epoch != 1 {
		t.Fatal(snap, e)
	}
	until := time.Now().Unix() + 300
	m := admission.Manifest{Version: admission.Version, Server: a.Server, Sender: a.Principal, Device: a.Device, AccountEpoch: 1, AttemptID: "1", LogicalID: "group-message", Purpose: "chat", Scope: "group", Conversation: group, Revision: 1, Epoch: 1, Deadline: until, Rosters: snap.Rosters, Targets: snap.Targets}
	copies := []admission.Copy{}
	for _, d := range devices[1:] {
		if _, e = s.ClaimOlmKey(ctx, a.Principal, a.Device, d.Principal, d.Device, "claim-"+d.Device, until, a.Server, 1); e != nil {
			t.Fatal(e)
		}
		envelope := ratchet.Envelope{Version: ratchet.Version, SessionID: "session-" + d.Device, Binding: admission.ExpectedBinding(m, admission.Target{Principal: d.Principal, Device: d.Device, AccountEpoch: 1}), Message: json.RawMessage(`{"type":0,"body":"` + strings.Repeat("c", 18000) + `"}`)}
		raw, _ := json.Marshal(envelope)
		copies = append(copies, admission.Copy{Principal: d.Principal, Device: d.Device, AccountEpoch: 1, SessionID: envelope.SessionID, ClaimID: "claim-" + d.Device, Envelope: raw})
	}
	// The last device in server lock order is deliberately invalid. Any earlier
	// message/copy/OTK updates must roll back together.
	last := 0
	for i := range copies {
		if copies[i].Device > copies[last].Device {
			last = i
		}
	}
	valid := copies[last].ClaimID
	copies[last].ClaimID = "missing"
	submit := func() (admission.Receipt, error) {
		parts, e := admission.Freeze(&m, copies)
		if e != nil {
			return admission.Receipt{}, e
		}
		if len(parts) < 2 {
			t.Fatal("160-device fixture did not exercise bounded multipart transport")
		}
		if _, e = s.BeginOlmAttempt(ctx, a, m); e != nil {
			return admission.Receipt{}, e
		}
		for i, p := range parts {
			if len(p) > 512<<10 {
				t.Fatal("oversized part")
			}
			if e = s.PutOlmPart(ctx, a, m.AttemptID, i, p); e != nil {
				return admission.Receipt{}, e
			}
		}
		return s.FinalizeOlmAttempt(ctx, a, m.AttemptID)
	}
	if _, e = submit(); !errors.Is(e, ErrOlmConflict) {
		t.Fatal("invalid final claim", e)
	}
	var n int
	if e = s.db.QueryRow(`SELECT count(*) FROM olm_deliveries`).Scan(&n); e != nil || n != 0 {
		t.Fatal("partial delivery", n, e)
	}
	if e = s.db.QueryRow(`SELECT count(*) FROM olm_directory_keys WHERE lifecycle='admitted'`).Scan(&n); e != nil || n != 0 {
		t.Fatal("partial OTK admission", n, e)
	}
	if _, e = s.AbortOlmAttempt(ctx, a, m); e != nil {
		t.Fatal(e)
	}
	copies[last].ClaimID = valid
	m.AttemptID = "2"
	r, e := submit()
	if e != nil || r.State != "accepted" {
		t.Fatal(r, e)
	}
	if e = s.db.QueryRow(`SELECT count(*) FROM olm_deliveries`).Scan(&n); e != nil || n != 159 {
		t.Fatal(n, e)
	}
}

func TestOlmAdmissionFilteredDeliveryPageAdvances(t *testing.T) {
	s, a, b, m, parts := admissionFixture(t)
	ctx := t.Context()
	if _, e := s.BeginOlmAttempt(ctx, a, m); e != nil {
		t.Fatal(e)
	}
	for i, p := range parts {
		if e := s.PutOlmPart(ctx, a, m.AttemptID, i, p); e != nil {
			t.Fatal(e)
		}
	}
	r, e := s.FinalizeOlmAttempt(ctx, a, m.AttemptID)
	if e != nil || r.State != "accepted" {
		t.Fatal(r, e)
	}
	// Seed two retained, previously admitted group deliveries before the DM. This
	// group no longer exists; its accepted ciphertext must neither leak nor vanish.
	m.Scope = "group"
	m.Peer = ""
	m.Conversation = "00000000-0000-4000-8000-000000000001"
	m.Revision = 1
	m.Epoch = 1
	raw, _ := json.Marshal(m)
	for i := 0; i < 2; i++ {
		id := fmt.Sprintf("000%d", i)
		if _, e = s.db.Exec(`INSERT INTO olm_messages(id,server,sender,device_id,account_epoch,logical_id,manifest) VALUES($1,$2,$3,$4,1,$1,$5)`, id, a.Server, a.Principal, a.Device, raw); e != nil {
			t.Fatal(e)
		}
		if _, e = s.db.Exec(`INSERT INTO olm_deliveries(id,message_id,principal,device_id,account_epoch,copy,digest) SELECT $1,$1,principal,device_id,account_epoch,copy,digest FROM olm_deliveries WHERE message_id=$2`, id, r.MessageID); e != nil {
			t.Fatal(e)
		}
	}
	var page OlmDeliveryPage
	if e = s.WithOlmDeliveries(ctx, b, "", func(p OlmDeliveryPage) error { page = p; return nil }); e != nil {
		t.Fatal(e)
	}
	if len(page.Deliveries) != 0 || page.After != "0001" || !page.HasMore {
		t.Fatal(page)
	}
	if e = s.WithOlmDeliveries(ctx, b, page.After, func(p OlmDeliveryPage) error { page = p; return nil }); e != nil || len(page.Deliveries) != 1 || page.Deliveries[0].Manifest.Scope != "direct" {
		t.Fatal(page, e)
	}
	var pending int
	if e = s.db.QueryRow(`SELECT count(*) FROM olm_deliveries WHERE state='pending'`).Scan(&pending); e != nil || pending != 3 {
		t.Fatal("filtered pending data removed", pending, e)
	}
}
