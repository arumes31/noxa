package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"noxa/internal/e2ee/admission"
	"noxa/internal/e2ee/ratchet"
)

// sendFixture admits the fixture's single alice -> bob copy.
func sendFixture(t *testing.T, s *Store, a OlmSource, m admission.Manifest, parts [][]byte) (admission.Receipt, error) {
	t.Helper()
	if _, err := s.BeginOlmAttempt(t.Context(), a, m); err != nil {
		t.Fatal(err)
	}
	for i, p := range parts {
		if err := s.PutOlmPart(t.Context(), a, m.AttemptID, i, p); err != nil {
			t.Fatal(err)
		}
	}
	return s.FinalizeOlmAttempt(t.Context(), a, m.AttemptID)
}

// seedPending inserts n pending 32 KiB chat copies from sender to
// principal/device (never acknowledged), as a fill attack would leave them.
func seedPending(t *testing.T, s *Store, tag, sender, principal, device string, n int) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO olm_messages(id,server,sender,device_id,account_epoch,logical_id,manifest)
		SELECT $1||'-'||g,'seed',$2,'seed-device',1,$1||'-'||g,'{}' FROM generate_series(1,$3) g`, tag, sender, n); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO olm_deliveries(id,message_id,principal,device_id,account_epoch,copy,digest,sender)
		SELECT $1||'-'||g,$1||'-'||g,$2,$3,1,decode(repeat('00',32768),'hex'),'digest-'||g,$4 FROM generate_series(1,$5) g`, tag, principal, device, sender, n); err != nil {
		t.Fatal(err)
	}
}

func pendingBytes(t *testing.T, s *Store, where string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := s.db.QueryRowContext(context.Background(), `SELECT COALESCE(sum(octet_length(copy)),0) FROM olm_deliveries WHERE state='pending' AND `+where, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// scaleBudgets lowers the pending budgets for one test.
func scaleBudgets(t *testing.T, global, account, sender int64) {
	t.Helper()
	g, a, s := OlmPendingGlobalBytes, OlmPendingAccountBytes, OlmPendingSenderBytes
	sc, ac := OlmPendingSenderControlBytes, OlmPendingAccountControlBytes
	OlmPendingGlobalBytes, OlmPendingAccountBytes, OlmPendingSenderBytes = global, account, sender
	t.Cleanup(func() {
		OlmPendingGlobalBytes, OlmPendingAccountBytes, OlmPendingSenderBytes = g, a, s
		OlmPendingSenderControlBytes, OlmPendingAccountControlBytes = sc, ac
	})
}

// Review P2-1 repro, now inverted: every way a device ends (revocation,
// sign-out, admin reset, idle expiry) closes its pending copies in the same
// transaction. The digest stays; the payload and the budget charge go.
func TestOlmEndedDevicePendingIsReclaimed(t *testing.T) {
	end := map[string]func(*Store, OlmSource) error{
		"revoke": func(s *Store, b OlmSource) error {
			_, err := s.RevokeDevice(t.Context(), b.Principal, b.Device, "user")
			return err
		},
		"sign-out": func(s *Store, b OlmSource) error {
			_, err := s.SignOutDevice(t.Context(), b.Principal, b.Device)
			return err
		},
		"reset": func(s *Store, b OlmSource) error {
			_, err := s.ResetDevices(t.Context(), b.Principal, "", DeviceRevokedByAdminReset)
			return err
		},
		"idle": func(s *Store, b OlmSource) error {
			if _, err := s.db.ExecContext(t.Context(), `UPDATE user_devices SET first_device=false, last_seen_at=now()-interval '400 days' WHERE device_id=$1`, b.Device); err != nil {
				return err
			}
			_, revoked, err := s.ExpireIdleDevices(t.Context(), time.Now().Add(-300*24*time.Hour), time.Now().Add(-365*24*time.Hour))
			if err == nil && countPrincipal(revoked, b.Principal) != 1 {
				err = fmt.Errorf("idle device not revoked: %+v", revoked)
			}
			return err
		},
	}
	for name, ending := range end {
		t.Run(name, func(t *testing.T) {
			s, a, b, m, parts := admissionFixture(t)
			if r, err := sendFixture(t, s, a, m, parts); err != nil || r.State != "accepted" {
				t.Fatal(r, err)
			}
			if pendingBytes(t, s, "device_id=$1", b.Device) == 0 {
				t.Fatal("copy not pending before the device ended")
			}
			if err := ending(s, b); err != nil {
				t.Fatal(err)
			}
			var state, digest string
			var size int
			if err := s.db.QueryRowContext(t.Context(), `SELECT state,digest,octet_length(copy) FROM olm_deliveries WHERE device_id=$1`, b.Device).Scan(&state, &digest, &size); err != nil {
				t.Fatal(err)
			}
			if state != "received" || size != 0 || digest == "" {
				t.Fatalf("ended device copy: state=%s size=%d digest=%q", state, size, digest)
			}
			if g := pendingBytes(t, s, "true"); g != 0 {
				t.Fatalf("global pending budget still charged %d bytes", g)
			}
		})
	}
}

// Copies to a device that was revoked before this fix, to a deleted device, or
// to an account epoch a reset replaced more than the grace period ago are
// closed by maintenance; a recently replaced epoch stays drainable.
func TestOlmAbandonedDeliveriesSweep(t *testing.T) {
	s, a, b, m, parts := admissionFixture(t)
	if r, err := sendFixture(t, s, a, m, parts); err != nil || r.State != "accepted" {
		t.Fatal(r, err)
	}
	ctx := t.Context()
	// A reset replaced bob's epoch 1 one day ago: still within the grace.
	if _, err := s.db.ExecContext(ctx, `INSERT INTO olm_account_history(device_id,account_epoch,identity,signing,key_highwater,retired_at) VALUES($1,1,'i','s',0,now()-interval '1 day')`, b.Device); err != nil {
		t.Fatal(err)
	}
	seedPending(t, s, "gone", "carol", "dave", "deleted-device", 2)
	if n, err := s.ExpireAbandonedOlmDeliveries(ctx); err != nil || n != 2 {
		t.Fatalf("sweep closed %d (want the 2 copies of the deleted device): %v", n, err)
	}
	if pendingBytes(t, s, "device_id=$1", b.Device) == 0 {
		t.Fatal("a recently replaced epoch must stay drainable")
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE olm_account_history SET retired_at=now()-$2::interval WHERE device_id=$1`, b.Device, fmt.Sprintf("%d seconds", int(OlmAbandonedEpochGrace.Seconds())+60)); err != nil {
		t.Fatal(err)
	}
	if n, err := s.ExpireAbandonedOlmDeliveries(ctx); err != nil || n != 1 {
		t.Fatalf("abandoned epoch sweep closed %d: %v", n, err)
	}
	if g := pendingBytes(t, s, "true"); g != 0 {
		t.Fatalf("pending after sweep: %d", g)
	}
	var size int
	if err := s.db.QueryRowContext(ctx, `SELECT octet_length(copy) FROM olm_deliveries WHERE device_id=$1`, b.Device).Scan(&size); err != nil || size != 0 {
		t.Fatal(size, err)
	}
}

// The self-send / never-ACK fill attack is bounded per account. The attacker
// (alice) leaves copies to her own devices unread: once her outstanding chat
// reaches the sender cap her sends are refused, the server-wide budget stays
// far below its limit for everyone else, and signing her devices out frees it.
func TestOlmPendingFillAttackIsBoundedPerAccount(t *testing.T) {
	scaleBudgets(t, 64<<20, 1<<20, 1<<20)
	s, a, b, m, parts := admissionFixture(t)
	seedPending(t, s, "self", a.Principal, a.Principal, a.Device, 33) // 1056 KiB > 1 MiB
	if _, err := sendFixture(t, s, a, m, parts); !errors.Is(err, ErrOlmLimit) {
		t.Fatalf("sender over its outstanding cap must be refused: %v", err)
	}
	if g := pendingBytes(t, s, "true"); g > OlmPendingGlobalBytes/32 {
		t.Fatalf("one account holds %d of the %d global budget", g, OlmPendingGlobalBytes)
	}
	if _, err := s.SignOutDevice(t.Context(), a.Principal, a.Device); err != nil {
		t.Fatal(err)
	}
	if g := pendingBytes(t, s, "sender=$1", a.Principal); g != 0 {
		t.Fatalf("sign-out left %d attacker bytes charged", g)
	}
	_ = b
}

// A recipient account that never reads is capped across all of its devices.
func TestOlmPendingRecipientAccountCap(t *testing.T) {
	scaleBudgets(t, 64<<20, 1<<20, 64<<20)
	s, a, b, m, parts := admissionFixture(t)
	// Copies from many other senders, to another (unread) device of bob.
	for i := 0; i < 31; i++ {
		seedPending(t, s, fmt.Sprintf("to-bob-%d", i), fmt.Sprintf("sybil-%d", i), b.Principal, "bob-other-device", 1)
	}
	if _, err := sendFixture(t, s, a, m, parts); !errors.Is(err, ErrOlmLimit) {
		t.Fatalf("recipient account over its cap must refuse new chat: %v", err)
	}
}

// Chat stops short of the global limit by the control reserve, so a chat
// backlog never blocks media key distribution.
func TestOlmPendingControlReserve(t *testing.T) {
	if got := pendingLimit(1<<20, true); got != 1<<20 {
		t.Fatalf("control limit %d", got)
	}
	if got := pendingLimit(1<<20, false); got != (1<<20)-(1<<16) {
		t.Fatalf("chat limit %d", got)
	}
	scaleBudgets(t, 1<<20, 64<<20, 64<<20)
	s, a, _, m, parts := admissionFixture(t)
	// 30 x 32 KiB = 960 KiB: below the full limit, above the chat share.
	for i := 0; i < 30; i++ {
		seedPending(t, s, fmt.Sprintf("backlog-%d", i), fmt.Sprintf("sender-%d", i), fmt.Sprintf("reader-%d", i), fmt.Sprintf("dev-%d", i), 1)
	}
	if _, err := sendFixture(t, s, a, m, parts); !errors.Is(err, ErrOlmLimit) {
		t.Fatalf("chat inside the control reserve must be refused: %v", err)
	}
}

// The near-capacity warning is rate limited and names the limit.
func TestOlmPendingWarningRateLimited(t *testing.T) {
	core, logs := observer.New(zap.WarnLevel)
	s := &Store{logger: zap.New(core)}
	s.notePendingUsage(OlmPendingGlobalBytes)
	s.notePendingUsage(OlmPendingGlobalBytes)
	s.notePendingRefusal("global", OlmPendingGlobalBytes)
	if logs.Len() != 1 || !strings.Contains(logs.All()[0].Message, "nearly exhausted") {
		t.Fatalf("warnings: %+v", logs.All())
	}
	s.notePendingUsage(OlmPendingGlobalBytes / 2)
	if logs.Len() != 1 {
		t.Fatal("warned below the threshold")
	}
}

// Idle expiry locks accounts in bounded batches and still expires them all.
func TestExpireIdleDevicesBatches(t *testing.T) {
	prev := idleExpiryBatch
	idleExpiryBatch = 1
	t.Cleanup(func() { idleExpiryBatch = prev })
	s := pollTestStore(t)
	ctx := t.Context()
	for i := 0; i < 3; i++ {
		p := fmt.Sprintf("batch-principal-%d-%d", i, time.Now().UnixNano())
		for j := 0; j < 2; j++ {
			if _, err := s.AddDevice(ctx, newTestDevice(p, j), 10); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE user_devices SET last_seen_at=now()-interval '400 days' WHERE principal LIKE 'batch-principal-%'`); err != nil {
		t.Fatal(err)
	}
	_, revoked, err := s.ExpireIdleDevices(ctx, time.Now().Add(-300*24*time.Hour), time.Now().Add(-365*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, d := range revoked {
		if strings.HasPrefix(d.Principal, "batch-principal-") {
			n++
		}
	}
	if n != 3 { // the first device of each account never expires
		t.Fatalf("revoked %d idle devices across batches, want 3", n)
	}
}

// Review P3-1: the drain ends its transaction before the network write, so the
// callback (a slow or stalled client) holds no row lock: the recipient's own
// roster can be changed while the page is still being written.
func TestOlmDeliveriesHoldNoLocksDuringWrite(t *testing.T) {
	s, a, b, m, parts := admissionFixture(t)
	if r, err := sendFixture(t, s, a, m, parts); err != nil || r.State != "accepted" {
		t.Fatal(r, err)
	}
	delivered := 0
	err := s.WithOlmDeliveries(t.Context(), b, "", func(p OlmDeliveryPage) error {
		delivered = len(p.Deliveries)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		// Takes the roster and device row locks the drain used to hold.
		_, err := s.SignOutDevice(ctx, b.Principal, b.Device)
		return err
	})
	if err != nil || delivered != 1 {
		t.Fatalf("delivered %d, lock still held during the write: %v", delivered, err)
	}
}

// controlWithEnvelope admits one control to target whose copy envelope is n
// bytes (opaque to the server, like any control body).
func controlWithEnvelope(t *testing.T, s *Store, a OlmSource, snap OlmRosterSnapshot, target OlmSource, attempt string, n int, deadline int64) (admission.Receipt, error) {
	t.Helper()
	var targets []admission.Target
	for _, tg := range snap.Targets {
		if tg.Local || tg.Device == target.Device {
			targets = append(targets, tg)
		}
	}
	m := admission.Manifest{Version: admission.Version, Server: a.Server, Sender: a.Principal, Device: a.Device, AccountEpoch: 1, AttemptID: attempt, LogicalID: "ctl-" + attempt,
		Purpose: "control", Scope: "direct", Peer: target.Principal, Deadline: deadline, Rosters: snap.Rosters, Targets: targets}
	build := func(body int) []byte {
		env, _ := json.Marshal(ratchet.Envelope{Version: ratchet.Version, SessionID: "session",
			Binding: admission.ExpectedBinding(m, admission.Target{Principal: target.Principal, Device: target.Device, AccountEpoch: target.AccountEpoch}),
			Message: json.RawMessage(`{"type":0,"body":"` + strings.Repeat("A", body) + `"}`)})
		return env
	}
	envelope := build(n - len(build(0)))
	parts, err := admission.Freeze(&m, []admission.Copy{{Principal: target.Principal, Device: target.Device, AccountEpoch: 1, SessionID: "session", Envelope: envelope}})
	if err != nil {
		return admission.Receipt{}, err
	}
	if _, err = s.BeginOlmAttempt(t.Context(), a, m); err != nil {
		return admission.Receipt{}, err
	}
	for i, p := range parts {
		if err = s.PutOlmPart(t.Context(), a, m.AttemptID, i, p); err != nil {
			t.Fatal(err)
		}
	}
	return s.FinalizeOlmAttempt(t.Context(), a, m.AttemptID)
}

// Task 4 fix review P2-A (repro adopted): a "control" label no longer escapes
// the per-account budgets. A control copy is capped at
// MaxControlEnvelopeBytes, and controls are charged to their own per-sender
// and per-recipient account budgets, so a sender over its chat cap, sending to
// a recipient over its account cap, gets only that small control budget.
func TestOlmControlLabelCannotBypassAccountCaps(t *testing.T) {
	scaleBudgets(t, 64<<20, 1<<20, 1<<20)
	OlmPendingSenderControlBytes, OlmPendingAccountControlBytes = 64<<10, 64<<10
	s, a, b, m, parts := admissionFixture(t)
	if r, err := sendFixture(t, s, a, m, parts); err != nil || r.State != "accepted" {
		t.Fatal(r, err)
	}
	seedPending(t, s, "self", a.Principal, a.Principal, a.Device, 33)
	seedPending(t, s, "tobob", "other", b.Principal, "bob-other-dev", 33)
	snap, err := s.OlmSnapshot(t.Context(), a, "direct", "bob", "")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Unix() + 120
	// A chat-sized body labelled control is refused outright.
	if _, err := controlWithEnvelope(t, s, a, snap, b, "100", 22<<10, deadline); !errors.Is(err, admission.ErrInvalid) {
		t.Fatalf("22 KiB control envelope: %v", err)
	}
	before := pendingBytes(t, s, "sender=$1", a.Principal)
	accepted := 0
	for i := 0; i < admission.MaxPendingControls; i++ {
		r, err := controlWithEnvelope(t, s, a, snap, b, strconv.Itoa(200+i), admission.MaxControlEnvelopeBytes, deadline)
		if err != nil || r.State != "accepted" {
			if !errors.Is(err, ErrOlmLimit) {
				t.Fatalf("control %d: %+v %v", i, r, err)
			}
			break
		}
		accepted++
	}
	extra := pendingBytes(t, s, "sender=$1", a.Principal) - before
	if extra > OlmPendingSenderControlBytes || accepted >= admission.MaxPendingControls {
		t.Fatalf("controls accepted past the control budget: %d (%d bytes)", accepted, extra)
	}
	t.Logf("controls admitted within the %d-byte control budget: %d (%d bytes)", OlmPendingSenderControlBytes, accepted, extra)
}

// The control budgets leave room for real rekeys at the largest encrypted
// channel: several outstanding full rekeys (63 recipients) even if every copy
// were a maximum-size control, and far more with real media-key controls.
func TestOlmControlBudgetsFitMaxChannelRekeys(t *testing.T) {
	const recipients = 63 // netproto.MediaE2EEMaxVoice - 1
	fullRekey := int64(recipients * admission.MaxControlEnvelopeBytes)
	if OlmPendingSenderControlBytes < 4*fullRekey/3 || OlmPendingAccountControlBytes < 4*fullRekey/3 {
		t.Fatalf("control budgets %d/%d below a worst-case rekey of %d", OlmPendingSenderControlBytes, OlmPendingAccountControlBytes, fullRekey)
	}
}
