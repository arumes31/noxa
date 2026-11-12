package query

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"noxa/internal/auth"
)

// startSSHQuery starts an SSH ServerQuery front end on an ephemeral port and
// returns its address.
func startSSHQuery(t *testing.T, backend Backend) string {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	base := New("", nil, backend)
	srv := NewSSH(addr, filepath.Join(t.TempDir(), "host.key"), base)
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Start(ctx) }()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", addr)
		if err == nil {
			_ = conn.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(func() {
		cancel()
		_ = srv.Close()
		<-errCh
	})
	return addr
}

// TestSSHCloseWithActiveSession verifies shutdown does not wait for an idle
// session's read timeout (224).
func TestSSHCloseWithActiveSession(t *testing.T) {
	backend := &roleQueryBackend{}
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	base := New("", nil, backend)
	srv := NewSSH(addr, filepath.Join(t.TempDir(), "host.key"), base)
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Start(context.Background()) }()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", addr); err == nil {
			_ = c.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	client, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User:            "integration",
		Auth:            []ssh.AuthMethod{ssh.Password("pw")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	})
	if err != nil {
		t.Fatalf("ssh dial: %v", err)
	}
	defer closeServerQueryTestResource(t, client)
	sess, err := client.NewSession()
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	defer closeServerQueryTestResource(t, sess)
	if err := sess.Shell(); err != nil {
		t.Fatalf("shell: %v", err)
	}

	closed := make(chan struct{})
	go func() {
		_ = srv.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("Close blocked on an idle session")
	}
	<-errCh
}

// TestSSHHostKeyIsStable verifies the generated host key is reused, so clients
// do not see a changed host identity after a restart (224).
func TestSSHHostKeyIsStable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "host.key")
	first, err := loadOrCreateHostKey(path)
	if err != nil {
		t.Fatalf("create host key: %v", err)
	}
	second, err := loadOrCreateHostKey(path)
	if err != nil {
		t.Fatalf("reload host key: %v", err)
	}
	if ssh.FingerprintSHA256(first.PublicKey()) != ssh.FingerprintSHA256(second.PublicKey()) {
		t.Fatal("host key changed between starts")
	}
}

func TestLoadOrCreateHostKeyRejectsPermissiveExistingKey(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not provide POSIX file modes")
	}
	path := filepath.Join(t.TempDir(), "host.key")
	if _, err := loadOrCreateHostKey(path); err != nil {
		t.Fatalf("create host key: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod host key: %v", err)
	}
	if _, err := loadOrCreateHostKey(path); err == nil {
		t.Fatal("permissive host key was accepted")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat host key: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("host key mode = %o, want it unchanged at 644", info.Mode().Perm())
	}
}

func TestLoadOrCreateHostKeyCreatesRestrictedFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not provide POSIX file modes")
	}
	path := filepath.Join(t.TempDir(), "new", "host.key")
	if _, err := loadOrCreateHostKey(path); err != nil {
		t.Fatalf("create host key: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat host key: %v", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("host key mode = %o, want no group/other access", info.Mode().Perm())
	}
}

func TestExecCommand(t *testing.T) {
	t.Parallel()

	payload := func(declared uint32, body string) []byte {
		out := make([]byte, 4+len(body))
		binary.BigEndian.PutUint32(out, declared)
		copy(out[4:], body)
		return out
	}
	tests := []struct {
		name     string
		payload  []byte
		expected string
	}{
		{name: "valid", payload: payload(10, "clientlist"), expected: "clientlist"},
		{name: "truncated prefix", payload: []byte{0, 0, 0}},
		{name: "declared length exceeds payload", payload: payload(11, "clientlist")},
		{name: "maximum declared length", payload: payload(^uint32(0), "clientlist")},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := execCommand(test.payload); got != test.expected {
				t.Fatalf("execCommand() = %q, want %q", got, test.expected)
			}
		})
	}
}

func dialTestSSH(t *testing.T, addr string) *ssh.Client {
	t.Helper()
	client, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User:            "integration",
		Auth:            []ssh.AuthMethod{ssh.Password("pw")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	})
	if err != nil {
		t.Fatalf("ssh dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestSSHLimitsSessionChannelsPerConnection(t *testing.T) {
	addr := startSSHQuery(t, &roleQueryBackend{})
	client := dialTestSSH(t, addr)
	for i := range maxSSHChannelsPerConn {
		if _, _, err := client.OpenChannel("session", nil); err != nil {
			t.Fatalf("channel %d rejected: %v", i, err)
		}
	}
	_, _, err := client.OpenChannel("session", nil)
	var open *ssh.OpenChannelError
	if !errors.As(err, &open) || open.Reason != ssh.ResourceShortage {
		t.Fatalf("channel beyond limit err=%v, want resource shortage", err)
	}
}

func TestSSHReservationReleasesCapacity(t *testing.T) {
	srv := NewSSH("", "", New("", nil, &roleQueryBackend{}))
	perConn := new(atomic.Int64)
	for range maxSSHChannelsPerConn {
		if !srv.reserveChannel(perConn) {
			t.Fatal("reservation within limit refused")
		}
	}
	if srv.reserveChannel(perConn) {
		t.Fatal("reservation beyond per-connection limit admitted")
	}
	srv.releaseChannel(perConn)
	if !srv.reserveChannel(perConn) {
		t.Fatal("released capacity not reusable")
	}
	if got := srv.channels.Load(); got != maxSSHChannelsPerConn {
		t.Fatalf("process channel count=%d", got)
	}
}

func TestSSHClosesChannelWithoutShellOrExec(t *testing.T) {
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	srv := NewSSH(addr, filepath.Join(t.TempDir(), "host.key"), New("", nil, &roleQueryBackend{}))
	srv.setupTimeout = 100 * time.Millisecond
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Start(context.Background()) }()
	t.Cleanup(func() { _ = srv.Close(); <-errCh })
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", addr); err == nil {
			_ = c.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	client := dialTestSSH(t, addr)
	ch, reqs, err := client.OpenChannel("session", nil)
	if err != nil {
		t.Fatal(err)
	}
	go ssh.DiscardRequests(reqs)
	done := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, ch); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("idle session channel was not closed")
	}
}

// Unauthenticated SSH handshakes are capped per source and never take one
// of the shared ServerQuery slots (audit #18).
func TestSSHHandshakesDoNotTakeQuerySlots(t *testing.T) {
	base := New("", nil, &roleQueryBackend{})
	srv := NewSSH("", "", base)
	var conns []net.Conn
	for range maxSSHHandshakesPerSource + 1 {
		a, b := net.Pipe()
		t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
		conns = append(conns, a)
	}
	for i := range maxSSHHandshakesPerSource {
		if !srv.beginHandshake(conns[i]) {
			t.Fatalf("handshake %d within the per-source cap refused", i)
		}
	}
	if srv.beginHandshake(conns[maxSSHHandshakesPerSource]) {
		t.Fatal("handshake beyond the per-source cap admitted")
	}
	base.mu.Lock()
	taken := len(base.conns)
	base.mu.Unlock()
	if taken != 0 {
		t.Fatalf("pending handshakes took %d ServerQuery slots", taken)
	}
	srv.endHandshake(conns[0])
	srv.endHandshake(conns[0]) // idempotent
	if !srv.beginHandshake(conns[maxSSHHandshakesPerSource]) {
		t.Fatal("a released handshake slot was not reusable")
	}
}

// A `login` typed inside an SSH session counts against the SSH limiter
// namespace, not the loopback raw port's (CodeRabbit on #308).
func TestSSHSessionLoginUsesSSHLimiterNamespace(t *testing.T) {
	s := New("", nil, &roleQueryBackend{})
	s.MaxLoginFailures = 1
	var out bytes.Buffer
	sess := &session{w: &out, remoteIP: "192.0.2.5", sshModelRequired: true, sshModelAccepted: true}
	s.execute(t.Context(), sess, "login integration wrong authorization_model=roles-v1")
	if s.LoginAllowed(auth.LoginFailureScope("ssh", "integration")) {
		t.Fatal("SSH-session login failure was not counted in the ssh namespace")
	}
	if !s.LoginAllowed(auth.LoginFailureScope("query", "integration")) {
		t.Fatal("SSH-session login failure locked the raw ServerQuery namespace")
	}
}

// SSH password failures also spend the account budget shared with TCP,
// /events and gRPC; the loopback raw port never does (audit2 #9).
func TestPasswordFailuresShareAccountBudgetExceptRawQuery(t *testing.T) {
	s := New("", nil, &roleQueryBackend{})
	s.MaxLoginFailures = 1
	var out bytes.Buffer
	raw := &session{w: &out, remoteIP: "192.0.2.6"}
	s.execute(t.Context(), raw, "login integration wrong authorization_model=roles-v1")
	if !s.LoginAllowed(auth.AccountPasswordScope("integration")) {
		t.Fatal("raw ServerQuery failure spent the shared account budget")
	}
	sess := &session{w: &out, remoteIP: "192.0.2.5", sshModelRequired: true, sshModelAccepted: true}
	s.execute(t.Context(), sess, "login integration wrong authorization_model=roles-v1")
	if s.LoginAllowed(auth.AccountPasswordScope("integration")) {
		t.Fatal("SSH failure did not spend the shared account budget")
	}
}
