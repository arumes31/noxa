package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	updateReadyAddressEnv = "NOXA_UPDATE_READY_ADDRESS"
	updateReadyNonceEnv   = "NOXA_UPDATE_READY_NONCE"
	updateReadyIOTimeout  = 2 * time.Second
)

// Serialize acknowledgement and legacy ownership transfer: a duplicate frontend
// call must not cancel a helper after another call already acknowledged readiness.
var updateStartupConfirmationMu sync.Mutex

type updateReadiness struct {
	listener net.Listener
	nonce    string
}

func newUpdateReadiness() (*updateReadiness, error) {
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("create update startup challenge: %w", err)
	}
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen for update startup: %w", err)
	}
	return &updateReadiness{listener: listener, nonce: hex.EncodeToString(nonce)}, nil
}

func (s *updateReadiness) address() string { return s.listener.Addr().String() }

func (s *updateReadiness) close() { _ = s.listener.Close() }

// wait accepts only the child that received this fresh native-only challenge.
// An unrelated loopback connection cannot make a failed replacement look ready.
func (s *updateReadiness) wait(ctx context.Context, childPID int) error {
	stop := context.AfterFunc(ctx, s.close)
	defer stop()
	nonce, err := hex.DecodeString(s.nonce)
	if err != nil {
		return err
	}
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		accepted := acceptUpdateReadiness(ctx, conn, nonce, childPID)
		_ = conn.Close()
		if accepted {
			return nil
		}
	}
}

func acceptUpdateReadiness(ctx context.Context, conn net.Conn, nonce []byte, childPID int) bool {
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if err := conn.SetDeadline(time.Now().Add(updateReadyIOTimeout)); err != nil {
		return false
	}
	var message [40]byte
	if _, err := io.ReadFull(conn, message[:]); err != nil ||
		subtle.ConstantTimeCompare(message[:32], nonce) != 1 ||
		binary.BigEndian.Uint64(message[32:]) != uint64(childPID) { // #nosec G115 -- OS process IDs are positive.
		return false
	}
	_, err := conn.Write([]byte{1})
	return err == nil
}

func updateChildEnvironment(env []string, address, nonce string) []string {
	clean := make([]string, 0, len(env)+2)
	for _, item := range env {
		key, _, _ := strings.Cut(item, "=")
		if !strings.EqualFold(key, updateReadyAddressEnv) && !strings.EqualFold(key, updateReadyNonceEnv) {
			clean = append(clean, item)
		}
	}
	return append(clean, updateReadyAddressEnv+"="+address, updateReadyNonceEnv+"="+nonce)
}

// startedAfterUpdate records at process start, before ConfirmUpdateStartup
// clears the readiness variables, whether the updater launched this process.
var startedAfterUpdate = updateLaunchedProcess(os.Getenv)

func updateLaunchedProcess(getenv func(string) string) bool {
	return getenv(updateReadyAddressEnv) != "" && getenv(updateReadyNonceEnv) != ""
}

// StartedAfterUpdate reports whether this is the first start of a freshly
// installed update, so the frontend can play its one-time intro. It carries
// no address, nonce or path.
func (a *App) StartedAfterUpdate() bool { return startedAfterUpdate }

// ConfirmUpdateStartup is called after the frontend's modules and native
// settings have initialized. It exposes no callback address, path, or command
// to JavaScript, and does nothing on an ordinary/manual application launch.
func (a *App) ConfirmUpdateStartup() error {
	updateStartupConfirmationMu.Lock()
	defer updateStartupConfirmationMu.Unlock()
	address, nonce := os.Getenv(updateReadyAddressEnv), os.Getenv(updateReadyNonceEnv)
	if err := prepareUpdateFrontendHandoff(a); err != nil {
		return err
	}
	_ = os.Unsetenv(updateReadyAddressEnv)
	_ = os.Unsetenv(updateReadyNonceEnv)
	if address == "" && nonce == "" {
		return nil
	}
	// A lost acknowledgement does not prove the legacy parent rejected us.
	// Keep the armed supervisor and independent rollback snapshot: the exact
	// parent-exit waiter resolves ownership without inventing frontend readiness.
	err := confirmUpdateReadiness(address, nonce, os.Getpid())
	completeUpdateFrontendHandoff(a)
	return err
}

func confirmUpdateReadiness(address, nonce string, processID int) error {
	host, port, err := net.SplitHostPort(address)
	number, portErr := strconv.ParseUint(port, 10, 16)
	challenge, nonceErr := hex.DecodeString(nonce)
	if err != nil || host != "127.0.0.1" || portErr != nil || number == 0 || nonceErr != nil || len(challenge) != 32 || processID <= 0 {
		return fmt.Errorf("invalid update startup challenge")
	}
	ctx, cancel := context.WithTimeout(context.Background(), updateReadyIOTimeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp4", address)
	if err != nil {
		return fmt.Errorf("confirm update startup: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(updateReadyIOTimeout)); err != nil {
		return err
	}
	var message [40]byte
	copy(message[:32], challenge)
	binary.BigEndian.PutUint64(message[32:], uint64(processID)) // #nosec G115 -- positive process ID was checked above.
	if _, err := conn.Write(message[:]); err != nil {
		return err
	}
	var ack [1]byte
	if _, err := io.ReadFull(conn, ack[:]); err != nil {
		return err
	}
	if ack[0] != 1 {
		return fmt.Errorf("update startup confirmation was rejected")
	}
	return nil
}
