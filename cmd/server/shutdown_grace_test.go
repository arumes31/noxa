package main

import (
	"os"
	"slices"
	"testing"
	"testing/synctest"
	"time"
)

func TestRunShutdownGraceAnnouncesRemainingTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var announced []time.Duration
		start := time.Now()
		skipped := runShutdownGrace(90*time.Second, func(left time.Duration) {
			announced = append(announced, left.Round(time.Second))
		}, make(chan os.Signal))
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
		calls := 0
		if !runShutdownGrace(2*time.Minute, func(time.Duration) { calls++ }, skip) {
			t.Fatal("second signal did not skip the grace period")
		}
		if elapsed := time.Since(start); elapsed != 5*time.Second {
			t.Fatalf("skip took %s, want 5s", elapsed)
		}
		if calls != 1 {
			t.Fatalf("announcements before skip = %d, want 1", calls)
		}
	})
}
