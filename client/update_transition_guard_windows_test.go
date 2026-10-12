//go:build windows

package main

import "testing"

func TestUpdateTransitionBlocksNativeSideEffects(t *testing.T) {
	previous := legacyUpdateStartup
	legacyUpdateStartup = &legacyUpdateBootstrap{}
	t.Cleanup(func() { legacyUpdateStartup = previous })
	app := &App{}
	app.beforeSettingsTransaction = func() { t.Fatal("update transition entered a settings transaction") }
	if got := app.SaveSettings(Settings{}); got != updateStartupPendingMessage {
		t.Fatalf("SaveSettings returned %q", got)
	}
	if revision, err := app.updateSettingsChecked(func(Settings) (Settings, error) {
		t.Fatal("update transition invoked settings mutation")
		return Settings{}, nil
	}); revision != 0 || err == nil || err.Error() != updateStartupPendingMessage {
		t.Fatalf("settings transaction returned revision=%d, error=%v", revision, err)
	}
	for name, connect := range map[string]func() ConnectTabResult{
		"account": func() ConnectTabResult {
			return app.ConnectNamedBookmarkTabWithID("", "127.0.0.1:1", "test", "test", "", "")
		},
		"guest":       func() ConnectTabResult { return app.ConnectGuestBookmarkTabWithID("", "127.0.0.1:1", "test") },
		"saved login": func() ConnectTabResult { return app.ConnectLogin(LoginRequest{}) },
	} {
		t.Run(name, func(t *testing.T) {
			if got := connect(); got.Error != updateStartupPendingMessage {
				t.Fatalf("connection was not rejected by the transition gate: %+v", got)
			}
		})
	}
	if lease, err := app.AcquireDisplayCaptureLease(); lease != "" || err == nil || err.Error() != updateStartupPendingMessage {
		t.Fatalf("capture lease returned lease=%q, error=%v", lease, err)
	}
	// Read-only initialization remains available, which is required before the
	// genuine frontend readiness acknowledgement can be sent to the old client.
	_ = app.GetSettings()
}
