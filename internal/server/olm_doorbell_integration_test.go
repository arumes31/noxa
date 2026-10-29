//go:build integration

package server

import (
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"noxa/internal/auth"
	"noxa/internal/config"
	"noxa/internal/e2ee/admission"
	"noxa/internal/e2ee/olmdir"
	"noxa/internal/netproto"
	"noxa/internal/store"
)

// Fix review P1-1 (repro adopted): an attempt that is rejected (nothing
// delivered) never rings the olm_pending doorbell, however often it is
// finalized, and the victim's own admission stays usable after the burst.
// Real TCP server, real store, PostgreSQL.
func TestOlmDoorbellNeverRingsForRejectedOrRepeatedFinalize(t *testing.T) {
	db := integrationManagementStore(t)
	ctx := t.Context()
	env := startTestEnvLogger(t, nil, func(c *config.Config) { c.ChatRateMsgs = 5; c.ChatRateWindowSeconds = 3 },
		func(d *Deps) { d.Devices = db }, nil, func(s *TCPServer) { s.tlsFingerprint = strings.TrimSuffix(strings.Repeat("aa:", 32), ":") })
	defer env.stop()
	binding, e0 := olmdir.ServerBinding(env.srv.TLSFingerprint())
	if e0 != nil {
		t.Fatal(e0)
	}

	apub, _, _ := auth.GenerateIdentityKeyPair()
	ad, e := db.AddDevice(ctx, store.NewDevice{Principal: "admin-uid", Ed25519Public: apub, Kind: store.DeviceKindDesktop}, 10)
	if e != nil {
		t.Fatal(e)
	}
	vpub, _, _ := auth.GenerateIdentityKeyPair()
	vd, e := db.AddDevice(ctx, store.NewDevice{Principal: "user-uid", Ed25519Public: vpub, Kind: store.DeviceKindDesktop}, 10)
	if e != nil {
		t.Fatal(e)
	}
	// The attacker has published a directory bundle (any device can).
	if _, e = db.DB().ExecContext(ctx, `INSERT INTO olm_directory_state(device_id,identity,signing,generation,key_highwater,last_bundle) VALUES($1,'i','s',1,0,'\x00')`, ad.DeviceID); e != nil {
		t.Fatal(e)
	}
	if _, e = db.DB().ExecContext(ctx, `INSERT INTO olm_directory_state(device_id,identity,signing,generation,key_highwater,last_bundle) VALUES($1,'v','s2',1,0,'\x00')`, vd.DeviceID); e != nil {
		t.Fatal(e)
	}

	victim, victimID := dialAuthed(t, env.addr, "user-uid")
	defer victim.Close()
	attacker, attackerID := dialAuthed(t, env.addr, "admin-uid")
	defer attacker.Close()
	vc, _ := env.srv.clientByID(victimID)
	vc.setDeviceID(vd.DeviceID)
	ac, _ := env.srv.clientByID(attackerID)
	ac.setDeviceID(ad.DeviceID)
	ac.mu.Lock()
	ac.authSigningKey = apub
	ac.mu.Unlock()

	part := []byte("[]")
	m := admission.Manifest{Version: admission.Version, Server: binding, Sender: "admin-uid", Device: ad.DeviceID, AccountEpoch: 1,
		AttemptID: "1", LogicalID: "x1", Purpose: "chat", Scope: "direct", Peer: "user-uid", Deadline: time.Now().Unix() + 600,
		Rosters: []admission.Roster{{Principal: "admin-uid", Version: 999}, {Principal: "user-uid", Version: 999}},
		Targets: []admission.Target{{Principal: "admin-uid", Device: ad.DeviceID, AccountEpoch: 1, Identity: "i", Local: true},
			{Principal: "user-uid", Device: vd.DeviceID, AccountEpoch: 1, Identity: "v"}},
		Parts: []admission.Part{{Digest: admission.Digest(part), Length: len(part)}}}
	if e = m.Validate(); e != nil {
		t.Fatalf("manifest: %v", e)
	}
	call := func(r netproto.OlmAdmissionRequest) netproto.OlmAdmissionResult {
		r.RequestID = "r"
		r.AccountEpoch = 1
		send(t, attacker, netproto.MsgOlmAdmissionRequest, r)
		f := readOfType(t, attacker, netproto.MsgOlmAdmissionResult)
		var out netproto.OlmAdmissionResult
		_ = json.Unmarshal(f.Payload, &out)
		return out
	}
	if r := call(netproto.OlmAdmissionRequest{Action: "begin", Manifest: &m}); r.Error != "" {
		t.Fatalf("begin: %+v", r)
	}
	const finals = 20
	for i := 0; i < finals; i++ {
		r := call(netproto.OlmAdmissionRequest{Action: "finalize", AttemptID: "1"})
		if i == 0 {
			b, _ := json.Marshal(r)
			t.Logf("finalize result: %s", b)
		}
	}
	rings := countDoorbellsIntegration(victim, time.Second)
	t.Logf("victim doorbells after %d finalizes of one rejected attempt: %d", finals, rings)
	if rings > 0 {
		t.Errorf("a rejected attempt rang the victim %d times", rings)
	}
	// Burst: fire 300 finalizes of the same rejected attempt, then count
	// what reaches the victim (session budget + per-device admission budget).
	start := time.Now()
	for i := 0; i < 300; i++ {
		send(t, attacker, netproto.MsgOlmAdmissionRequest, netproto.OlmAdmissionRequest{RequestID: "b", AccountEpoch: 1, Action: "finalize", AttemptID: "1"})
	}
	n := countDoorbellsIntegration(victim, 2*time.Second)
	t.Logf("burst: 300 finalizes sent in %v; victim doorbells %d", time.Since(start).Round(time.Millisecond), n)
	if n != 0 {
		t.Fatalf("burst of rejected finalizes rang the victim %d times", n)
	}
	// The victim's own admission is unaffected (not rate-limited).
	_ = victim.SetReadDeadline(time.Time{})
	send(t, victim, netproto.MsgOlmAdmissionRequest, netproto.OlmAdmissionRequest{RequestID: "v", AccountEpoch: 1, Action: "deliveries"})
	_ = victim.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		f, err := netproto.ReadFrame(victim)
		if err != nil {
			t.Fatalf("victim got no admission reply: %v", err)
		}
		if f.Type == uint16(netproto.MsgOlmAdmissionResult) || f.Type == uint16(netproto.MsgError) {
			if strings.Contains(string(f.Payload), "rate-limit") || strings.Contains(string(f.Payload), "rate limit") {
				t.Fatalf("victim rate-limited after the burst: %s", f.Payload)
			}
			break
		}
	}
	// Review 2a-fix2 P3-6: an invisible sender's control begin gets the
	// distinct non-retryable code "invisible", not "rate-limit".
	env.state.SetStatus(attackerID, "invisible", "")
	control := m
	control.AttemptID, control.LogicalID, control.Purpose = "2", "x2", "control"
	send(t, attacker, netproto.MsgOlmAdmissionRequest, netproto.OlmAdmissionRequest{RequestID: "inv", AccountEpoch: 1, Action: "begin", Manifest: &control})
	_ = attacker.SetReadDeadline(time.Time{})
	for {
		var r netproto.OlmAdmissionResult
		_ = json.Unmarshal(readOfType(t, attacker, netproto.MsgOlmAdmissionResult).Payload, &r)
		if r.RequestID != "inv" {
			continue // replies to the earlier burst
		}
		if r.Error != "invisible" {
			t.Fatalf("invisible control begin: %+v", r)
		}
		break
	}
}

func countDoorbellsIntegration(c net.Conn, d time.Duration) int {
	n := 0
	_ = c.SetReadDeadline(time.Now().Add(d))
	for {
		f, err := netproto.ReadFrame(c)
		if err != nil {
			return n
		}
		if f.Type == uint16(netproto.MsgEvent) && string(f.Payload) == `{"type":"olm_pending","data":{}}` {
			n++
		}
	}
}
