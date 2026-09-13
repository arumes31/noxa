package permissions

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// mediaPermissionDB snapshots a permission when the query begins and can
// pause that result across an invalidation, without an external database.
type mediaPermissionDB struct {
	mu       sync.Mutex
	group    bool
	value    int64
	queries  int
	queryErr error
	started  chan struct{}
	release  chan struct{}
}

type mediaPermissionConnector struct{ state *mediaPermissionDB }

func (c mediaPermissionConnector) Connect(context.Context) (driver.Conn, error) {
	return mediaPermissionConn(c), nil
}
func (c mediaPermissionConnector) Driver() driver.Driver { return mediaPermissionDriver{} }

type mediaPermissionDriver struct{}

func (mediaPermissionDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("use connector")
}

type mediaPermissionConn struct{ state *mediaPermissionDB }

func (mediaPermissionConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare unsupported")
}
func (mediaPermissionConn) Close() error { return nil }
func (mediaPermissionConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions unsupported")
}
func (c mediaPermissionConn) QueryContext(ctx context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	s := c.state
	match := strings.Contains(query, "FROM client_permissions cp") && strings.Contains(query, "IS NULL")
	if s.group {
		match = strings.Contains(query, "FROM server_group_permissions sgp")
	}
	if !match {
		return &mediaPermissionRows{}, nil
	}
	s.mu.Lock()
	value := s.value
	queryErr := s.queryErr
	s.queries++
	started, release := s.started, s.release
	s.started = nil
	s.mu.Unlock()
	if queryErr != nil {
		return nil, queryErr
	}
	if started != nil {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &mediaPermissionRows{row: []driver.Value{string(PermissionKeyClientVideoPublish), value, int64(0), false, false}}, nil
}

type mediaPermissionRows struct{ row []driver.Value }

func (*mediaPermissionRows) Columns() []string {
	return []string{"permission_key", "value", "grant_value", "skip_flag", "negate_flag"}
}
func (*mediaPermissionRows) Close() error { return nil }
func (r *mediaPermissionRows) Next(dest []driver.Value) error {
	if r.row == nil {
		return io.EOF
	}
	copy(dest, r.row)
	r.row = nil
	return nil
}

type mediaPermissionStore struct{ db *sql.DB }

func (s mediaPermissionStore) DB() *sql.DB { return s.db }
func newMediaPermissionLoader(t *testing.T, state *mediaPermissionDB) *Loader {
	t.Helper()
	db := sql.OpenDB(mediaPermissionConnector{state})
	t.Cleanup(func() { _ = db.Close() })
	return NewLoader(mediaPermissionStore{db}, nil)
}

func TestGuestPermissionCacheReusesRowsAndInvalidates(t *testing.T) {
	state := &mediaPermissionDB{group: true, value: 1}
	loader := newMediaPermissionLoader(t, state)
	for range 50 {
		if _, err := loader.LoadGroupPermissions(t.Context(), 7); err != nil {
			t.Fatal(err)
		}
	}
	state.mu.Lock()
	queries := state.queries
	state.value = 0
	state.mu.Unlock()
	if queries != 1 {
		t.Errorf("50 guest packet checks executed %d SQL queries, want 1", queries)
	}
	loader.InvalidateAll()
	set, err := loader.LoadGroupPermissions(t.Context(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := set.Get(PermissionKeyClientVideoPublish); !ok || p.Value != 0 {
		t.Fatal("revocation not visible immediately after invalidation")
	}
}

func TestPermissionInvalidationDiscardsInflightSnapshot(t *testing.T) {
	for _, mode := range []string{"group_all", "client_all", "client_scoped"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			started, release := make(chan struct{}), make(chan struct{})
			state := &mediaPermissionDB{group: mode == "group_all", value: 1, started: started, release: release}
			loader := newMediaPermissionLoader(t, state)
			load := func() (int, error) {
				var set PermissionSet
				if state.group {
					var err error
					set, err = loader.LoadGroupPermissions(ctx, 7)
					if err != nil {
						return 0, err
					}
				} else {
					tp, err := loader.LoadForClient(ctx, 7, 0)
					if err != nil {
						return 0, err
					}
					set, _ = tp.Get(TierClientSpecific)
				}
				p, ok := set.Get(PermissionKeyClientVideoPublish)
				if !ok {
					return 0, errors.New("missing permission")
				}
				return p.Value, nil
			}
			type result struct {
				value int
				err   error
			}
			done := make(chan result, 1)
			go func() { value, err := load(); done <- result{value, err} }()
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal("permission query did not start")
			}
			state.mu.Lock()
			state.value = 0
			state.mu.Unlock()
			if mode == "client_scoped" {
				loader.Invalidate(7, 0)
			} else {
				loader.InvalidateAll()
			}
			close(release)
			var got result
			select {
			case got = <-done:
			case <-ctx.Done():
				t.Fatal("invalidated permission load did not finish")
			}
			if got.err != nil {
				t.Fatal(got.err)
			}
			if got.value != 0 {
				t.Error("in-flight permission read returned a revoked grant")
			}
			value, err := load()
			if err != nil {
				t.Fatal(err)
			}
			if value != 0 {
				t.Error("in-flight read repopulated the cache with a revoked grant")
			}
		})
	}
}

func TestGuestPermissionCacheExpiresAndRemainsBounded(t *testing.T) {
	state := &mediaPermissionDB{group: true, value: 1}
	loader := newMediaPermissionLoader(t, state)
	if _, err := loader.LoadGroupPermissions(t.Context(), 7); err != nil {
		t.Fatal(err)
	}
	loader.cacheMu.Lock()
	entry := loader.groupCache[7]
	entry.expires = time.Now().Add(-time.Second)
	loader.groupCache[7] = entry
	loader.cacheMu.Unlock()
	state.mu.Lock()
	state.value = 0
	state.mu.Unlock()
	set, err := loader.LoadGroupPermissions(t.Context(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := set.Get(PermissionKeyClientVideoPublish); !ok || p.Value != 0 {
		t.Fatal("expired grant remained effective")
	}
	for groupID := int64(0); groupID <= maxCachedGroups; groupID++ {
		if _, err := loader.LoadGroupPermissions(t.Context(), groupID); err != nil {
			t.Fatal(err)
		}
	}
	loader.cacheMu.RLock()
	count := len(loader.groupCache)
	loader.cacheMu.RUnlock()
	if count > maxCachedGroups {
		t.Fatalf("group cache retained %d entries above its bound", count)
	}
}

func TestMediaPermissionCacheDoesNotRetainFailures(t *testing.T) {
	for _, group := range []bool{false, true} {
		name := "client"
		if group {
			name = "group"
		}
		t.Run(name, func(t *testing.T) {
			queryErr := errors.New("permission database unavailable")
			state := &mediaPermissionDB{group: group, value: 0, queryErr: queryErr}
			loader := newMediaPermissionLoader(t, state)
			load := func(ctx context.Context) error {
				if group {
					_, err := loader.LoadGroupPermissions(ctx, 7)
					return err
				}
				_, err := loader.LoadForClient(ctx, 7, 0)
				return err
			}
			if err := load(t.Context()); !errors.Is(err, queryErr) {
				t.Fatalf("SQL error = %v", err)
			}
			state.mu.Lock()
			state.queryErr = nil
			started, release := make(chan struct{}), make(chan struct{})
			state.started, state.release = started, release
			state.mu.Unlock()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- load(ctx) }()
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal("retry after SQL error did not reach database")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("canceled SQL read = %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("canceled SQL read did not finish")
			}
			if err := load(t.Context()); err != nil {
				t.Fatalf("retry after canceled query: %v", err)
			}
			state.mu.Lock()
			queries := state.queries
			state.mu.Unlock()
			if queries != 3 {
				t.Fatalf("query count = %d, want failure, cancellation, successful retry", queries)
			}
		})
	}
}
