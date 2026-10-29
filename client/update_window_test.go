package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"testing"
	"time"
)

func TestUpdateWindowSurvivesChildEnvironment(t *testing.T) {
	entry := updateWindowEnv + "=encoded-placement"
	env := updateChildEnvironment([]string{entry, updateReadyNonceEnv + "=old"}, "127.0.0.1:1234", "new")
	if !slices.Contains(env, entry) || !slices.Contains(env, updateReadyNonceEnv+"=new") || slices.Contains(env, updateReadyNonceEnv+"=old") {
		t.Fatal("child environment lost placement or retained old readiness")
	}
}

func TestUpdateWindowPassedToRestartAndParentRestoredOnFailure(t *testing.T) {
	oldCapture, oldLaunch := captureUpdateWindow, restartLaunch
	t.Cleanup(func() { captureUpdateWindow, restartLaunch = oldCapture, oldLaunch })
	t.Setenv(updateWindowEnv, "parent")
	captureUpdateWindow = func() (*updateWindowPlacement, error) {
		return &updateWindowPlacement{Left: 300, Top: 200, Right: 1300, Bottom: 900}, nil
	}
	restartLaunch = func(string) error {
		p, err := decodeUpdateWindow(os.Getenv(updateWindowEnv), time.Now())
		if err != nil || p.Left != 300 {
			t.Fatalf("child geometry: %+v, %v", p, err)
		}
		return errors.New("launch failed")
	}
	a := &App{ctx: context.Background()}
	if got := a.ApplyAndRestart(); got != "launch failed" {
		t.Fatalf("restart = %q", got)
	}
	if os.Getenv(updateWindowEnv) != "parent" || a.quitting.Load() {
		t.Fatal("failed restart changed parent")
	}
}

func TestUpdateWindowHandoffRestoresOnlyOnceAfterUpdate(t *testing.T) {
	oldCapture, oldApply, oldStarted := captureUpdateWindow, applyUpdateWindow, startedAfterUpdate
	t.Cleanup(func() { captureUpdateWindow, applyUpdateWindow, startedAfterUpdate = oldCapture, oldApply, oldStarted })
	t.Setenv(updateWindowEnv, "previous-parent-value")
	captureUpdateWindow = func() (*updateWindowPlacement, error) {
		return &updateWindowPlacement{Left: -1800, Top: 80, Right: -500, Bottom: 980, Maximized: true}, nil
	}
	restore := prepareUpdateWindow()
	saved, err := decodeUpdateWindow(os.Getenv(updateWindowEnv), time.Now())
	if err != nil || saved.Left != -1800 || !saved.Maximized {
		t.Fatalf("saved placement: %+v, %v", saved, err)
	}
	child := os.Getenv(updateWindowEnv)
	restore()
	if os.Getenv(updateWindowEnv) != "previous-parent-value" {
		t.Fatal("parent environment not restored")
	}
	called := 0
	applyUpdateWindow = func(p updateWindowPlacement) error {
		called++
		if p.Left != -1800 || p.Right != -500 || !p.Maximized {
			t.Fatalf("wrong placement: %+v", p)
		}
		return nil
	}
	startedAfterUpdate = false
	_ = os.Setenv(updateWindowEnv, child)
	restoreUpdateWindow()
	if called != 0 || os.Getenv(updateWindowEnv) != "" {
		t.Fatal("normal launch consumed placement")
	}
	startedAfterUpdate = true
	_ = os.Setenv(updateWindowEnv, child)
	restoreUpdateWindow()
	restoreUpdateWindow()
	if called != 1 {
		t.Fatalf("restore calls = %d", called)
	}
}

func TestUpdateWindowRejectsExpiredAndInvalidBounds(t *testing.T) {
	now := time.Now()
	valid := updateWindowPlacement{Version: 1, Expires: now.Add(time.Minute).Unix(), Left: 100, Top: 70, Right: 1124, Bottom: 838}
	for _, change := range []func(*updateWindowPlacement){
		func(p *updateWindowPlacement) { p.Expires = now.Add(-time.Minute).Unix() },
		func(p *updateWindowPlacement) { p.Expires = now.Add(time.Hour).Unix() },
		func(p *updateWindowPlacement) { p.Right = p.Left },
		func(p *updateWindowPlacement) { p.Bottom = -100 },
		func(p *updateWindowPlacement) { p.Left = -2147483648; p.Right = 2147483647 },
		func(p *updateWindowPlacement) { p.Left = -132000; p.Right = -131000 },
		func(p *updateWindowPlacement) { p.Left = 131000; p.Right = 132000 },
		func(p *updateWindowPlacement) { p.Version = 2 },
	} {
		p := valid
		change(&p)
		raw, _ := json.Marshal(p)
		if _, err := decodeUpdateWindow(string(raw), now); err == nil {
			t.Fatalf("accepted invalid placement: %+v", p)
		}
	}
}

func TestUpdateWindowCaptureFailureDoesNotForwardStalePosition(t *testing.T) {
	old := captureUpdateWindow
	t.Cleanup(func() { captureUpdateWindow = old })
	t.Setenv(updateWindowEnv, "stale")
	captureUpdateWindow = func() (*updateWindowPlacement, error) { return nil, errors.New("window gone") }
	restore := prepareUpdateWindow()
	if os.Getenv(updateWindowEnv) != "" {
		t.Fatal("forwarded stale placement")
	}
	restore()
}
