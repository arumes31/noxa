package web

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"golang.org/x/net/websocket"

	"noxa/internal/config"
)

// echoControl copies every byte back, standing in for the control server.
type echoControl struct{ remote chan string }

func (e *echoControl) ServeExternalConn(_ context.Context, conn net.Conn) error {
	defer func() { _ = conn.Close() }()
	if e.remote != nil {
		e.remote <- conn.RemoteAddr().String()
	}
	_, _ = io.Copy(conn, conn)
	return nil
}

func testConfig(t *testing.T) config.WebConfig {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!doctype html>app"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "assets", "main-abc.js"), []byte("js"), 0o600); err != nil {
		t.Fatal(err)
	}
	return config.WebConfig{
		Enabled:              true,
		Addr:                 "127.0.0.1:0",
		PublicURL:            "https://chat.example",
		TrustedProxies:       []string{"127.0.0.0/8"},
		AppDir:               dir,
		MaxDevicesPerAccount: 10,
		WSConnectionsPerIP:   2,
		WSAttemptsPerMinute:  30,
	}
}

func newTestServer(t *testing.T, cfg config.WebConfig, control ControlServer) *httptest.Server {
	t.Helper()
	srv, err := New(cfg, "noXa test", control, zap.NewNop())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func dialWS(t *testing.T, ts *httptest.Server, origin string) (*websocket.Conn, error) {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	cfg, err := websocket.NewConfig(wsURL, origin)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Header.Set("X-Forwarded-Proto", "https")
	cfg.Header.Set("X-Forwarded-For", "203.0.113.7")
	return websocket.DialConfig(cfg)
}

func TestWSCarriesControlByteStream(t *testing.T) {
	control := &echoControl{remote: make(chan string, 1)}
	ts := newTestServer(t, testConfig(t), control)
	conn, err := dialWS(t, ts, "https://chat.example")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	conn.PayloadType = websocket.BinaryFrame
	if got := <-control.remote; !strings.HasPrefix(got, "203.0.113.7") {
		t.Fatalf("control saw remote %q, want the forwarded client", got)
	}
	// Two messages may arrive as one stream; the reader must not care.
	if _, err := conn.Write([]byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte{4, 5}); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	got := make([]byte, 5)
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != string([]byte{1, 2, 3, 4, 5}) {
		t.Fatalf("echo = %v", got)
	}
}

func TestWSRejectsForeignOrigin(t *testing.T) {
	ts := newTestServer(t, testConfig(t), &echoControl{})
	if _, err := dialWS(t, ts, "https://evil.example"); err == nil {
		t.Fatal("foreign origin was accepted")
	}
}

func TestWSRequiresHTTPSFromProxy(t *testing.T) {
	ts := newTestServer(t, testConfig(t), &echoControl{})
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/web/info", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("plain HTTP through proxy = %d, want 403", resp.StatusCode)
	}
}

func TestWSLimitsOpenConnectionsPerIP(t *testing.T) {
	ts := newTestServer(t, testConfig(t), &echoControl{})
	for i := 0; i < 2; i++ {
		conn, err := dialWS(t, ts, "https://chat.example")
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		defer func() { _ = conn.Close() }()
	}
	if _, err := dialWS(t, ts, "https://chat.example"); err == nil {
		t.Fatal("third connection from one IP was accepted")
	}
}

func TestAdmissionRateLimitsAttempts(t *testing.T) {
	a := newAdmission(100, 3)
	ip := netip.MustParseAddr("198.51.100.1")
	now := time.Now()
	for i := 0; i < 3; i++ {
		release, ok := a.admit(ip, now)
		if !ok {
			t.Fatalf("attempt %d refused", i)
		}
		release()
	}
	if _, ok := a.admit(ip, now); ok {
		t.Fatal("fourth attempt within a minute was admitted")
	}
	if _, ok := a.admit(netip.MustParseAddr("198.51.100.2"), now); !ok {
		t.Fatal("another IP was refused")
	}
	if _, ok := a.admit(ip, now.Add(30*time.Second)); !ok {
		t.Fatal("attempt was refused after the bucket refilled")
	}
}

func TestClientIPIgnoresForwardedFromUntrustedPeer(t *testing.T) {
	srv, err := New(testConfig(t), "noXa", &echoControl{}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.RemoteAddr = "192.0.2.10:5555"
	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	if got := srv.clientIP(req); got.String() != "192.0.2.10" {
		t.Fatalf("clientIP = %s, want the untrusted peer", got)
	}
	req.RemoteAddr = "127.0.0.1:5555"
	req.Header.Set("X-Forwarded-For", "203.0.113.7, 127.0.0.2")
	if got := srv.clientIP(req); got.String() != "203.0.113.7" {
		t.Fatalf("clientIP = %s, want the nearest untrusted hop", got)
	}
}

func TestInfoAndAppRoutes(t *testing.T) {
	ts := newTestServer(t, testConfig(t), &echoControl{})
	get := func(path string) *http.Response {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = resp.Body.Close() })
		return resp
	}

	var info Info
	resp := get("/api/v1/web/info")
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		t.Fatal(err)
	}
	if info.RPID != "chat.example" || info.ProtocolVersion != ProtocolVersion || info.PublicURL != "https://chat.example" {
		t.Fatalf("info = %+v", info)
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Fatalf("missing CSP: %q", csp)
	}

	for _, path := range []string{"/app/", "/app/channels/general", "/link"} {
		body, _ := io.ReadAll(get(path).Body)
		if !strings.Contains(string(body), "app") {
			t.Fatalf("%s did not serve index.html: %q", path, body)
		}
	}
	if cc := get("/app/assets/main-abc.js").Header.Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Fatalf("hashed asset Cache-Control = %q", cc)
	}
	if code := get("/app/missing.js").StatusCode; code != http.StatusNotFound {
		t.Fatalf("missing asset = %d, want 404", code)
	}
}

func TestLoopbackOriginsOnlyInDevMode(t *testing.T) {
	srv, err := New(testConfig(t), "noXa", &echoControl{}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if srv.originAllowed("http://127.0.0.1:5174") {
		t.Fatal("loopback origin accepted outside dev mode")
	}
	srv.AllowLoopbackOrigins()
	for origin, want := range map[string]bool{
		"http://127.0.0.1:5174": true, "http://localhost": true, "https://[::1]:8443": true,
		"http://192.168.1.2": false, "http://localhost.evil.example": false, "null": false,
	} {
		if got := srv.originAllowed(origin); got != want {
			t.Errorf("originAllowed(%q) = %v, want %v", origin, got, want)
		}
	}
}
