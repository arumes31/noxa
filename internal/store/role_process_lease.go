package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrRoleProcessBusy means another server or offline operator command holds
// the database-wide roles-v1 process lease.
var ErrRoleProcessBusy = errors.New("roles-v1 database is already in use by a server or operator command")

var errRoleProcessLeaseLost = errors.New("roles-v1 database process lease was lost")

// Separate from the migration lock: the lease spans the whole serving process
// so offline registration cannot mutate assignments behind Authority's cache.
const roleProcessLockID int64 = 0x6e6f7861726f6c65 // "noxarole"

// RoleProcessLease pins one PostgreSQL session and its exclusive advisory lock.
// Use a separate pool so even a server configured with one store connection can
// continue to query while holding the lease.
type RoleProcessLease struct {
	mu         sync.Mutex
	db         *sql.DB
	conn       *sql.Conn
	backendPID int64
	closed     bool
}

// AcquireRoleProcessLease fails immediately when another server or offline
// operator command is using the same database. Hold the lease through all
// migrations, policy loads and writes, and (for the server) until shutdown.
func AcquireRoleProcessLease(ctx context.Context, databaseURL string) (*RoleProcessLease, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("opening role process lease database: %w", err)
	}
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connecting role process lease: %w", err)
	}
	var acquired bool
	var backendPID int64
	err = conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1), pg_backend_pid()`, roleProcessLockID).Scan(&acquired, &backendPID)
	if err != nil || !acquired {
		_ = conn.Close()
		_ = db.Close()
		if err != nil {
			return nil, fmt.Errorf("acquiring role process lease: %w", err)
		}
		return nil, ErrRoleProcessBusy
	}
	return &RoleProcessLease{db: db, conn: conn, backendPID: backendPID}, nil
}

// Check detects a dropped or replaced PostgreSQL session. A serving process
// must stop accepting work if this fails because another operator could have
// acquired the now-unheld lease and changed the policy outside its cache.
func (l *RoleProcessLease) Check(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return errRoleProcessLeaseLost
	}
	var backendPID int64
	var held bool
	if err := l.conn.QueryRowContext(ctx, `SELECT pg_backend_pid(), EXISTS (
		SELECT 1 FROM pg_locks WHERE locktype='advisory' AND pid=pg_backend_pid()
			AND classid=$1::oid AND objid=$2::oid AND objsubid=1
			AND mode='ExclusiveLock' AND granted
	)`, roleProcessLockID>>32, roleProcessLockID&0xffffffff).Scan(&backendPID, &held); err != nil {
		return fmt.Errorf("checking role process lease: %w", err)
	}
	if backendPID != l.backendPID || !held {
		return errRoleProcessLeaseLost
	}
	return nil
}

// Close releases the advisory lock and closes the dedicated connection.
func (l *RoleProcessLease) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var released bool
	unlockErr := l.conn.QueryRowContext(ctx, `SELECT pg_advisory_unlock($1)`, roleProcessLockID).Scan(&released)
	closeErr := errors.Join(l.conn.Close(), l.db.Close())
	if unlockErr != nil {
		return errors.Join(fmt.Errorf("releasing role process lease: %w", unlockErr), closeErr)
	}
	if !released {
		return errors.Join(errRoleProcessLeaseLost, closeErr)
	}
	return closeErr
}
