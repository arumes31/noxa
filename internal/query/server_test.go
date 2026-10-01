// server_test.go exercises the ServerQuery protocol over real TCP with a
// fake backend.
package query

import (
	"bufio"
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"noxa/internal/auth"
	"noxa/internal/broadcast"
	"noxa/internal/metrics"
)

func closeServerQueryTestResource(t *testing.T, closer io.Closer) {
	t.Helper()
	if err := closer.Close(); err != nil {
		t.Logf("closing test resource: %v", err)
	}
}

// fakeBackend supplies only the roles-v1 integration contract to transport tests.
type fakeBackend struct {
	users map[string]struct {
		password    string
		integration bool
	}
}

func (*fakeBackend) RoleIntegrationsEnabled() bool { return true }

func (f *fakeBackend) AuthenticateIntegration(_ context.Context, uniqueID, password, _ string) (auth.IntegrationPrincipal, error) {
	user, ok := f.users[uniqueID]
	if !ok || !user.integration || user.password != password {
		return auth.IntegrationPrincipal{}, auth.ErrIntegrationDenied
	}
	return auth.IntegrationPrincipal{}, nil
}

func (*fakeBackend) WithIntegrationSnapshot(ctx context.Context, _ auth.IntegrationPrincipal, deliver func(context.Context, *broadcast.TreeSnapshot) error) error {
	return deliver(ctx, &broadcast.TreeSnapshot{})
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{users: map[string]struct {
		password    string
		integration bool
	}{
		"admin-uid": {"pw", true},
		"user-uid":  {"pw", false},
	}}
}

// startQueryServer starts a Server on an ephemeral port and returns its
// address.
func startQueryServer(t *testing.T, backend Backend) (string, *Server) {
	t.Helper()
	return startQueryServerWith(t, backend, nil)
}

// startQueryServerWith is startQueryServer with a hook to tune limits before
// the listener starts.
func startQueryServerWith(t *testing.T, backend Backend, configure func(*Server)) (string, *Server) {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	srv := New(addr, nil, backend)
	if configure != nil {
		configure(srv)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		_ = srv.Close()
		<-errCh
	})

	// Wait until the server accepts and releases the readiness connection.
	// Closing the client alone can leave it counted against MaxConns briefly.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", addr)
		if err == nil {
			defer closeServerQueryTestResource(t, conn)
			if err := conn.SetDeadline(deadline); err != nil {
				t.Fatal(err)
			}
			if _, err := io.WriteString(conn, "quit\n"); err != nil {
				t.Fatalf("readiness quit: %v", err)
			}
			// Server-side EOF follows unregisterConn, so no probe occupies a slot.
			if _, err := io.Copy(io.Discard, conn); err != nil {
				t.Fatalf("readiness cleanup: %v", err)
			}
			return addr, srv
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("query server did not become ready")
	return "", nil
}

// dialQuery connects and consumes the two banner lines.
func dialQuery(t *testing.T, addr string) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	r := bufio.NewReader(conn)
	b1 := readLine(t, r)
	if !strings.HasPrefix(b1, "NOXA ServerQuery") {
		t.Fatalf("banner line 1 = %q", b1)
	}
	_ = readLine(t, r) // hint line
	return conn, r
}

func readLine(t *testing.T, r *bufio.Reader) string {
	t.Helper()
	line, err := r.ReadString('\n')
	if err != nil {
		t.Fatalf("read line: %v", err)
	}
	return strings.TrimRight(line, "\r\n")
}

// sendCmd writes a command and reads lines until the terminating error line,
// returning all lines (error line last).
func sendCmd(t *testing.T, conn net.Conn, r *bufio.Reader, cmd string) []string {
	t.Helper()
	if _, err := conn.Write([]byte(cmd + "\n")); err != nil {
		t.Fatalf("write command: %v", err)
	}
	var lines []string
	for {
		line := readLine(t, r)
		lines = append(lines, line)
		if strings.HasPrefix(line, "error id=") {
			return lines
		}
	}
}

// loginOK authenticates as the admin user.
func loginOK(t *testing.T, conn net.Conn, r *bufio.Reader) {
	t.Helper()
	lines := sendCmd(t, conn, r, "login admin-uid pw authorization_model=roles-v1")
	if got := lines[len(lines)-1]; got != "error id=0 msg=ok" {
		t.Fatalf("login = %q", got)
	}
}

func lastErr(t *testing.T, lines []string) string {
	t.Helper()
	return lines[len(lines)-1]
}

// --- tests ------------------------------------------------------------------

func TestGreeting(t *testing.T) {
	addr, _ := startQueryServer(t, newFakeBackend())
	conn, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer closeServerQueryTestResource(t, conn)

	r := bufio.NewReader(conn)
	if line := readLine(t, r); !strings.Contains(line, "NOXA ServerQuery "+Version) {
		t.Fatalf("banner = %q", line)
	}
	if line := readLine(t, r); !strings.Contains(line, "help") {
		t.Fatalf("hint = %q", line)
	}
}

func TestUnauthedRejected(t *testing.T) {
	addr, _ := startQueryServer(t, newFakeBackend())
	conn, r := dialQuery(t, addr)
	defer closeServerQueryTestResource(t, conn)

	lines := sendCmd(t, conn, r, "clientlist")
	if got := lastErr(t, lines); got != `error id=2568 msg=not\slogged\sin` {
		t.Fatalf("error = %q", got)
	}
}

func TestLoginFailures(t *testing.T) {
	addr, _ := startQueryServer(t, newFakeBackend())
	conn, r := dialQuery(t, addr)
	defer closeServerQueryTestResource(t, conn)

	// Wrong password.
	lines := sendCmd(t, conn, r, "login admin-uid wrong authorization_model=roles-v1")
	if got := lastErr(t, lines); !strings.HasPrefix(got, "error id=520") {
		t.Fatalf("wrong password error = %q", got)
	}

	// Valid credentials but not an admin.
	lines = sendCmd(t, conn, r, "login user-uid pw authorization_model=roles-v1")
	if got := lastErr(t, lines); !strings.HasPrefix(got, "error id=520") {
		t.Fatalf("integration-disabled user error = %q", got)
	}

	// Admin succeeds.
	lines = sendCmd(t, conn, r, "login admin-uid pw authorization_model=roles-v1")
	if got := lastErr(t, lines); got != "error id=0 msg=ok" {
		t.Fatalf("admin login = %q", got)
	}
}

func TestUnknownCommand(t *testing.T) {
	addr, _ := startQueryServer(t, newFakeBackend())
	conn, r := dialQuery(t, addr)
	defer closeServerQueryTestResource(t, conn)
	loginOK(t, conn, r)

	lines := sendCmd(t, conn, r, "frobnicate foo=1")
	if got := lastErr(t, lines); !strings.HasPrefix(got, "error id=2568") {
		t.Fatalf("unknown command error = %q", got)
	}
}

func TestHelpVersionQuit(t *testing.T) {
	addr, _ := startQueryServer(t, newFakeBackend())
	conn, r := dialQuery(t, addr)
	defer closeServerQueryTestResource(t, conn)

	// help works unauthenticated.
	lines := sendCmd(t, conn, r, "help")
	if joined := strings.Join(lines, "\n"); !strings.Contains(joined, "login <unique_id_or_nickname> <password> authorization_model=roles-v1") {
		t.Fatalf("help = %v", lines)
	}

	// version works unauthenticated.
	lines = sendCmd(t, conn, r, "version")
	if lines[0] != "version="+Version {
		t.Fatalf("version = %q", lines[0])
	}

	// quit closes the connection after an ok.
	lines = sendCmd(t, conn, r, "quit")
	if got := lastErr(t, lines); got != "error id=0 msg=ok" {
		t.Fatalf("quit = %q", got)
	}
	if _, err := r.ReadString('\n'); err == nil {
		t.Fatal("connection not closed after quit")
	}
}

func TestBruteForceLockout(t *testing.T) {
	backend := newFakeBackend()
	addr, _ := startQueryServerWith(t, backend, func(s *Server) { s.MaxLoginFailures = 3 })

	conn, r := dialQuery(t, addr)
	defer closeServerQueryTestResource(t, conn)

	for i := 0; i < 3; i++ {
		sendCmd(t, conn, r, "login admin-uid wrong authorization_model=roles-v1")
	}
	// Next attempt is refused even with the right password.
	lines := sendCmd(t, conn, r, "login admin-uid pw authorization_model=roles-v1")
	if got := lastErr(t, lines); !strings.HasPrefix(got, "error id=520") ||
		!strings.Contains(got, `too\smany\sfailed\slogins`) {
		t.Fatalf("lockout error = %q", got)
	}
}

func TestUnknownLoginMatchesWrongPasswordAndIsLimited(t *testing.T) {
	backend := newFakeBackend()
	addr, server := startQueryServerWith(t, backend, func(s *Server) { s.MaxLoginFailures = 3 })
	m := metrics.New()
	server.SetMetrics(m)
	conn, reader := dialQuery(t, addr)
	defer closeServerQueryTestResource(t, conn)

	wrongKnown := lastErr(t, sendCmd(t, conn, reader, "login admin-uid wrong authorization_model=roles-v1"))
	unknown := lastErr(t, sendCmd(t, conn, reader, "login unknown-principal wrong authorization_model=roles-v1"))
	if wrongKnown != unknown {
		t.Fatalf("wrong known response = %q, unknown response = %q", wrongKnown, unknown)
	}

	families, err := m.Registry().Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	for _, family := range families {
		if family.GetName() != "noxa_auth_failures_total" {
			continue
		}
		if len(family.GetMetric()) != 1 || family.GetMetric()[0].GetCounter().GetValue() != 2 {
			t.Fatalf("query auth failure metric = %+v, want one invalid-credential series with value 2", family.GetMetric())
		}
		goto metricChecked
	}
	t.Fatal("query auth failure metric was not gathered")

metricChecked:

	limitedAddr, _ := startQueryServerWith(t, backend, func(s *Server) { s.MaxLoginFailures = 1 })
	limitedConn, limitedReader := dialQuery(t, limitedAddr)
	defer closeServerQueryTestResource(t, limitedConn)
	_ = sendCmd(t, limitedConn, limitedReader, "login unknown-principal wrong authorization_model=roles-v1")
	if got := lastErr(t, sendCmd(t, limitedConn, limitedReader, "login unknown-principal wrong authorization_model=roles-v1")); !strings.Contains(got, `too\smany\sfailed\slogins`) {
		t.Fatalf("unknown login was not limited: %q", got)
	}
}

func TestServerQueryLoginFailureStateExpiresResetsAndIsBounded(t *testing.T) {
	backend := newFakeBackend()
	server := New("127.0.0.1:0", nil, backend)
	server.MaxLoginFailures = 2
	server.LockoutDuration = time.Minute
	server.LoginFailureTTL = 30 * time.Second
	server.MaxLoginFailureEntries = 2
	now := time.Date(2026, time.August, 21, 12, 0, 0, 0, time.UTC)
	server.loginNow = func() time.Time { return now }

	expiring := auth.LoginFailureScope("192.0.2.1", "expiring")
	server.RecordLoginFailure(expiring)
	now = now.Add(31 * time.Second)
	server.RecordLoginFailure(expiring)
	if !server.LoginAllowed(expiring) {
		t.Fatal("expired failure streak did not reset")
	}

	locked := auth.LoginFailureScope("192.0.2.1", "locked")
	server.RecordLoginFailure(locked)
	server.RecordLoginFailure(locked)
	if server.LoginAllowed(locked) {
		t.Fatal("scope was not locked at threshold")
	}
	server.ClearLoginFailures(locked)
	if !server.LoginAllowed(locked) {
		t.Fatal("successful-login reset did not clear lockout")
	}

	capacityServer := New("127.0.0.1:0", nil, backend)
	capacityServer.MaxLoginFailures = 2
	capacityServer.LockoutDuration = time.Minute
	capacityServer.LoginFailureTTL = time.Minute
	capacityServer.MaxLoginFailureEntries = 2
	capacityServer.loginNow = func() time.Time { return now }
	first := auth.LoginFailureScope("192.0.2.1", "first")
	second := auth.LoginFailureScope("192.0.2.1", "second")
	third := auth.LoginFailureScope("192.0.2.1", "third")
	capacityServer.RecordLoginFailure(first)
	now = now.Add(time.Second)
	capacityServer.RecordLoginFailure(second)
	now = now.Add(time.Second)
	capacityServer.RecordLoginFailure(third)
	capacityServer.RecordLoginFailure(first)
	if !capacityServer.LoginAllowed(first) {
		t.Fatal("capacity eviction did not reset the oldest incomplete streak")
	}
}

func TestConnectionCap(t *testing.T) {
	addr, _ := startQueryServerWith(t, newFakeBackend(), func(s *Server) { s.MaxConns = 2 })

	c1, _ := dialQuery(t, addr)
	defer closeServerQueryTestResource(t, c1)
	c2, _ := dialQuery(t, addr)
	defer closeServerQueryTestResource(t, c2)

	// Third connection is refused with an error line.
	c3, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer closeServerQueryTestResource(t, c3)
	r3 := bufio.NewReader(c3)
	line, err := r3.ReadString('\n')
	if err != nil {
		t.Fatalf("read refusal: %v", err)
	}
	if !strings.HasPrefix(line, "error id=1539") {
		t.Fatalf("refusal = %q", line)
	}
}

func TestIdleTimeout(t *testing.T) {
	addr, _ := startQueryServerWith(t, newFakeBackend(), func(s *Server) { s.IdleTimeout = 100 * time.Millisecond })

	conn, _ := dialQuery(t, addr)
	defer closeServerQueryTestResource(t, conn)

	// Send nothing: the server should close the connection after the idle
	// timeout.
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 1)
	_, err := conn.Read(buf)
	if err == nil {
		t.Fatal("connection still open after idle timeout")
	}
}

// TestLoginBackendError verifies an unknown user is a clean login failure.
func TestLoginBackendError(t *testing.T) {
	backend := newFakeBackend()
	addr, _ := startQueryServer(t, backend)
	conn, r := dialQuery(t, addr)
	defer closeServerQueryTestResource(t, conn)

	// Unknown user is a clean login failure, not an internal error.
	lines := sendCmd(t, conn, r, "login ghost-uid pw authorization_model=roles-v1")
	if got := lastErr(t, lines); !strings.HasPrefix(got, "error id=520") {
		t.Fatalf("error = %q", got)
	}
}

// --- wave 10a command tests ---------------------------------------------------
