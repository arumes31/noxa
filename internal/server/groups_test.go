// groups_test.go provides the audit and historical group-icon fixtures used by
// current server tests.
package server

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"noxa/internal/netproto"
	"noxa/internal/store"
)

// fakeGroups is the role-aware audit fixture shared by server tests.
type fakeGroups struct {
	mu    sync.Mutex
	audit []store.AuditEntry
}

func newFakeGroups() *fakeGroups { return &fakeGroups{} }

func (f *fakeGroups) Audit(ctx context.Context, actor, action, target, detail string) {
	f.AuditScoped(ctx, actor, action, target, detail, nil)
}

func (f *fakeGroups) AuditScoped(_ context.Context, actor, action, target, detail string, channelIDs []int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.audit = append(f.audit, store.AuditEntry{
		ID: int64(len(f.audit) + 1), Actor: actor, Action: action,
		Target: target, Detail: detail, CreatedAt: time.Now(),
		ChannelIDs: channelIDs,
	})
}

func (f *fakeGroups) AuditList(_ context.Context, beforeID int64, limit int) ([]store.AuditEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var out []store.AuditEntry
	for i := len(f.audit) - 1; i >= 0 && len(out) < limit; i-- {
		if beforeID > 0 && f.audit[i].ID >= beforeID {
			continue
		}
		out = append(out, f.audit[i])
	}
	return out, nil
}

// auditActions returns the recorded audit actions in order.
func (f *fakeGroups) auditActions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.audit))
	for _, e := range f.audit {
		out = append(out, e.Action)
	}
	return out
}

// fakeBanAdmin implements BanAdminStore in memory.
type fakeBanAdmin struct {
	mu   sync.Mutex
	bans []store.BanRecord
}

// --- helpers -----------------------------------------------------------------

// readError reads the next MsgError frame and decodes it.
func readError(t *testing.T, conn net.Conn) netproto.Error {
	t.Helper()
	f := readOfType(t, conn, netproto.MsgError)
	var e netproto.Error
	if err := netproto.Decode(f, &e); err != nil {
		t.Fatalf("decode error frame: %v", err)
	}
	return e
}

// --- group CRUD tests --------------------------------------------------------

func (f *fakeBanAdmin) ListBans(context.Context) ([]store.BanRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]store.BanRecord, len(f.bans))
	copy(out, f.bans)
	return out, nil
}

func (f *fakeBanAdmin) DeleteBan(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, b := range f.bans {
		if b.ID == id {
			f.bans = append(f.bans[:i], f.bans[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("ban not found")
}

var tinyGIF = func() []byte {
	raw, err := base64.StdEncoding.DecodeString("R0lGODlhAQABAIAAAAAAAP///ywAAAAAAQABAAACAUwAOw==")
	if err != nil {
		panic(err)
	}
	return raw
}()

// srvClient returns the server-side registry entry for an authenticated
// connection.
func srvClient(t *testing.T, env *testEnv, uniqueID string) *Client {
	t.Helper()
	var c *Client
	waitFor(t, "client "+uniqueID+" registered", func() bool {
		var ok bool
		c, ok = env.srv.clientByUniqueID(uniqueID)
		return ok
	})
	return c
}

// containsAction reports whether an audit action was recorded.
func containsAction(actions []string, want string) bool {
	for _, a := range actions {
		if a == want {
			return true
		}
	}
	return false
}
