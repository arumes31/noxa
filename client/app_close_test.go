package main

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestCloseWaitsForGoodbyeAcknowledgement(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		name := "window close"
		if explicit {
			name = "explicit quit with close-to-tray"
		}
		t.Run(name, func(t *testing.T) {
			originalQuit := wailsQuit
			t.Cleanup(func() { wailsQuit = originalQuit })
			app := &App{ctx: t.Context(), settings: DefaultSettings()}
			t.Cleanup(app.cancelCloseNotification)
			app.settings.CloseToTray = explicit
			events, quits := 0, 0
			app.eventEmit = func(name string, _ any) {
				if name != "app_closing" {
					t.Errorf("unexpected event %q", name)
				}
				events++
			}
			wailsQuit = func(ctx context.Context) {
				quits++
				if app.beforeClose(ctx) {
					t.Error("acknowledged exit was intercepted")
				}
			}
			app.ReadyForCloseNotifications()
			if explicit {
				app.Quit()
			} else if !app.beforeClose(app.ctx) {
				t.Fatal("window closed before goodbye audio finished")
			}
			if events != 1 || quits != 0 {
				t.Fatalf("before acknowledgement: events=%d quits=%d", events, quits)
			}
			app.Quit()
			if !app.beforeClose(app.ctx) {
				t.Fatal("repeated close bypassed pending goodbye")
			}
			if events != 1 || quits != 0 {
				t.Fatalf("repeated close: events=%d quits=%d", events, quits)
			}
			app.CompleteClose()
			app.CompleteClose()
			app.Quit()
			if quits != 1 || events != 1 {
				t.Fatalf("after acknowledgement: events=%d quits=%d", events, quits)
			}
		})
	}
}

func TestCloseWithoutReadyRendererExitsImmediately(t *testing.T) {
	originalQuit := wailsQuit
	t.Cleanup(func() { wailsQuit = originalQuit })
	app := &App{ctx: t.Context()}
	app.eventEmit = func(string, any) { t.Error("unready renderer received a close event") }
	quits := 0
	wailsQuit = func(context.Context) { quits++ }
	app.CompleteClose()
	if quits != 0 {
		t.Fatal("unsolicited acknowledgement closed the application")
	}
	if app.beforeClose(app.ctx) {
		t.Fatal("unready renderer prevented window close")
	}
	app.Quit()
	if quits != 1 {
		t.Fatalf("explicit quit calls = %d, want 1", quits)
	}
}

func TestCloseToTrayDoesNotAnnounceGoodbye(t *testing.T) {
	originalHide, originalMarkHidden := windowHide, windowMarkHidden
	t.Cleanup(func() { windowHide, windowMarkHidden = originalHide, originalMarkHidden })
	app := &App{ctx: t.Context(), settings: DefaultSettings()}
	app.settings.CloseToTray = true
	app.eventEmit = func(string, any) { t.Error("hiding to tray announced goodbye") }
	app.ReadyForCloseNotifications()
	hides, marks := 0, 0
	windowHide = func(context.Context) { hides++ }
	windowMarkHidden = func() { marks++ }
	if !app.beforeClose(app.ctx) || hides != 1 || marks != 1 {
		t.Fatalf("close-to-tray did not hide window: hides=%d marks=%d", hides, marks)
	}
}

func TestCloseNotificationTimeout(t *testing.T) {
	originalQuit := wailsQuit
	t.Cleanup(func() { wailsQuit = originalQuit })
	synctest.Test(t, func(t *testing.T) {
		app := &App{ctx: t.Context()}
		events, quits := 0, 0
		app.eventEmit = func(string, any) { events++ }
		wailsQuit = func(ctx context.Context) {
			quits++
			if app.beforeClose(ctx) {
				t.Error("timeout exit was intercepted")
			}
		}
		app.ReadyForCloseNotifications()
		app.Quit()
		time.Sleep(closeNotificationTimeout - time.Nanosecond)
		synctest.Wait()
		if quits != 0 || events != 1 {
			t.Fatalf("before timeout: events=%d quits=%d", events, quits)
		}
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		app.CompleteClose()
		if quits != 1 || events != 1 {
			t.Fatalf("after timeout and late acknowledgement: events=%d quits=%d", events, quits)
		}
	})
}

func TestCloseConcurrentRequestsAndAcknowledgementsExitOnce(t *testing.T) {
	originalQuit := wailsQuit
	t.Cleanup(func() { wailsQuit = originalQuit })
	app := &App{ctx: t.Context()}
	var events, quits atomic.Int32
	t.Cleanup(app.cancelCloseNotification)
	app.eventEmit = func(string, any) { events.Add(1) }
	wailsQuit = func(context.Context) { quits.Add(1) }
	app.ReadyForCloseNotifications()
	var requests sync.WaitGroup
	for range 20 {
		requests.Go(app.Quit)
	}
	requests.Wait()
	if events.Load() != 1 || quits.Load() != 0 {
		t.Fatalf("concurrent requests: events=%d quits=%d", events.Load(), quits.Load())
	}
	for range 20 {
		requests.Go(app.CompleteClose)
	}
	requests.Wait()
	if quits.Load() != 1 {
		t.Fatalf("concurrent acknowledgements: quits=%d", quits.Load())
	}
}

func TestShutdownCancelsPendingCloseNotification(t *testing.T) {
	originalQuit := wailsQuit
	t.Cleanup(func() { wailsQuit = originalQuit })
	synctest.Test(t, func(t *testing.T) {
		app := &App{ctx: t.Context()}
		app.eventEmit = func(string, any) {}
		wailsQuit = func(context.Context) { t.Error("goodbye timeout requested quit after shutdown") }
		app.ReadyForCloseNotifications()
		app.Quit()
		app.shutdown(app.ctx)
		time.Sleep(closeNotificationTimeout)
		synctest.Wait()
		app.CompleteClose()
	})
}
