package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestAssignGuestGroupAtomic(t *testing.T) {
	s := testDBStore(t)
	ctx := context.Background()
	uid := fmt.Sprintf("guest-assignment-%d", time.Now().UnixNano())
	groupID, err := s.CreateGroup(ctx, "server", uid, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.DB().ExecContext(ctx, `DELETE FROM users WHERE unique_id = $1`, uid)
		_ = s.DeleteGroup(ctx, "server", groupID, true)
	})
	if _, err := s.AssignGuestGroup(ctx, "server", -1, 0, uid, "guest", 0); err == nil {
		t.Fatal("missing group accepted")
	}
	var count int
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE unique_id = $1`, uid).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("failed assignment left a user row behind")
	}
	id, err := s.AssignGuestGroup(ctx, "server", groupID, 0, uid, "guest", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	var admin bool
	var expiry time.Time
	if err := s.DB().QueryRowContext(ctx, `SELECT u.is_admin, m.expires_at FROM users u JOIN server_group_members m ON m.user_id = u.id WHERE u.id = $1 AND m.server_group_id = $2`, id, groupID).Scan(&admin, &expiry); err != nil {
		t.Fatal(err)
	}
	if admin || time.Until(expiry) < 59*time.Minute {
		t.Fatal("incorrect privileges or timed membership")
	}
	if _, err := s.DB().ExecContext(ctx, `UPDATE users SET password_hash = 'existing-credential' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AssignGuestGroup(ctx, "server", groupID, 0, uid, "overwrite", 0); err == nil {
		t.Fatal("credential-bearing identity accepted as a guest")
	}
}
