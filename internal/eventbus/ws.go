// ws.go provides the request guardrails for the roles-v1 event stream.
package eventbus

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"go.uber.org/zap"
	"golang.org/x/net/websocket"

	"noxa/internal/auth"
)

type authFailureRecorder interface {
	IncAuthFailure(transport, reason string)
}

const (
	// wsWriteTimeout bounds a single frame write. A consumer whose TCP window
	// is closed must not pin the goroutine forever; the bus drop policy handles
	// the backlog, this handles the socket.
	wsWriteTimeout = 10 * time.Second
	// Incoming frames are only drained to detect disconnects. Keeping the limit
	// small prevents an authenticated peer from making the server buffer the
	// websocket package's 32 MiB default.
	wsMaxIncomingBytes = 4 << 10

	maxWSConnections   = 64
	maxWSAuthAttempts  = 20
	wsAuthWindow       = time.Minute
	maxWSAuthBuckets   = 4096
	maxBasicHeaderSize = 2048
	maxUniqueIDSize    = 128
	maxPasswordSize    = 1024
	maxRawQuerySize    = 4096
	maxTypeQuerySize   = 1024
	maxTypeSize        = 64
	maxTypeFilters     = 32
)

var errInvalidEventFilter = errors.New("invalid event type filter")

type wsHandlerConfig struct {
	maxConnections  int
	maxAuthAttempts int
	authWindow      time.Duration
	maxAuthBuckets  int
	now             func() time.Time
	loginLimiter    *auth.LoginFailureLimiter
	failures        authFailureRecorder
	roleBackend     RoleBackend
	streamLifetime  time.Duration
}

func newWSHandler(bus *Bus, logger *zap.Logger, cfg wsHandlerConfig) http.Handler {
	if logger == nil {
		logger = zap.NewNop()
	}
	if cfg.maxConnections < 1 {
		cfg.maxConnections = 1
	}
	limiter := newWSAuthLimiter(cfg)
	connections := make(chan struct{}, cfg.maxConnections)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setWSHeaders(w.Header())
		w.Header().Set("Noxa-Authorization-Model", "roles-v1")
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if bus == nil || cfg.roleBackend == nil {
			http.Error(w, "event stream unavailable", http.StatusServiceUnavailable)
			return
		}
		if len(r.Header.Get("Authorization")) > maxBasicHeaderSize {
			recordWSAuthFailure(cfg.failures, "malformed_metadata")
			http.Error(w, "request header too large", http.StatusRequestHeaderFieldsTooLarge)
			return
		}
		if len(r.URL.RawQuery) > maxRawQuerySize {
			http.Error(w, "request URI too long", http.StatusRequestURITooLong)
			return
		}
		models := r.Header.Values("Noxa-Authorization-Model")
		if len(models) != 1 || models[0] != "roles-v1" {
			http.Error(w, "roles-v1 authorization required", http.StatusPreconditionFailed)
			return
		}
		types, err := requestTypes(r.URL.RawQuery)
		if err != nil {
			http.Error(w, "roles-v1 requires role_snapshot, optionally speaking_changed", http.StatusBadRequest)
			return
		}
		wantSpeaking, err := roleWSTypes(types)
		if err != nil {
			http.Error(w, "roles-v1 requires role_snapshot, optionally speaking_changed", http.StatusBadRequest)
			return
		}
		allowed, retryAfter := limiter.allow(r.RemoteAddr)
		if !allowed {
			recordWSAuthFailure(cfg.failures, "locked_out")
			w.Header().Set("Retry-After", retryAfterSeconds(retryAfter))
			http.Error(w, "too many authentication attempts", http.StatusTooManyRequests)
			return
		}

		user, password, ok := r.BasicAuth()
		if !ok || user == "" || len(user) > maxUniqueIDSize || len(password) > maxPasswordSize {
			recordWSAuthFailure(cfg.failures, "malformed_metadata")
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}

		var attempt *auth.LoginAttempt
		principalScope := auth.LoginFailureScope("", user)
		if cfg.loginLimiter != nil {
			sourceScope := auth.LoginFailureScope("ws:"+remoteIPKey(r.RemoteAddr), "")
			var allowed bool
			attempt, allowed = cfg.loginLimiter.Reserve(sourceScope, principalScope)
			if !allowed {
				recordWSAuthFailure(cfg.failures, "locked_out")
				http.Error(w, "too many authentication attempts", http.StatusTooManyRequests)
				return
			}
			defer attempt.Cancel()
		}

		ctx, cancel := context.WithTimeout(r.Context(), wsWriteTimeout)
		principal, err := cfg.roleBackend.AuthenticateIntegration(ctx, user, password, remoteIPKey(r.RemoteAddr))
		cancel()
		if errors.Is(err, auth.ErrIntegrationDenied) {
			if attempt != nil {
				attempt.Fail()
			}
			recordWSAuthFailure(cfg.failures, "invalid_credentials")
			logger.Warn("event stream login refused",
				zap.String("unique_id", user), zap.String("remote", r.RemoteAddr))
			http.Error(w, "invalid credentials", http.StatusUnauthorized)
			return
		}
		if err != nil {
			logger.Warn("event stream auth error", zap.Error(err))
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if attempt != nil {
			attempt.Succeed(principalScope)
		}
		if !isWebSocketUpgrade(r.Header) {
			http.Error(w, "websocket upgrade required", http.StatusBadRequest)
			return
		}
		if err := validateWSOrigin(r); err != nil {
			http.Error(w, "websocket origin rejected", http.StatusForbidden)
			return
		}
		select {
		case connections <- struct{}{}:
			defer func() { <-connections }()
		default:
			w.Header().Set("Retry-After", "5")
			http.Error(w, "too many event streams", http.StatusTooManyRequests)
			return
		}

		transport := &roleWSResponseWriter{ResponseWriter: w}
		wsServer := websocket.Server{
			Handshake: sameOriginOrAbsent,
			Handler: func(conn *websocket.Conn) {
				conn.MaxPayloadBytes = wsMaxIncomingBytes
				defer func() {
					if err := conn.Close(); err != nil {
						logger.Debug("closing event stream websocket failed", zap.Error(err))
					}
				}()
				serveRoleStream(r.Context(), bus, conn, transport.conn, cfg.roleBackend, principal, wantSpeaking, cfg.streamLifetime)
			},
		}
		wsServer.Header = w.Header().Clone()
		wsServer.ServeHTTP(transport, r)
	})
}

func recordWSAuthFailure(recorder authFailureRecorder, reason string) {
	if recorder != nil {
		recorder.IncAuthFailure("ws", reason)
	}
}

func requestTypes(rawQuery string) ([]string, error) {
	query, err := url.ParseQuery(rawQuery)
	if err != nil || len(query["types"]) > 1 {
		return nil, errInvalidEventFilter
	}
	return parseTypes(query.Get("types"))
}

// parseTypes splits the ?types= filter; an empty parameter means all events.
func parseTypes(raw string) ([]string, error) {
	if len(raw) > maxTypeQuerySize {
		return nil, errInvalidEventFilter
	}
	var out []string
	seen := make(map[string]struct{})
	for _, t := range strings.Split(raw, ",") {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if len(t) > maxTypeSize || !utf8.ValidString(t) || strings.IndexFunc(t, unicode.IsControl) >= 0 {
			return nil, errInvalidEventFilter
		}
		if _, duplicate := seen[t]; duplicate {
			continue
		}
		if len(out) >= maxTypeFilters {
			return nil, errInvalidEventFilter
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out, nil
}

func setWSHeaders(header http.Header) {
	header.Set("Cache-Control", "no-store")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("WWW-Authenticate", `Basic realm="noxa events"`)
	header.Set("X-Content-Type-Options", "nosniff")
}

func isWebSocketUpgrade(header http.Header) bool {
	if !strings.EqualFold(strings.TrimSpace(header.Get("Upgrade")), "websocket") {
		return false
	}
	for _, token := range strings.Split(header.Get("Connection"), ",") {
		if strings.EqualFold(strings.TrimSpace(token), "upgrade") {
			return true
		}
	}
	return false
}

func validateWSOrigin(r *http.Request) error {
	raw := r.Header.Get("Origin")
	if raw == "" {
		return nil
	}
	origin, err := url.ParseRequestURI(raw)
	if err != nil || origin.Host == "" || origin.User != nil {
		return errors.New("invalid websocket origin")
	}
	if !strings.EqualFold(origin.Host, r.Host) {
		return errors.New("websocket origin does not match request host")
	}
	wantScheme := "http"
	if r.TLS != nil {
		wantScheme = "https"
	}
	if !strings.EqualFold(origin.Scheme, wantScheme) {
		return errors.New("websocket origin scheme does not match request")
	}
	return nil
}

// sameOriginOrAbsent prevents a browser on an unrelated site from opening an
// authenticated stream. Non-browser bot clients commonly omit Origin and are
// allowed; browser clients must use the exact listener authority.
func sameOriginOrAbsent(config *websocket.Config, r *http.Request) error {
	if err := validateWSOrigin(r); err != nil {
		return err
	}
	origin, err := websocket.Origin(config, r)
	if err != nil {
		return err
	}
	if origin == nil {
		return nil
	}
	if !strings.EqualFold(origin.Host, r.Host) {
		return errors.New("websocket origin does not match request host")
	}
	wantScheme := "http"
	if r.TLS != nil {
		wantScheme = "https"
	}
	if !strings.EqualFold(origin.Scheme, wantScheme) {
		return errors.New("websocket origin scheme does not match request")
	}
	config.Origin = origin
	return nil
}

type wsAuthBucket struct {
	started  time.Time
	attempts int
}

type wsAuthLimiter struct {
	mu         sync.Mutex
	buckets    map[string]wsAuthBucket
	limit      int
	window     time.Duration
	maxBuckets int
	now        func() time.Time
}

func newWSAuthLimiter(cfg wsHandlerConfig) *wsAuthLimiter {
	if cfg.maxAuthAttempts < 1 {
		cfg.maxAuthAttempts = 1
	}
	if cfg.authWindow <= 0 {
		cfg.authWindow = time.Minute
	}
	if cfg.maxAuthBuckets < 1 {
		cfg.maxAuthBuckets = 1
	}
	if cfg.now == nil {
		cfg.now = time.Now
	}
	return &wsAuthLimiter{
		buckets:    make(map[string]wsAuthBucket),
		limit:      cfg.maxAuthAttempts,
		window:     cfg.authWindow,
		maxBuckets: cfg.maxAuthBuckets,
		now:        cfg.now,
	}
}

func (l *wsAuthLimiter) allow(remoteAddr string) (bool, time.Duration) {
	now := l.now()
	key := remoteIPKey(remoteAddr)
	l.mu.Lock()
	defer l.mu.Unlock()

	bucket, exists := l.buckets[key]
	if exists && now.Sub(bucket.started) >= l.window {
		delete(l.buckets, key)
		exists = false
	}
	if !exists && len(l.buckets) >= l.maxBuckets {
		l.pruneLocked(now)
		if len(l.buckets) >= l.maxBuckets {
			return false, l.window
		}
	}
	if !exists {
		bucket = wsAuthBucket{started: now}
	}
	if bucket.attempts >= l.limit {
		return false, positiveDuration(l.window - now.Sub(bucket.started))
	}
	bucket.attempts++
	l.buckets[key] = bucket
	return true, 0
}

func (l *wsAuthLimiter) pruneLocked(now time.Time) {
	for key, bucket := range l.buckets {
		if now.Sub(bucket.started) >= l.window {
			delete(l.buckets, key)
		}
	}
}

func remoteIPKey(remoteAddr string) string {
	if addrPort, err := netip.ParseAddrPort(remoteAddr); err == nil {
		return addrPort.Addr().Unmap().String()
	}
	if addr, err := netip.ParseAddr(remoteAddr); err == nil {
		return addr.Unmap().String()
	}
	return "unknown"
}

func positiveDuration(duration time.Duration) time.Duration {
	if duration <= 0 {
		return time.Second
	}
	return duration
}

func retryAfterSeconds(duration time.Duration) string {
	seconds := (positiveDuration(duration) + time.Second - 1) / time.Second
	return strconv.FormatInt(int64(seconds), 10)
}
