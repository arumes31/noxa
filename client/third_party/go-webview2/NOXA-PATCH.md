# Local WebView2 focus patch

Source: github.com/wailsapp/go-webview2 v1.0.23 (upstream LICENSE retained).

The packaged Windows client exited from Chromium.Focus while Save As was open:
MoveFocus returned E_INVALIDARG and errorCallback called os.Exit(1).

Aside from mechanical gofmt normalization required by CI, only Focus behavior is changed: ignore missing/shutting-down controllers and disabled
owner windows; log and defer the E_INVALIDARG focus attempt. Other HRESULTs and
other fatal-error sites retain upstream behavior. No permission policy changes.

Regression command, from client:

    go test github.com/wailsapp/go-webview2/pkg/edge -run '^TestFocus' -count=1

Tests exercise actual COM callback results in subprocesses, including retaining
exit status 1 for other failures. Also verify native open/save/cancel/refocus in
the packaged Windows executable. Reevaluate this replacement when upgrading
Wails/WebView2, removing it once upstream includes the equivalent fix.

The standalone module also requires golang.org/x/sys v0.48.0 (Go 1.26 or
newer) to fix GO-2026-5024 / CVE-2026-39824. This matches the version already
selected by the noXa client; its minimum-version selection overrides this
fork's original v0.27.0 requirement. The application still requires Go 1.27.1.
Govulncheck found no call to NewNTUnicodeString in the standalone Windows
scan, but the old standalone module imported the affected package.
See https://pkg.go.dev/vuln/GO-2026-5024 for the affected range and fix.
