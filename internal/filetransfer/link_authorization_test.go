package filetransfer

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type deadlineErrorResponse struct{ *httptest.ResponseRecorder }

func (w deadlineErrorResponse) SetWriteDeadline(time.Time) error {
	return errors.New("deadline unavailable")
}

func TestRoleDownloadDeadlineFailureHidesMetadata(t *testing.T) {
	r, path := newTestLinkRegistry(t, []byte("private contents"))
	token, _, err := r.create(path, "private-name.txt", &Principal{UserID: 2, SessionID: "session"}, 7)
	if err != nil {
		t.Fatal(err)
	}
	r.accessGuard = func(context.Context, Principal, int64, string, func(context.Context) error) error {
		return ErrAccessRevoked
	}
	response := deadlineErrorResponse{httptest.NewRecorder()}
	r.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/dl/"+token, nil))
	if response.Code != http.StatusNotFound || response.Header().Get("Content-Disposition") != "" {
		t.Fatalf("denied link exposed metadata: %d %+v", response.Code, response.Header())
	}
}

func TestRoleDownloadLinkDenialHidesFileMetadata(t *testing.T) {
	r, path := newTestLinkRegistry(t, []byte("private contents"))
	token, _, err := r.create(path, "private-name.txt", &Principal{UserID: 2, SessionID: "session"}, 7)
	if err != nil {
		t.Fatal(err)
	}
	r.accessGuard = func(context.Context, Principal, int64, string, func(context.Context) error) error {
		return ErrAccessRevoked
	}
	response := httptest.NewRecorder()
	r.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/dl/"+token, nil))
	if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), "private") || response.Header().Get("Content-Disposition") != "" || response.Header().Get("Last-Modified") != "" {
		t.Fatalf("denied link disclosed file metadata: %+v %s", response.Header(), response.Body.String())
	}
}

func TestRoleDownloadLinkRechecksEveryBodyWrite(t *testing.T) {
	content := bytes.Repeat([]byte("private"), 32*1024)
	r, path := newTestLinkRegistry(t, content)
	token, _, err := r.create(path, "private-name.txt", &Principal{UserID: 2, SessionID: "session"}, 7)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	r.accessGuard = func(ctx context.Context, p Principal, scope int64, direction string, effect func(context.Context) error) error {
		if p.UserID != 2 || p.SessionID != "session" || scope != 7 || direction != "download" {
			return ErrAccessRevoked
		}
		calls++
		if calls > 2 {
			return ErrAccessRevoked
		}
		return effect(ctx)
	}
	response := httptest.NewRecorder()
	r.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/dl/"+token, nil))
	if response.Code != http.StatusOK || response.Body.Len() == 0 || response.Body.Len() > 32*1024 || response.Body.Len() == len(content) {
		t.Fatalf("revoked stream body size: %d", response.Body.Len())
	}
	r.revokeAccess(func(Principal, int64, string) bool { return false })
	r.accessGuard = func(ctx context.Context, p Principal, scope int64, direction string, effect func(context.Context) error) error {
		return effect(ctx)
	}
	response = httptest.NewRecorder()
	r.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/dl/"+token, nil))
	if response.Code != http.StatusNotFound {
		t.Fatal("restored permissions revived revoked bearer link")
	}
}

func TestLinkRevocationDrainsAuthorizedWrite(t *testing.T) {
	r, path := newTestLinkRegistry(t, []byte("private contents"))
	token, _, err := r.create(path, "private.txt", &Principal{UserID: 2, SessionID: "session"}, 7)
	if err != nil {
		t.Fatal(err)
	}
	r.accessGuard = func(ctx context.Context, _ Principal, _ int64, _ string, effect func(context.Context) error) error {
		return effect(ctx)
	}
	l, _ := r.take(token)
	revoked := make(chan struct{})
	err = r.withLinkAccess(t.Context(), token, l, func(context.Context) error {
		go func() { r.revokeAccess(func(Principal, int64, string) bool { return false }); close(revoked) }()
		select {
		case <-revoked:
			t.Error("revocation returned while an authorized write could still deliver bytes")
		case <-time.After(50 * time.Millisecond):
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-revoked:
	case <-time.After(3 * time.Second):
		t.Fatal("revocation did not drain")
	}
	if err := r.withLinkAccess(t.Context(), token, l, func(context.Context) error { t.Error("write after revocation"); return nil }); !errors.Is(err, ErrAccessRevoked) {
		t.Fatal(err)
	}
}
