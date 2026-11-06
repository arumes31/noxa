package main

import (
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestRunShutdownGraceAnnouncesRemainingTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var mu sync.Mutex
		var announced []time.Duration
		start := time.Now()
		skipped := runShutdownGrace(90*time.Second, func(left time.Duration) {
			mu.Lock()
			defer mu.Unlock()
			announced = append(announced, left.Round(time.Second))
		}, make(chan os.Signal))
		synctest.Wait() // announcements run asynchronously
		mu.Lock()
		defer mu.Unlock()
		if skipped {
			t.Fatal("grace reported a skip without a second signal")
		}
		if elapsed := time.Since(start); elapsed != 90*time.Second {
			t.Fatalf("grace lasted %s, want 90s", elapsed)
		}
		want := []time.Duration{90 * time.Second, time.Minute, 30 * time.Second, 10 * time.Second}
		if !slices.Equal(announced, want) {
			t.Fatalf("announcements = %v, want %v", announced, want)
		}
	})
}

func TestRunShutdownGraceSecondSignalSkips(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		skip := make(chan os.Signal, 1)
		start := time.Now()
		go func() {
			time.Sleep(5 * time.Second)
			skip <- os.Interrupt
		}()
		var calls atomic.Int32
		if !runShutdownGrace(2*time.Minute, func(time.Duration) { calls.Add(1) }, skip) {
			t.Fatal("second signal did not skip the grace period")
		}
		if elapsed := time.Since(start); elapsed != 5*time.Second {
			t.Fatalf("skip took %s, want 5s", elapsed)
		}
		synctest.Wait()
		if calls := calls.Load(); calls != 1 {
			t.Fatalf("announcements before skip = %d, want 1", calls)
		}
	})
}

func TestRunShutdownGraceIgnoresBlockedAnnouncements(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		start := time.Now()
		if runShutdownGrace(20*time.Second, func(time.Duration) { <-release }, make(chan os.Signal)) {
			t.Fatal("grace reported a skip")
		}
		if elapsed := time.Since(start); elapsed != 20*time.Second {
			t.Fatalf("a blocked announcement delayed shutdown to %s", elapsed)
		}
		close(release)
	})
}
