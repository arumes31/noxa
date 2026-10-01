# Client audit — 23 September 2026

**Follow-up:** findings 1–9 were implemented after this audit. See [fixes and verification](client-audit-fixes-2026-09-23.md). The original observations below are retained as historical evidence.

Audited the current working tree, including the selected quick wins, without changing application source. This is a test report, not a clean bill of health. Seven product issues and two validation-tooling issues need attention.

## Results and scope

| Check | Result |
| --- | --- |
| Full Chromium workflow suite | **631 passed**, zero skipped/flaky/failed; 11.8 minutes |
| Frontend unit suites | **168 passed** across eight suites |
| Frontend lint | Passed, 148 files |
| Client `go test -race -count=1 ./...` | Passed; 902 test/subtest pass events, six live tests skipped without environment configuration |
| Client `go vet ./...` | Passed |
| Production frontend and Windows Wails build | Passed; `client/build/bin/noxa-audit.exe` |
| Unmodified live integration tests, isolated current server | Three passed, four failed after supplying fixture visibility; failures reflect obsolete test contracts |
| Live integration with temporary test-only Go overlay | Six passed: trust, encrypted channel chat, client-info privacy, upload/download, file management, presence. Authentication and wrong-password rejection passed separately. Application source unchanged. |
| Real packaged Windows UI | Tested interactively via Windows Computer Use, against an isolated profile and disposable loopback server; details below |

The real UI passed startup/onboarding dismissal, required-field validation, TLS guest connection and reconnect, recent-server restoration, channel join, Unicode chat sending, received peer messages and history, the composer counter, device refresh, F8 shortcut capture/save/persistence, 30/60-minute notification snooze and cancellation, server diagnostics, About/version-copy action, file upload, file download, save-dialog cancellation, and disconnect. The downloaded fixture's SHA-256 matched the original (`B6E6FFC7F312DF1AC7BD2BC531271AA63DF24C57104B715C481E5DBB2048118D`). One native Save As attempt terminated the process; later save/cancel attempts succeeded.

## Must fix

### 1. P1 — Investigate and eliminate native Save As process termination

**Observed in the real Windows executable.** Clicking Download opened Save As; during subsequent window/focus handling the client exited. Standard output reports `WebView2 Error: The parameter is incorrect.` The stack runs from the native save dialog through Wails `Frontend.onFocus` to WebView2 `Chromium.Focus`. The installed `go-webview2@v1.0.23` calls `os.Exit(1)` on that error, bypassing the application's panic handler.

**Precision:** one observed occurrence. Two subsequent dialog openings worked; one completed a verified download and the other survived explicit parent activation and cancellation. This is intermittent, not a claim that every download crashes. Parent activation was part of the investigation, but the precise initial trigger remains unproven.

Entry point: `client/files.go:834`. Correct the modal/focus failure path and add packaged-Windows tests for opening, saving, cancelling, and switching focus while a native dialog is open. Evidence: `.cache/client-audit-2026-09-23/native.stdout.log` and `native/appdata/noxa/client.log` (19:13:47 UTC).

### 2. P1 — Whisper preview must reflect acknowledged routing

`client/frontend/src/workspace-ui.js:188` builds recipients from saved settings. Settings are committed before the server routing request (`settings-ui.js:1730`), so a rejected update still displays “Whisper targets: Alice.” A production-module reproduction returned `permission denied` while that preview remained visible. The server rejects before replacing the previous route (`internal/server/role_media_controls.go:63`), which can still be ordinary channel routing.

Display confirmed routing or an explicit failed/unapplied state. The false preview and server rejection path were verified; accidental live audio delivery was **not** tested. Evidence: `repro-whisper-preview.mjs` and its `.log` in the audit directory.

### 3. P1 — Explicit tray Quit must bypass close-to-tray

`client/tray.go:111` removes the tray and calls Wails Quit. With CloseToTray enabled, `client/app.go:130` cancels that quit and hides the window. The client can remain running with no tray icon, retaining connections and shortcuts.

A callback-level Go reproduction failed with “explicit tray Quit is cancelled by OnBeforeClose … after tray is already removed.” The installed Wails quit implementation confirms the call chain. This finding was not reproduced by clicking the Windows tray. Distinguish explicit quit from ordinary window-close behavior. Evidence: `tray-quit-repro.log`, `hotkeys_audit_test.go`, and `hotkeys-overlay.json`.

### 4. P2 — Blocking someone must immediately silence existing audio

The actual block context-menu handler (`client/frontend/src/clientinfo.js:145`) saves `blocked_users` and announces “voice muted locally” without updating existing audio nodes. Reconciliation only happens on a later server snapshot (`main.js:1079`). The production-module reproduction recorded `blocked:["alice"]`, `muted:false`, and both audio gains still at `1` after the success toast.

Apply the block to current playback immediately and make newly created streams respect it. Evidence: `repro-block-audio.mjs` / `.log`.

### 5. P2 — Concurrent audio preference saves lose changes

`audio.js:370` saves a full settings snapshot. `main.js:1082` starts several blocked-user mute saves without awaiting them. With Alice and Bob blocked, the reproduction leaves only Bob muted and Alice audible. Concurrent per-user volume changes similarly discard an earlier change.

Serialize mutations against current settings or use transactional field-specific backend methods. Evidence: `repro-audio-settings-race.mjs` / `.log`, covering both blocked-user batches and overlapping volume updates.

### 6. P2 — Local mute must report persistence failures

`audio.js:376` ignores the settings-save error, while `clientinfo.js:124` reports successful muting. Injecting `SaveSettings → "disk unavailable"` makes the operation resolve successfully with mute still false and audio gains unchanged.

Propagate failures and show success only after the mute takes effect. Evidence: `repro-mute-save-failure.mjs` / `.log`.

### 7. P2 — Pending hotkey registration survives unbinding

`client/hotkeys.go:165` starts asynchronous registration. An immediate empty binding clears only already-installed registrations; the old goroutine later installs itself at line183. The audit Go overlay reproduced this in five out of five runs, repeated twice.

Cancel or invalidate pending registrations on rebind, unbind, and shutdown. The normal test waits for registration before unbinding, missing this ordering. Evidence: `hotkeys-repro.log` and `hotkeys_audit_test.go`.

## Validation tooling that must also be repaired

8. **P2 — Update the live integration fixtures to roles-v1.** `client/conn_live_test.go:196` omits `authorization_model=roles-v1`; line202 uses retired `channelcreate`. Other assertions expect obsolete `user_moved` / `status_changed` events and asynchronous mutation errors. The default green suite skips these tests. The current server rejected the old login as expected. Temporary overlay fixtures and current snapshot/ack assertions allowed the live flows to pass. Preserve meaningful authorization checks when updating them. Evidence: `live-go.log`, `live-overlay-go.log`, `conn_live_overlay_test.go`, `live-overlay.json`.

9. **P2 — Make the native QA runner initialize the current authorization model.** `scripts/native-ui-session.ps1:235` starts a fresh server without provisioning and activating roles. The actual run failed with `role authorization is not configured`. The audit proceeded only after preparing disposable identities and activating roles separately. Evidence: `temp/native-ui-client-audit-20260923/evidence/server.stderr.log`.

## Coverage limits and retained evidence

This run does not establish real two-machine microphone/speaker quality, camera/screen-sharing quality, WAN/VPN behavior, long-running media stability, native positional audio, or OS tray/notification behavior across Windows versions. Native authenticated group/private-call UI was not exercised; their browser workflows passed. Stream codec/bitrate diagnostics passed simulated browser tests, but were not validated with a remote native video sender in this run. These remain acceptance checks, not newly proven defects.

Some module-only browser fixtures log missing-Wails-runtime startup errors while their intended assertions pass; the full browser result should not be read as an assertion that every console is clean.

Logs, deterministic reproduction scripts, temporary Go overlays, and browser JSON results are retained under `.cache/client-audit-2026-09-23/`. Native UI screenshots were inspected during the session. The real client used only an isolated profile. Its process, the disposable server, and the two audit database containers were stopped after testing. Existing source changes were preserved; no fixes were applied during this audit.
