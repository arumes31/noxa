package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCancelUpdateInterruptsMetadataAndAllowsRetry(t *testing.T) {
	withUpdateRepo(t, "o/r")
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	previous := updateAPIBase
	updateAPIBase = server.URL
	t.Cleanup(func() { updateAPIBase = previous })
	app := &App{}
	result := make(chan string, 1)
	go func() { result <- app.DownloadAndApply(UpdateInfo{}) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("metadata request did not start")
	}
	if got := app.DownloadAndApply(UpdateInfo{}); !strings.Contains(got, "already") {
		t.Fatalf("duplicate update = %q", got)
	}
	if !app.CancelUpdate() {
		t.Fatal("download cancellation rejected")
	}
	select {
	case got := <-result:
		if got != "update cancelled" {
			t.Fatalf("cancelled update = %q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation did not interrupt network request")
	}
	if app.CancelUpdate() {
		t.Fatal("idle cancellation accepted")
	}
	retry := fakeGitHub(t, `{"tag_name":"v0.0.0"}`)
	defer retry.Close()
	updateAPIBase = retry.URL
	if got := app.DownloadAndApply(UpdateInfo{}); got != "update is no longer available" {
		t.Fatalf("retry = %q", got)
	}
}

func TestUpdateCancellationStopsAtVerification(t *testing.T) {
	app := &App{}
	ctx, finish, err := app.beginUpdate()
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	if err := app.updatePhase(ctx, "verifying"); err != nil {
		t.Fatal(err)
	}
	if app.CancelUpdate() || ctx.Err() != nil {
		t.Fatal("verification must not be cancellable")
	}
}

func TestCancelledUpdateCannotEnterVerification(t *testing.T) {
	app := &App{}
	ctx, finish, err := app.beginUpdate()
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	app.CancelUpdate()
	if err := app.updatePhase(ctx, "verifying"); err != context.Canceled {
		t.Fatalf("verification after cancellation = %v", err)
	}
}

func TestCancelledDownloadRemovesPartialAsset(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("partial executable"))
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- downloadTo(ctx, server.URL, root, "partial.exe", 1024, nil) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("asset request did not start")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("download cancellation = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("asset request did not stop")
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("partial download retained: %v, %v", entries, err)
	}
}

func TestRestoredUpdateKeepsWindowAndAllowsNewDownload(t *testing.T) {
	previousLaunch, previousQuit := restartLaunch, wailsQuit
	t.Cleanup(func() { restartLaunch, wailsQuit = previousLaunch, previousQuit })
	app := &App{ctx: context.Background(), eventEmit: func(string, any) {}}
	app.update.phase = "ready"
	restartLaunch = func(string) error { return errUpdateRestored }
	wailsQuit = func(context.Context) { t.Error("rollback quit the working app") }
	if got := app.ApplyAndRestart(); got == "" || app.GetUpdatePhase() != "restored" || app.quitting.Load() {
		t.Fatalf("rollback = %q, phase=%q, quitting=%v", got, app.GetUpdatePhase(), app.quitting.Load())
	}
	_, finish, err := app.beginUpdate()
	if err != nil {
		t.Fatalf("new download after rollback: %v", err)
	}
	finish()
}
