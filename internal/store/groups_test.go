// groups_test.go DB-backed tests for group metadata and audit storage.
// They follow the repository's skip pattern: without a reachable Postgres the
// tests skip. Set NOXA_TEST_DATABASE_URL to override the default dev URL.
package store

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// testDBStore opens the test database or skips. Migrations are applied so the
// wave-6a tables/columns exist.
func testDBStore(t *testing.T) *Store {
	t.Helper()
	configured := os.Getenv("NOXA_TEST_DATABASE_URL") != ""
	s, err := New(testDBURL(), testLogger(), 2, 1, time.Minute)
	if err != nil {
		if configured {
			t.Fatalf("opening configured database: %v", err)
		}
		t.Skipf("no database available (%v); skipping DB-backed test", err)
	}
	if err := s.Migrate(); err != nil {
		_ = s.Close()
		if configured {
			t.Fatalf("migrating configured database: %v", err)
		}
		t.Skipf("migrations failed (%v); skipping DB-backed test", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// seedTestUser inserts a throwaway user row and removes it (cascading) at
// test end.
func seedTestUser(t *testing.T, s *Store, suffix string) int64 {
	t.Helper()
	var id int64
	err := s.DB().QueryRowContext(context.Background(),
		`INSERT INTO users (unique_id, nickname, password_hash, created_at)
		 VALUES ($1, $2, 'x', NOW()) RETURNING id`,
		"w6atest_"+suffix, "w6a-"+suffix).Scan(&id)
	if err != nil {
		t.Fatalf("seeding user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = s.DB().ExecContext(context.Background(), `DELETE FROM users WHERE id = $1`, id)
	})
	return id
}

func TestAuditDB(t *testing.T) {
	s := testDBStore(t)
	ctx := context.Background()
	suffix := fmt.Sprint(time.Now().UnixNano())

	actor := "w6a_audit_" + suffix
	for i := 0; i < 3; i++ {
		s.Audit(ctx, actor, "group_create", fmt.Sprintf("server:g%d", i), "")
	}
	t.Cleanup(func() {
		_, _ = s.DB().ExecContext(ctx, `DELETE FROM audit_log WHERE actor_unique_id = $1`, actor)
	})

	entries, err := s.AuditList(ctx, 0, 2)
	if err != nil {
		t.Fatalf("AuditList: %v", err)
	}
	if len(entries) != 2 || entries[0].Actor != actor || entries[0].Target != "server:g2" {
		t.Fatalf("page 1 = %+v", entries)
	}
	// Page 2 via before_id returns the oldest of the three.
	entries, err = s.AuditList(ctx, entries[1].ID, 10)
	if err != nil {
		t.Fatalf("AuditList page 2: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Actor == actor && e.Target == "server:g0" {
			found = true
		}
	}
	if !found {
		t.Fatalf("page 2 missing oldest entry: %+v", entries)
	}
}

// TestBansDB covers ListBans/DeleteBan (wave 6b ban administration).
func TestBansDB(t *testing.T) {
	s := testDBStore(t)
	ctx := context.Background()
	suffix := fmt.Sprint(time.Now().UnixNano())

	value := "w6a_ban_" + suffix
	if _, err := s.DB().ExecContext(ctx,
		`INSERT INTO bans (ban_type, value, reason, expires_at) VALUES (1, $1, 'test ban', NOW() + interval '1 hour')`, value); err != nil {
		t.Fatalf("seeding ban: %v", err)
	}
	t.Cleanup(func() {
		_, _ = s.DB().ExecContext(ctx, `DELETE FROM bans WHERE value = $1`, value)
	})

	bans, err := s.ListBans(ctx)
	if err != nil {
		t.Fatalf("ListBans: %v", err)
	}
	var found *BanRecord
	for i := range bans {
		if bans[i].Value == value {
			found = &bans[i]
		}
	}
	if found == nil || found.Reason != "test ban" || found.ExpiresAt == nil {
		t.Fatalf("seeded ban = %+v", found)
	}

	if err := s.DeleteBan(ctx, found.ID); err != nil {
		t.Fatalf("DeleteBan: %v", err)
	}
	bans, err = s.ListBans(ctx)
	if err != nil {
		t.Fatalf("ListBans after DeleteBan: %v", err)
	}
	for _, b := range bans {
		if b.Value == value {
			t.Fatal("ban still listed after delete")
		}
	}
}

// TestGroupMembersDB covers the member listings against a real database.
