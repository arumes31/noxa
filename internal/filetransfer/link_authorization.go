package filetransfer

import (
	"context"
	"errors"
	"net/http"
	"os"
	"time"
)

func (r *LinkRegistry) revokeAccess(allowed func(Principal, int64, string) bool) {
	r.deliveryMu.Lock()
	defer r.deliveryMu.Unlock()
	r.mu.Lock()
	for token, l := range r.links {
		if l.principal == nil || !allowed(*l.principal, l.scope, "download") {
			delete(r.links, token)
		}
	}
	var files []*os.File
	for file, l := range r.activeLinks {
		if l.principal == nil || !allowed(*l.principal, l.scope, "download") {
			files = append(files, file)
		}
	}
	r.mu.Unlock()
	for _, file := range files {
		_ = file.Close()
	}
}

func (r *LinkRegistry) withLinkAccess(ctx context.Context, token string, l link, effect func(context.Context) error) error {
	r.mu.Lock()
	guard := r.accessGuard
	r.mu.Unlock()
	if guard == nil || l.principal == nil {
		return ErrAccessRevoked
	}
	return guard(ctx, *l.principal, l.scope, "download", func(ctx context.Context) error {
		r.deliveryMu.RLock()
		defer r.deliveryMu.RUnlock()
		// Revoked/expired bearer links stay dead even if the account later
		// regains access while ServeContent still has buffered bytes.
		if _, ok := r.take(token); !ok {
			return ErrAccessRevoked
		}
		return effect(ctx)
	})
}

// ServeContent can buffer file bytes and uses optimized copy paths for files.
// Guard the response writer itself so every header/body write shares a current
// policy lease. Omitting ReaderFrom keeps body writes bounded to copy chunks.
type linkResponseWriter struct {
	http.ResponseWriter
	registry *LinkRegistry
	token    string
	link     link
	ctx      context.Context
	sent     bool
	err      error
}

func (w *linkResponseWriter) write(effect func() error) error {
	return w.registry.withLinkAccess(w.ctx, w.token, w.link, func(context.Context) error {
		if err := http.NewResponseController(w.ResponseWriter).SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return err
		}
		return effect()
	})
}

func (w *linkResponseWriter) WriteHeader(status int) {
	if w.sent || w.err != nil {
		return
	}
	w.err = w.write(func() error { w.ResponseWriter.WriteHeader(status); w.sent = true; return nil })
	if w.err != nil && !w.sent {
		// File metadata prepared by ServeContent must not survive denial.
		for key := range w.Header() {
			w.Header().Del(key)
		}
		w.Header().Set("Cache-Control", "private, no-store")
		http.Error(w.ResponseWriter, "unknown or expired link", http.StatusNotFound)
		w.sent = true
	}
}

func (w *linkResponseWriter) Write(body []byte) (int, error) {
	if !w.sent {
		w.WriteHeader(http.StatusOK)
	}
	if w.err != nil {
		return 0, w.err
	}
	var n int
	w.err = w.write(func() error { var err error; n, err = w.ResponseWriter.Write(body); return err })
	return n, w.err
}
