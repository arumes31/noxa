package store

import (
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"noxa/internal/e2ee/olmdir"
)

func olmFixture(t *testing.T, s *Store, principal string) (Device, ed25519.PrivateKey, olmdir.Bundle) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	pubText := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub}))
	d, e := s.AddDevice(t.Context(), NewDevice{Principal: principal, Kind: DeviceKindDesktop, Ed25519Public: pubText}, 24)
	if e != nil {
		t.Fatal(e)
	}
	curve := func() string {
		k, _ := ecdh.X25519().GenerateKey(rand.Reader)
		return base64.RawStdEncoding.EncodeToString(k.PublicKey().Bytes())
	}
	b := olmdir.Bundle{Version: olmdir.Version, Protocol: olmdir.Protocol, Server: "tls-sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Principal: principal, DeviceID: d.DeviceID, AccountEpoch: 1, Generation: 1, IssuedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(time.Hour).Unix(), Identity: curve(), Signing: base64.RawStdEncoding.EncodeToString(pub)}
	for i := 1; i <= 10; i++ {
		var id [8]byte
		binary.BigEndian.PutUint64(id[:], uint64(i))
		b.OneTimeKeys = append(b.OneTimeKeys, olmdir.Key{ID: base64.RawStdEncoding.EncodeToString(id[:]), Key: curve()})
	}
	signOlm(t, &b, priv)
	return d, priv, b
}
func signOlm(t *testing.T, b *olmdir.Bundle, k ed25519.PrivateKey) {
	t.Helper()
	tr, e := b.Transcript()
	if e != nil {
		t.Fatal(e)
	}
	b.Signature = ed25519.Sign(k, tr)
}
func TestOlmDirectoryAuthorityAndSpentKeys(t *testing.T) {
	if os.Getenv("NOXA_TEST_DATABASE_URL") == "" {
		t.Fatal("NOXA_TEST_DATABASE_URL required; real PostgreSQL tests must not skip")
	}
	s := pollTestStore(t)
	ctx := t.Context()
	d, k, b := olmFixture(t, s, "recipient")
	a, _, _ := olmFixture(t, s, "sender")
	if e := s.PublishOlmBundle(ctx, "attacker", d.DeviceID, b.Server, b); !errors.Is(e, ErrOlmAuthority) {
		t.Fatalf("wrong principal: %v", e)
	}
	forged := b
	forged.Signature = make([]byte, 64)
	if e := s.PublishOlmBundle(ctx, d.Principal, d.DeviceID, b.Server, forged); e == nil {
		t.Fatal("self/supplied signature accepted")
	}
	if e := s.PublishOlmBundle(ctx, d.Principal, d.DeviceID, b.Server, b); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	out := make(chan OlmClaim, 12)
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, e := s.ClaimOlmKey(ctx, a.Principal, a.DeviceID, d.Principal, d.DeviceID, "same-request", b.ExpiresAt, b.Server)
			out <- c
			errs <- e
		}()
	}
	wg.Wait()
	close(out)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	id := ""
	for c := range out {
		if id != "" && c.KeyID != id {
			t.Fatal("retry consumed another key")
		}
		id = c.KeyID
		if e := c.Bundle.Verify(k.Public().(ed25519.PublicKey), b.Server, d.Principal, d.DeviceID, time.Now()); e != nil {
			t.Fatal(e)
		}
	}
	if e := s.PublishOlmBundle(ctx, d.Principal, d.DeviceID, b.Server, b); e != nil {
		t.Fatal("exact retry", e)
	}
	var available int
	_ = s.db.QueryRowContext(context.Background(), `SELECT count(*) FROM olm_directory_keys WHERE claimed_by IS NULL`).Scan(&available)
	if available != 9 {
		t.Fatalf("spent key resurrected: %d", available)
	}
	conflict := b
	conflict.ExpiresAt++
	signOlm(t, &conflict, k)
	if e := s.PublishOlmBundle(ctx, d.Principal, d.DeviceID, b.Server, conflict); !errors.Is(e, ErrOlmConflict) {
		t.Fatalf("conflict %v", e)
	}
	b.Generation++
	signOlm(t, &b, k)
	if e := s.PublishOlmBundle(ctx, d.Principal, d.DeviceID, b.Server, b); !errors.Is(e, ErrOlmConflict) {
		t.Fatalf("old IDs recycled %v", e)
	}
	if _, e := s.RevokeDevice(ctx, d.Principal, d.DeviceID, DeviceRevokedByUser); e != nil {
		t.Fatal(e)
	}
	if _, e := s.ClaimOlmKey(ctx, a.Principal, a.DeviceID, d.Principal, d.DeviceID, "same-request", b.ExpiresAt, b.Server); !errors.Is(e, ErrOlmAuthority) {
		t.Fatalf("revoked retry %v", e)
	}
	if e := s.PublishOlmBundle(ctx, d.Principal, d.DeviceID, b.Server, b); !errors.Is(e, ErrOlmAuthority) {
		t.Fatalf("revoked publish %v", e)
	}
}

func freshOlmKeys(t *testing.T, b *olmdir.Bundle, start, count int) {
	t.Helper()
	b.OneTimeKeys = nil
	for i := start; i < start+count; i++ {
		var id [8]byte
		binary.BigEndian.PutUint64(id[:], uint64(i))
		k, e := ecdh.X25519().GenerateKey(rand.Reader)
		if e != nil {
			t.Fatal(e)
		}
		b.OneTimeKeys = append(b.OneTimeKeys, olmdir.Key{ID: base64.RawStdEncoding.EncodeToString(id[:]), Key: base64.RawStdEncoding.EncodeToString(k.PublicKey().Bytes())})
	}
}
func TestOlmDirectoryConcurrentClaimsAndReplenishment(t *testing.T) {
	if os.Getenv("NOXA_TEST_DATABASE_URL") == "" {
		t.Fatal("real PostgreSQL URL required")
	}
	s := pollTestStore(t)
	ctx := t.Context()
	d, secret, b := olmFixture(t, s, "recipient")
	a, _, _ := olmFixture(t, s, "sender")
	if e := s.PublishOlmBundle(ctx, d.Principal, d.DeviceID, b.Server, b); e != nil {
		t.Fatal(e)
	}
	type result struct {
		claim   OlmClaim
		err     error
		request string
	}
	var wg sync.WaitGroup
	out := make(chan result, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			r := fmt.Sprint(n)
			c, e := s.ClaimOlmKey(ctx, a.Principal, a.DeviceID, d.Principal, d.DeviceID, r, b.ExpiresAt, b.Server)
			out <- result{c, e, r}
		}(i)
	}
	wg.Wait()
	close(out)
	seen := map[string]bool{}
	retry := ""
	limited := 0
	for r := range out {
		if errors.Is(r.err, ErrOlmLimit) {
			limited++
			continue
		}
		if r.err != nil {
			t.Fatal(r.err)
		}
		if seen[r.claim.KeyID] {
			t.Fatal("double claim")
		}
		seen[r.claim.KeyID] = true
		retry = r.request
	}
	if len(seen) != 2 || limited != 8 {
		t.Fatalf("new request IDs bypassed per-device quota: claims=%d limited=%d", len(seen), limited)
	}
	for i := 0; i < 8; i++ {
		sender, _, _ := olmFixture(t, s, fmt.Sprint("another-sender-", i))
		if _, e := s.ClaimOlmKey(ctx, sender.Principal, sender.DeviceID, d.Principal, d.DeviceID, "claim", b.ExpiresAt, b.Server); e != nil {
			t.Fatal(e)
		}
	}
	fresh, _, _ := olmFixture(t, s, "fresh")
	if _, e := s.ClaimOlmKey(ctx, fresh.Principal, fresh.DeviceID, d.Principal, d.DeviceID, "exhaust", b.ExpiresAt, b.Server); !errors.Is(e, ErrOlmExhausted) {
		t.Fatalf("empty pool: %v", e)
	}
	next := b
	next.Generation = 2
	freshOlmKeys(t, &next, 11, 30)
	signOlm(t, &next, secret)
	changed := next
	changed.Identity = next.OneTimeKeys[0].Key
	changed.OneTimeKeys = changed.OneTimeKeys[1:]
	signOlm(t, &changed, secret)
	if e := s.PublishOlmBundle(ctx, d.Principal, d.DeviceID, b.Server, changed); !errors.Is(e, ErrOlmAccountChanged) {
		t.Fatalf("account replaced: %v", e)
	}
	if e := s.PublishOlmBundle(ctx, d.Principal, d.DeviceID, b.Server, next); e != nil {
		t.Fatal("replenish", e)
	}
	if e := s.PublishOlmBundle(ctx, d.Principal, d.DeviceID, b.Server, b); e != nil {
		t.Fatal("older exact retry", e)
	}
	if _, e := s.ClaimOlmKey(ctx, a.Principal, a.DeviceID, d.Principal, d.DeviceID, retry, b.ExpiresAt, b.Server); e != nil {
		t.Fatal("idempotent retry after quota", e)
	}
	if _, e := s.ClaimOlmKey(ctx, a.Principal, a.DeviceID, d.Principal, d.DeviceID, retry, b.ExpiresAt-1, b.Server); !errors.Is(e, ErrOlmConflict) {
		t.Fatalf("changed retry lifetime: %v", e)
	}
	pub, _, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	other, e := s.AddDevice(ctx, NewDevice{Principal: a.Principal, Kind: DeviceKindWeb, Ed25519Public: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub})), ApprovedBy: a.DeviceID, ApprovalSignature: make([]byte, 64)}, 24)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := s.ClaimOlmKey(ctx, a.Principal, other.DeviceID, d.Principal, d.DeviceID, retry, b.ExpiresAt, b.Server); !errors.Is(e, ErrOlmConflict) {
		t.Fatalf("cross-device retry: %v", e)
	}
	// The retained, monotonic watermark rejects reuse even after payload cleanup.
	if _, e = s.db.ExecContext(context.Background(), `UPDATE olm_directory_publications SET expires_at=now()-interval '25 hours'`); e != nil {
		t.Fatal(e)
	}
	if n, e := s.ExpireOlmDirectory(ctx); e != nil || n != 2 {
		t.Fatalf("cleanup %d %v", n, e)
	}
	if _, e := s.ClaimOlmKey(ctx, a.Principal, a.DeviceID, d.Principal, d.DeviceID, "0", time.Now().Unix()-1, b.Server); !errors.Is(e, ErrOlmExhausted) {
		t.Fatal("expired exact request reused", e)
	}
	next.Generation = 3
	signOlm(t, &next, secret)
	if e = s.PublishOlmBundle(ctx, d.Principal, d.DeviceID, b.Server, next); !errors.Is(e, ErrOlmConflict) {
		t.Fatal("watermark erased", e)
	}
	// Public cleanup retains all private-key obligations. Explicit closure,
	// serialized with admission, is required before their capacity can be reused.
	if _, e = s.db.ExecContext(context.Background(), `UPDATE olm_directory_keys SET claim_until=1 WHERE device_id=$1 AND lifecycle='reserved'`, d.DeviceID); e != nil {
		t.Fatal(e)
	}
	rows, e := s.db.QueryContext(context.Background(), `SELECT key_id FROM olm_directory_keys WHERE device_id=$1`, d.DeviceID)
	if e != nil {
		t.Fatal(e)
	}
	var ids []string
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			t.Fatal(e)
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	closed, e := s.CloseOlmKeys(ctx, OlmSource{Server: b.Server, Principal: d.Principal, Device: d.DeviceID, Signing: d.Ed25519Public, AccountEpoch: 1}, ids)
	if e != nil {
		t.Fatal(e)
	}
	for _, c := range closed {
		if c.State != "closed" {
			t.Fatal("unresolved key", c.State)
		}
	}
	freshOlmKeys(t, &next, 41, 100)
	signOlm(t, &next, secret)
	if e = s.PublishOlmBundle(ctx, d.Principal, d.DeviceID, b.Server, next); e != nil {
		t.Fatal(e)
	}
	next.Generation++
	freshOlmKeys(t, &next, 141, 1)
	signOlm(t, &next, secret)
	if e = s.PublishOlmBundle(ctx, d.Principal, d.DeviceID, b.Server, next); !errors.Is(e, ErrOlmLimit) {
		t.Fatal("capacity exceeded", e)
	}
}
func TestOlmDirectoryExpiryAndRevokeRace(t *testing.T) {
	if os.Getenv("NOXA_TEST_DATABASE_URL") == "" {
		t.Fatal("real PostgreSQL URL required")
	}
	s := pollTestStore(t)
	ctx := t.Context()
	d, k, b := olmFixture(t, s, "recipient")
	a, _, _ := olmFixture(t, s, "sender")
	if e := s.PublishOlmBundle(ctx, d.Principal, d.DeviceID, b.Server, b); e != nil {
		t.Fatal(e)
	}
	if _, e := s.db.ExecContext(context.Background(), `UPDATE olm_directory_publications SET expires_at=now()-interval '1 second'`); e != nil {
		t.Fatal(e)
	}
	if _, e := s.ClaimOlmKey(ctx, a.Principal, a.DeviceID, d.Principal, d.DeviceID, "expired", b.ExpiresAt, b.Server); !errors.Is(e, ErrOlmExhausted) {
		t.Fatal("expired key listed", e)
	}
	s.db.SetMaxOpenConns(6)
	// The revoking transaction holds the SAME device row lock as publication and claim.
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = tx.Rollback() }()
	if _, e = tx.ExecContext(context.Background(), `UPDATE user_devices SET revoked_at=now() WHERE device_id=$1`, d.DeviceID); e != nil {
		t.Fatal(e)
	}
	b.Generation++
	freshOlmKeys(t, &b, 11, 1)
	signOlm(t, &b, k)
	results := make(chan error, 2)
	go func() { results <- s.PublishOlmBundle(ctx, d.Principal, d.DeviceID, b.Server, b) }()
	go func() {
		_, e := s.ClaimOlmKey(ctx, a.Principal, a.DeviceID, d.Principal, d.DeviceID, "race", b.ExpiresAt, b.Server)
		results <- e
	}()
	// Observe actual PostgreSQL lock contention before releasing revocation.
	deadline := time.Now().Add(2 * time.Second)
	for {
		var waiting int
		if e = s.db.QueryRowContext(context.Background(), `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%FROM user_devices WHERE principal=%FOR UPDATE'`).Scan(&waiting); e != nil {
			t.Fatal(e)
		}
		if waiting > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("claim/publication never waited on the revoking row lock")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 2; i++ {
		if e := <-results; !errors.Is(e, ErrOlmAuthority) {
			t.Fatalf("post-revoke work: %v", e)
		}
	}
	// Sweep work stays bounded even with a large expired historical backlog.
	if _, e = s.db.ExecContext(context.Background(), `INSERT INTO olm_directory_publications(device_id,generation,bundle,expires_at,server) SELECT $1,n,'{}'::bytea,now()-interval '25 hours',$2 FROM generate_series(100,356) n`, d.DeviceID, b.Server); e != nil {
		t.Fatal(e)
	}
	if n, e := s.ExpireOlmDirectory(ctx); e != nil || n != 256 {
		t.Fatalf("first bounded sweep: %d %v", n, e)
	}
	if n, e := s.ExpireOlmDirectory(ctx); e != nil || n != 1 {
		t.Fatalf("second bounded sweep: %d %v", n, e)
	}
}

func TestOlmDirectoryCrossedClaimsAndQuotaScope(t *testing.T) {
	if os.Getenv("NOXA_TEST_DATABASE_URL") == "" {
		t.Fatal("real PostgreSQL URL required")
	}
	s := pollTestStore(t)
	ctx := t.Context()
	a, _, ba := olmFixture(t, s, "alice")
	b, _, bb := olmFixture(t, s, "bob")
	for _, v := range []olmdir.Bundle{ba, bb} {
		if e := s.PublishOlmBundle(ctx, v.Principal, v.DeviceID, v.Server, v); e != nil {
			t.Fatal(e)
		}
	}
	results := make(chan error, 2)
	for _, pair := range [][2]Device{{a, b}, {b, a}} {
		go func(pair [2]Device) {
			op, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			_, e := s.ClaimOlmKey(op, pair[0].Principal, pair[0].DeviceID, pair[1].Principal, pair[1].DeviceID, "crossed", ba.ExpiresAt, ba.Server)
			results <- e
		}(pair)
	}
	for i := 0; i < 2; i++ {
		if e := <-results; e != nil {
			t.Fatal("crossed claims deadlocked or failed", e)
		}
	}
	// One target's quota does not consume another conversation's per-peer budget.
	c, _, bc := olmFixture(t, s, "carol")
	if e := s.PublishOlmBundle(ctx, c.Principal, c.DeviceID, bc.Server, bc); e != nil {
		t.Fatal(e)
	}
	// Seed only synthetic consumed obligations to hit the exact peer budget.
	if _, e := s.db.ExecContext(context.Background(), `INSERT INTO olm_directory_keys(device_id,generation,key_id,claimed_by,claimed_at,claimed_device,request_id,claim_until,principal,server,bundle,account_epoch,claimed_epoch,lifecycle) SELECT $1,1,'budget-'||n,$2,now(),$3,'budget-'||n,$4,$5,$6,$7,1,1,'received' FROM generate_series(1,23)n`, b.DeviceID, a.Principal, a.DeviceID, ba.ExpiresAt, b.Principal, bb.Server, mustOlmJSON(bb)); e != nil {
		t.Fatal(e)
	}
	if _, e := s.ClaimOlmKey(ctx, a.Principal, a.DeviceID, b.Principal, b.DeviceID, "blocked-peer", ba.ExpiresAt, ba.Server); !errors.Is(e, ErrOlmLimit) {
		t.Fatal("peer quota missing", e)
	}
	if _, e := s.ClaimOlmKey(ctx, a.Principal, a.DeviceID, c.Principal, c.DeviceID, "other-peer", ba.ExpiresAt, ba.Server); e != nil {
		t.Fatal("another peer consumed this conversation quota", e)
	}
}

func TestOlmDirectoryDefaultMultiDeviceFanout(t *testing.T) {
	if os.Getenv("NOXA_TEST_DATABASE_URL") == "" {
		t.Fatal("real PostgreSQL URL required")
	}
	s := pollTestStore(t)
	ctx := t.Context()
	add := func(principal string, n int) ([]Device, []olmdir.Bundle) {
		var devices []Device
		var bundles []olmdir.Bundle
		for i := 0; i < n; i++ {
			d, key, b := olmFixture(t, s, principal)
			if !d.FirstDevice { // Simulate a completed existing noXa device endorsement.
				if _, e := s.db.ExecContext(context.Background(), `UPDATE user_devices SET approved_by=$2,approval_signature=$3 WHERE device_id=$1`, d.DeviceID, devices[0].DeviceID, make([]byte, 64)); e != nil {
					t.Fatal(e)
				}
			}
			freshOlmKeys(t, &b, 1, 10)
			signOlm(t, &b, key)
			if e := s.PublishOlmBundle(ctx, d.Principal, d.DeviceID, b.Server, b); e != nil {
				t.Fatal(e)
			}
			devices = append(devices, d)
			bundles = append(bundles, b)
		}
		return devices, bundles
	}
	senders, _ := add("sender", 10)
	recipients, bundles := add("recipient", 10)
	for i, sender := range senders {
		for j, recipient := range recipients {
			if _, e := s.ClaimOlmKey(ctx, sender.Principal, sender.DeviceID, recipient.Principal, recipient.DeviceID, fmt.Sprintf("fanout-%d-%d", i, j), bundles[j].ExpiresAt, bundles[j].Server); e != nil {
				t.Fatalf("legitimate device pair %d,%d denied: %v", i, j, e)
			}
		}
	}
}

func TestOlmDirectoryCertificateRotation(t *testing.T) {
	if os.Getenv("NOXA_TEST_DATABASE_URL") == "" {
		t.Fatal("real PostgreSQL URL required")
	}
	s := pollTestStore(t)
	ctx := t.Context()
	d, key, b := olmFixture(t, s, "recipient")
	a, _, _ := olmFixture(t, s, "sender")
	if e := s.PublishOlmBundle(ctx, d.Principal, d.DeviceID, b.Server, b); e != nil {
		t.Fatal(e)
	}
	next := b
	next.Server = "tls-sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	next.Generation++
	freshOlmKeys(t, &next, 11, 1)
	signOlm(t, &next, key)
	if e := s.PublishOlmBundle(ctx, d.Principal, d.DeviceID, next.Server, next); e != nil {
		t.Fatal(e)
	}
	c, e := s.ClaimOlmKey(ctx, a.Principal, a.DeviceID, d.Principal, d.DeviceID, "new-cert", next.ExpiresAt, next.Server)
	if e != nil || c.Bundle.Server != next.Server || c.Bundle.Generation != 2 {
		t.Fatalf("old binding shadowed new publication: %+v %v", c, e)
	}
}

func TestOlmDirectoryBulkDeviceLockOrder(t *testing.T) {
	if os.Getenv("NOXA_TEST_DATABASE_URL") == "" {
		t.Fatal("real PostgreSQL URL required")
	}
	for _, operation := range []string{"reset", "revoke-idle", "warn-idle"} {
		t.Run(operation, func(t *testing.T) {
			s := pollTestStore(t)
			s.db.SetMaxOpenConns(6)
			ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
			defer cancel()
			root, _, _ := olmFixture(t, s, "owner")
			// Physical insertion order opposes the shared device-ID lock order.
			high, highKey, bundle := olmFixture(t, s, root.Principal)
			low, _, _ := olmFixture(t, s, root.Principal)
			for _, item := range []struct {
				device *Device
				id     string
			}{
				{&high, "ffffffff-ffff-ffff-ffff-ffffffffffff"},
				{&low, "00000000-0000-0000-0000-000000000000"},
			} {
				_, err := s.db.ExecContext(ctx, `UPDATE user_devices SET device_id=$2,approved_by=$3,approval_signature=$4,last_seen_at=now()-interval '200 days' WHERE device_id=$1`, item.device.DeviceID, item.id, root.DeviceID, make([]byte, 64))
				if err != nil {
					t.Fatal(err)
				}
				item.device.DeviceID = item.id
			}
			handle, err := s.PasskeyUserHandle(ctx, root.Principal)
			if err != nil {
				t.Fatal(err)
			}
			for _, device := range []Device{root, low, high} {
				if err := s.AddPasskey(ctx, Passkey{CredentialID: []byte(device.DeviceID), Principal: device.Principal, UserHandle: handle, DeviceID: device.DeviceID, Name: "test", Credential: []byte(`{}`)}); err != nil {
					t.Fatal(err)
				}
			}
			bundle.DeviceID = high.DeviceID
			signOlm(t, &bundle, highKey)
			if err := s.PublishOlmBundle(ctx, high.Principal, high.DeviceID, bundle.Server, bundle); err != nil {
				t.Fatal(err)
			}
			blocker, err := s.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = blocker.Rollback() }()
			if _, err = blocker.ExecContext(ctx, `SELECT device_id FROM user_devices WHERE device_id=$1 FOR UPDATE`, low.DeviceID); err != nil {
				t.Fatal(err)
			}
			type bulkResult struct {
				count int
				err   error
			}
			bulk := make(chan bulkResult, 1)
			go func() {
				if operation == "reset" {
					rows, err := s.ResetDevices(ctx, root.Principal, root.DeviceID, DeviceRevokedByAdminReset)
					bulk <- bulkResult{len(rows), err}
					return
				}
				revokeBefore := time.Now().Add(-180 * 24 * time.Hour)
				if operation == "warn-idle" {
					revokeBefore = time.Now().Add(-300 * 24 * time.Hour)
				}
				warned, revoked, err := s.ExpireIdleDevices(ctx, time.Now().Add(-166*24*time.Hour), revokeBefore)
				bulk <- bulkResult{len(warned) + len(revoked), err}
			}()
			waitForLocks := func(want int) {
				t.Helper()
				deadline := time.Now().Add(2 * time.Second)
				for {
					var n int
					err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND (query LIKE '%user_devices%' OR query LIKE '%olm_rosters%') AND pid<>pg_backend_pid()`).Scan(&n)
					if err != nil {
						t.Fatal(err)
					}
					if n >= want {
						return
					}
					if time.Now().After(deadline) {
						t.Fatalf("wanted %d contending device operations, saw %d", want, n)
					}
					time.Sleep(5 * time.Millisecond)
				}
			}
			waitForLocks(1)
			// Bulk work must not hold the higher row while waiting for the lower
			// row. Otherwise a claim holding low and waiting for high can deadlock.
			probe, err := s.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = probe.ExecContext(ctx, `SELECT device_id FROM user_devices WHERE device_id=$1 FOR UPDATE NOWAIT`, high.DeviceID)
			_ = probe.Rollback()
			if err != nil {
				t.Fatalf("bulk operation acquired higher ID before lower ID: %v", err)
			}
			claim := make(chan error, 1)
			go func() {
				_, err := s.ClaimOlmKey(ctx, low.Principal, low.DeviceID, high.Principal, high.DeviceID, "bulk-race", bundle.ExpiresAt, bundle.Server)
				claim <- err
			}()
			waitForLocks(2)
			if err = blocker.Commit(); err != nil {
				t.Fatal(err)
			}
			result := <-bulk
			if result.err != nil || result.count != 2 {
				t.Fatalf("bulk update: count=%d err=%v", result.count, result.err)
			}
			var credentials int
			if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM webauthn_credentials WHERE principal=$1`, root.Principal).Scan(&credentials); err != nil {
				t.Fatal(err)
			}
			wantCredentials := 1
			if operation == "warn-idle" {
				wantCredentials = 3
			}
			if credentials != wantCredentials {
				t.Fatalf("dependent passkey cleanup: got %d, want %d", credentials, wantCredentials)
			}
			claimErr := <-claim
			if operation == "warn-idle" {
				if claimErr != nil {
					t.Fatalf("warning blocked a valid claim: %v", claimErr)
				}
			} else if !errors.Is(claimErr, ErrOlmAuthority) {
				t.Fatalf("post-revocation claim: %v", claimErr)
			}
			for _, device := range []Device{low, high} {
				got, err := s.DeviceByID(ctx, device.Principal, device.DeviceID)
				if err != nil {
					t.Fatal(err)
				}
				if operation == "warn-idle" {
					if !got.Active() || got.ExpiryWarnedAt.IsZero() {
						t.Fatalf("warning lost: %+v", got)
					}
				} else if got.Active() {
					t.Fatal("bulk revocation rolled back")
				}
			}
		})
	}
}

func mustOlmJSON(b olmdir.Bundle) []byte { v, _ := json.Marshal(b); return v }
