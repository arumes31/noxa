// Package web serves the mobile web app: the PWA bundle, the WebSocket
// control transport and the app metadata the bundle checks on start.
//
// The control protocol is unchanged on /ws. Binary WebSocket messages carry
// the same length-prefixed frames as the TCP listener as a byte stream, so
// authentication, authorization, rate limits and acknowledgements stay in
// the TCP server's dispatcher.
package web

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	"golang.org/x/crypto/acme/autocert"
	"golang.org/x/net/websocket"
	"golang.org/x/time/rate"

	"noxa/internal/config"
	"noxa/internal/health"
	"noxa/internal/netproto"
	"noxa/internal/version"
)

// ProtocolVersion is the web app protocol this server speaks. A bundle with
// a lower version must reload before connecting; a call in progress keeps
// running until it ends.
const ProtocolVersion = 1

const (
	readHeaderTimeout = 10 * time.Second
	idleTimeout       = 60 * time.Second
	limiterIdleTTL    = 10 * time.Minute
	// frameHeaderSize mirrors netproto's type + length prefix.
	frameHeaderSize = 8
)

// ControlServer runs the control protocol on an accepted connection.
type ControlServer interface {
	ServeExternalConn(ctx context.Context, conn net.Conn) error
}

// Info is the public app metadata returned by /api/v1/web/info.
type Info struct {
	ServerName      string `json:"server_name"`
	PublicURL       string `json:"public_url"`
	RPID            string `json:"rp_id"`
	ProtocolVersion int    `json:"protocol_version"`
	// Version is the server build; the bundle reports it as its client
	// version because the server always serves its own matching app.
	Version string `json:"version"`
}

// Server is the mobile web listener.
type Server struct {
	cfg        config.WebConfig
	serverName string
	logger     *zap.Logger
	control    ControlServer
	origins    map[string]struct{}
	proxies    []netip.Prefix
	mux        *http.ServeMux
	srv        *http.Server
	admission  *admission
	baseCtx    context.Context
	cancelBase context.CancelFunc
	// devLoopback also accepts http(s) loopback origins on /ws, so the app
	// can be developed and tested without a certificate. Never in production.
	devLoopback bool
}

// AllowLoopbackOrigins accepts http(s)://localhost, 127.0.0.1 and [::1]
// origins on any port. Call it only for dev_mode servers.
func (s *Server) AllowLoopbackOrigins() { s.devLoopback = true }

func (s *Server) originAllowed(origin string) bool {
	if _, ok := s.origins[origin]; ok {
		return true
	}
	if !s.devLoopback {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Path != "" {
		return false
	}
	switch u.Hostname() {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

// New builds the listener. It does not open sockets until Start.
func New(cfg config.WebConfig, serverName string, control ControlServer, logger *zap.Logger) (*Server, error) {
	if logger == nil {
		logger = zap.NewNop()
	}
	if control == nil {
		return nil, errors.New("web: control server is required")
	}
	origins := make(map[string]struct{})
	for _, origin := range cfg.Origins() {
		origins[origin] = struct{}{}
	}
	baseCtx, cancel := context.WithCancel(context.Background())
	s := &Server{
		cfg:        cfg,
		serverName: serverName,
		logger:     logger,
		control:    control,
		origins:    origins,
		proxies:    cfg.ProxyNetworks(),
		mux:        http.NewServeMux(),
		admission:  newAdmission(cfg.WSConnectionsPerIP, cfg.WSAttemptsPerMinute),
		baseCtx:    baseCtx,
		cancelBase: cancel,
	}
	s.routes()
	s.srv = &http.Server{
		Addr:              cfg.Addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    1 << 16,
		BaseContext:       func(net.Listener) context.Context { return baseCtx },
	}
	tlsConfig, err := s.tlsConfig()
	if err != nil {
		cancel()
		return nil, err
	}
	s.srv.TLSConfig = tlsConfig
	return s, nil
}

// Handle registers an additional route, e.g. passkey or device-link APIs.
func (s *Server) Handle(pattern string, h http.Handler) {
	s.mux.Handle(pattern, h)
}

// Handler returns the full handler chain, for tests and embedding.
func (s *Server) Handler() http.Handler {
	return securityHeaders(health.RequireSecureCredentials(s.mux, s.proxies...))
}

// Start serves until Shutdown. It returns nil after a graceful shutdown.
func (s *Server) Start() error {
	s.logger.Info("web listener started",
		zap.String("addr", s.cfg.Addr),
		zap.Bool("tls", s.srv.TLSConfig != nil),
		zap.String("public_url", s.cfg.PublicURL),
	)
	var err error
	if s.srv.TLSConfig != nil {
		err = s.srv.ListenAndServeTLS("", "")
	} else {
		err = s.srv.ListenAndServe()
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Shutdown stops accepting requests. Hijacked WebSocket sessions belong to
// the control server, whose own shutdown closes them.
func (s *Server) Shutdown(ctx context.Context) error {
	s.cancelBase()
	return s.srv.Shutdown(ctx)
}

func (s *Server) tlsConfig() (*tls.Config, error) {
	switch {
	case s.cfg.CertFile != "":
		cert, err := tls.LoadX509KeyPair(s.cfg.CertFile, s.cfg.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("web: loading TLS certificate: %w", err)
		}
		return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}, nil
	case s.cfg.ACME:
		manager := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			Cache:      autocert.DirCache(s.cfg.ACMECacheDir),
			HostPolicy: autocert.HostWhitelist(s.cfg.RPID()),
			Email:      s.cfg.ACMEEmail,
		}
		cfg := manager.TLSConfig()
		cfg.MinVersion = tls.VersionTLS12
		return cfg, nil
	default:
		// A trusted reverse proxy terminates TLS.
		return nil, nil
	}
}

func (s *Server) routes() {
	s.mux.Handle("GET /ws", http.HandlerFunc(s.handleWS))
	s.mux.Handle("GET /api/v1/web/info", http.HandlerFunc(s.handleInfo))
	app := s.appHandler()
	s.mux.Handle("GET /app/", app)
	// Device links keep their secret in the URL fragment, which browsers
	// never send; the app reads it after load.
	s.mux.Handle("GET /link", http.HandlerFunc(s.serveIndex))
	s.mux.Handle("GET /{$}", http.RedirectHandler("/app/", http.StatusFound))
}

func (s *Server) handleInfo(w http.ResponseWriter, _ *http.Request) {
	origin, _ := s.cfg.PublicOrigin()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(Info{
		ServerName:      s.serverName,
		PublicURL:       origin,
		RPID:            s.cfg.RPID(),
		ProtocolVersion: ProtocolVersion,
		Version:         version.Short(),
	})
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	if !s.originAllowed(r.Header.Get("Origin")) {
		http.Error(w, "origin not allowed", http.StatusForbidden)
		return
	}
	ip := s.clientIP(r)
	release, ok := s.admission.admit(ip, time.Now())
	if !ok {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "too many connections", http.StatusTooManyRequests)
		return
	}
	defer release()
	remote := &net.TCPAddr{IP: net.IP(ip.AsSlice())}
	ws := websocket.Server{
		// The origin was checked above against the configured list.
		Handshake: func(*websocket.Config, *http.Request) error { return nil },
		Handler: func(conn *websocket.Conn) {
			conn.PayloadType = websocket.BinaryFrame
			conn.MaxPayloadBytes = netproto.MaxFrameSize + frameHeaderSize
			err := s.control.ServeExternalConn(r.Context(), &wsConn{Conn: conn, remote: remote})
			if err != nil {
				s.logger.Debug("web control connection rejected", zap.Error(err))
			}
		},
	}
	ws.ServeHTTP(w, r)
}

// clientIP is the peer address, or the nearest untrusted X-Forwarded-For
// hop when the peer is a configured proxy.
func (s *Server) clientIP(r *http.Request) netip.Addr {
	peer, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.IPv6Unspecified()
	}
	addr := peer.Addr().Unmap()
	if !s.trusted(addr) {
		return addr
	}
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		hop, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			return addr
		}
		hop = hop.Unmap()
		if !s.trusted(hop) {
			return hop
		}
		addr = hop
	}
	return addr
}

func (s *Server) trusted(addr netip.Addr) bool {
	for _, network := range s.proxies {
		if network.Contains(addr) {
			return true
		}
	}
	return false
}

// appHandler serves the PWA bundle. Unknown paths fall back to index.html so
// client-side routes survive reloads; hashed assets are cached immutably.
func (s *Server) appHandler() http.Handler {
	files := http.StripPrefix("/app/", http.FileServer(http.Dir(s.cfg.AppDir)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rel := strings.TrimPrefix(path.Clean(r.URL.Path), "/app")
		rel = strings.TrimPrefix(rel, "/")
		if rel == "" || rel == "index.html" {
			s.serveIndex(w, r)
			return
		}
		info, err := os.Stat(filepath.Join(s.cfg.AppDir, filepath.FromSlash(rel)))
		if err != nil || info.IsDir() {
			if path.Ext(rel) == "" {
				s.serveIndex(w, r)
				return
			}
			http.NotFound(w, r)
			return
		}
		switch {
		case rel == "sw.js" || rel == "manifest.webmanifest":
			w.Header().Set("Cache-Control", "no-cache")
		case strings.HasPrefix(rel, "assets/"):
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		if rel == "sw.js" {
			w.Header().Set("Service-Worker-Allowed", "/")
		}
		files.ServeHTTP(w, r)
	})
}

func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(w, r, filepath.Join(s.cfg.AppDir, "index.html"))
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; "+
			"style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; media-src 'self' blob:; "+
			"connect-src 'self'; worker-src 'self'; manifest-src 'self'; frame-ancestors 'none'; "+
			"base-uri 'none'; form-action 'self'; object-src 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(self), microphone=(self), display-capture=(), geolocation=()")
		h.Set("Strict-Transport-Security", "max-age=31536000")
		next.ServeHTTP(w, r)
	})
}

// wsConn reports the client address instead of x/net/websocket's origin.
type wsConn struct {
	*websocket.Conn
	remote net.Addr
}

func (c *wsConn) RemoteAddr() net.Addr { return c.remote }

// WebTransport marks the session as a mobile web app connection.
func (c *wsConn) WebTransport() bool { return true }

// admission bounds WebSocket attempts per minute and open sessions per IP.
type admission struct {
	mu        sync.Mutex
	perIP     int
	perMinute int
	entries   map[netip.Addr]*admissionEntry
	lastSweep time.Time
}

type admissionEntry struct {
	limiter *rate.Limiter
	open    int
	seen    time.Time
}

func newAdmission(perIP, perMinute int) *admission {
	return &admission{perIP: perIP, perMinute: perMinute, entries: make(map[netip.Addr]*admissionEntry)}
}

func (a *admission) admit(ip netip.Addr, now time.Time) (func(), bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sweep(now)
	entry := a.entries[ip]
	if entry == nil {
		entry = &admissionEntry{limiter: rate.NewLimiter(rate.Limit(float64(a.perMinute)/60), a.perMinute)}
		a.entries[ip] = entry
	}
	entry.seen = now
	if entry.open >= a.perIP || !entry.limiter.AllowN(now, 1) {
		return nil, false
	}
	entry.open++
	var once sync.Once
	return func() {
		once.Do(func() {
			a.mu.Lock()
			entry.open--
			entry.seen = time.Now()
			a.mu.Unlock()
		})
	}, true
}

func (a *admission) sweep(now time.Time) {
	if now.Sub(a.lastSweep) < time.Minute {
		return
	}
	a.lastSweep = now
	for ip, entry := range a.entries {
		if entry.open == 0 && now.Sub(entry.seen) > limiterIdleTTL {
			delete(a.entries, ip)
		}
	}
}
