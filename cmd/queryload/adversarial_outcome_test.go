package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"testing/synctest"
	"time"
)

func TestQueryOutcomeOneSuccessDoesNotHideLaterRejection(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client, peer := net.Pipe()
		defer func() { _ = peer.Close() }()
		tracked := &closeTrackingConn{Conn: client}
		go func() {
			reader := bufio.NewReader(peer)
			for _, status := range []string{"error id=0 msg=ok", "error id=256 msg=denied"} {
				if _, err := reader.ReadString('\n'); err != nil {
					return
				}
				if _, err := fmt.Fprintln(peer, status); err != nil {
					return
				}
			}
		}()
		ticker := &manualQueryloadTicker{ch: make(chan time.Time, 2)}
		var reported *result
		done := make(chan error, 1)
		go func() {
			done <- runWithDeps(context.Background(), options{connections: 1, rate: 10, duration: time.Second, command: "clientlist"}, queryloadDeps{
				dialAndLogin: func(options) (net.Conn, *bufio.Reader, error) { return tracked, bufio.NewReader(tracked), nil },
				newTicker:    func(time.Duration) queryloadTicker { return ticker }, now: time.Now,
				report: func(_ options, r *result) { reported = r },
			})
		}()
		ticker.ch <- time.Now()
		ticker.ch <- time.Now()
		synctest.Wait()
		time.Sleep(time.Second)
		if err := <-done; err == nil {
			t.Fatal("one successful command hid a later rejection")
		}
		if reported == nil || reported.ok.Load() != 1 || reported.failed.Load() != 1 || reported.canceled.Load() != 0 {
			t.Fatalf("report did not preserve one success and one rejection: %+v", reported)
		}
		if !tracked.closed.Load() || !ticker.stopped.Load() {
			t.Fatal("runner leaked connection or ticker")
		}
	})
}

func TestQueryOutcomeActiveAndQueuedShutdownAccounting(t *testing.T) {
	for _, callerCancel := range []bool{false, true} {
		name := "normal_duration"
		if callerCancel {
			name = "caller_cancel"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				client, peer := net.Pipe()
				defer func() { _ = peer.Close() }()
				tracked := &closeTrackingConn{Conn: client}
				secondReceived := make(chan struct{})
				go func() {
					reader := bufio.NewReader(peer)
					if _, err := reader.ReadString('\n'); err != nil {
						return
					}
					if _, err := fmt.Fprintln(peer, "error id=0 msg=ok"); err != nil {
						return
					}
					if _, err := reader.ReadString('\n'); err != nil {
						return
					}
					close(secondReceived)
					_, _ = io.Copy(io.Discard, peer) // second response remains pending until Close
				}()
				ticker := &manualQueryloadTicker{ch: make(chan time.Time, 4)}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				var reported *result
				done := make(chan error, 1)
				go func() {
					done <- runWithDeps(ctx, options{connections: 1, rate: 10, duration: time.Second, command: "clientlist"}, queryloadDeps{
						dialAndLogin: func(options) (net.Conn, *bufio.Reader, error) { return tracked, bufio.NewReader(tracked), nil },
						newTicker:    func(time.Duration) queryloadTicker { return ticker }, now: time.Now,
						report: func(_ options, r *result) { reported = r },
					})
				}()
				ticker.ch <- time.Now()
				ticker.ch <- time.Now()
				<-secondReceived
				ticker.ch <- time.Now()
				ticker.ch <- time.Now()
				synctest.Wait()
				if callerCancel {
					cancel()
				} else {
					time.Sleep(time.Second)
				}
				err := <-done
				if callerCancel && !errors.Is(err, context.Canceled) {
					t.Fatalf("caller cancellation = %v", err)
				}
				if !callerCancel && err != nil {
					t.Fatalf("normal duration shutdown = %v", err)
				}
				if reported == nil || reported.ok.Load() != 1 || reported.failed.Load() != 0 || reported.canceled.Load() != 3 {
					t.Fatalf("active/queued cancellation accounting: %+v", reported)
				}
				if !tracked.closed.Load() || !ticker.stopped.Load() {
					t.Fatal("runner leaked connection or ticker")
				}
			})
		})
	}
}

func TestQueryOutcomeCanceledCallerDoesNotBecomeSetupFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := runWithDeps(ctx, options{connections: 1, rate: 1, duration: time.Second}, queryloadDeps{
		dialAndLogin: func(options) (net.Conn, *bufio.Reader, error) { return nil, nil, errors.New("unrelated setup error") },
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled caller returned %v, want context.Canceled", err)
	}
}

func TestQueryOutcomeCancellationDuringSetupClosesOpenedWorkers(t *testing.T) {
	for _, failCurrentDial := range []bool{false, true} {
		t.Run(fmt.Sprintf("dial_fails=%t", failCurrentDial), func(t *testing.T) {
			client, peer := net.Pipe()
			defer func() { _ = peer.Close() }()
			tracked := &closeTrackingConn{Conn: client}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			err := runWithDeps(ctx, options{connections: 3, rate: 1, duration: time.Second}, queryloadDeps{
				dialAndLogin: func(options) (net.Conn, *bufio.Reader, error) {
					calls++
					if calls == 1 {
						if !failCurrentDial {
							cancel()
						}
						return tracked, bufio.NewReader(tracked), nil
					}
					cancel()
					return nil, nil, errors.New("setup interrupted")
				},
			})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("setup cancellation returned %v", err)
			}
			if !tracked.closed.Load() {
				t.Fatal("already opened worker was not closed on setup cancellation")
			}
			if !failCurrentDial && calls != 1 {
				t.Fatalf("made %d dial attempts after cancellation, want 1", calls)
			}
		})
	}
}
