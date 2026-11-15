package server

import (
	"context"
	"net"
	"os"
	"testing"
	"time"

	"go.uber.org/zap"
	"noxa/internal/auth"
	"noxa/internal/config"
	"noxa/internal/store"
)

func TestGuestSessionsCappedPerAddressMembersUnaffected(t *testing.T) {
	env := startTestEnvFull(t, nil, func(cfg *config.Config) { cfg.Web.WSConnectionsPerIP = 2 })
	defer env.stop()
	for i := range 2 {
		conn, resp := dialGuest(t, env.addr, "guest", "")
		defer func() { _ = conn.Close() }()
		if !resp.OK {
			t.Fatalf("guest %d refused: %s", i, resp.Reason)
		}
	}
	conn, resp := dialGuest(t, env.addr, "guest", "")
	_ = conn.Close()
	if resp.OK || resp.Reason != "too many guest sessions from your address" {
		t.Fatalf("third guest from one address: %+v", resp)
	}
	for _, uid := range []string{"admin-uid", "user-uid"} {
		member, _ := dialAuthed(t, env.addr, uid) // fails the test when refused
		defer func() { _ = member.Close() }()
	}
}

type addrConn struct {
	net.Conn
	addr net.Addr
}

func (c addrConn) RemoteAddr() net.Addr { return c.addr }

func guestOn(ip string, web bool) *Client {
	c := &Client{Conn: addrConn{addr: &net.TCPAddr{IP: net.ParseIP(ip), Port: 1}}}
	c.webTransport.Store(web)
	return c
}

func TestGuestBanIPSkipsMembersAndPossibleProxies(t *testing.T) {
	s := New(&config.Config{Web: config.WebConfig{TrustedProxies: []string{"10.0.0.0/8"}}}, zap.NewNop(), &Deps{})
	member := guestOn("203.0.113.7", false)
	member.UserID = 5
	for _, tt := range []struct {
		client *Client
		want   string
	}{
		{guestOn("::ffff:203.0.113.7", false), "203.0.113.7"},
		{guestOn("127.0.0.1", false), "127.0.0.1"},
		{guestOn("203.0.113.7", true), "203.0.113.7"},
		{guestOn("127.0.0.1", true), ""},
		{guestOn("10.1.2.3", true), ""},
		{member, ""},
	} {
		if got := s.guestBanIP(tt.client); got != tt.want {
			t.Errorf("guestBanIP(%v web=%v) = %q, want %q", tt.client.Conn.RemoteAddr(), tt.client.isWebTransport(), got, tt.want)
		}
	}
}

// A guest's ban also bans its address (a new guest ID is free), and lifting
// the ID ban lifts that IP ban with it.
func TestGuestBanCreatesLinkedIPBan(t *testing.T) {
	dbURL := os.Getenv("NOXA_TEST_DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://noxa:noxa@localhost:5432/noxa?sslmode=disable"
	}
	st, err := store.New(dbURL, zap.NewNop(), 5, 1, time.Minute)
	if err != nil {
		if os.Getenv("NOXA_TEST_DATABASE_URL") != "" {
			t.Fatalf("configured database unavailable: %v", err)
		}
		t.Skipf("database unavailable, skipping: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	const ip, guestID = "203.0.113.77", "guest:ban-link-test"
	t.Cleanup(func() {
		_, _ = st.DB().ExecContext(ctx, `DELETE FROM bans WHERE value IN ($1, $2)`, ip, guestID)
	})
	s := New(&config.Config{}, zap.NewNop(), &Deps{Bans: st, BanAdmin: st})
	if err := s.insertBan(ctx, guestID, s.guestBanIP(guestOn("::ffff:"+ip, false)), "spam", nil, nil); err != nil {
		t.Fatal(err)
	}
	authSvc := auth.New(st, zap.NewNop())
	if ban, err := authSvc.LookupActiveBan(ctx, "guest:a-fresh-id", ip); err != nil || ban == nil || ban.Type != 0 || ban.Reason != "spam" {
		t.Fatalf("login of a new guest ID from the banned address: ban %+v err %v", ban, err)
	}
	var uidBan int64
	if err := st.DB().QueryRowContext(ctx, `SELECT id FROM bans WHERE ban_type = 1 AND value = $1`, guestID).Scan(&uidBan); err != nil {
		t.Fatal(err)
	}
	if err := s.removeRoleBan(ctx, "actor", uidBan); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM bans WHERE value IN ($1, $2)`, ip, guestID).Scan(&left); err != nil || left != 0 {
		t.Fatalf("unban left %d rows (err %v)", left, err)
	}
	if ban, err := authSvc.LookupActiveBan(ctx, "guest:a-fresh-id", ip); err != nil || ban != nil {
		t.Fatalf("address still banned after unban: %+v %v", ban, err)
	}
}
