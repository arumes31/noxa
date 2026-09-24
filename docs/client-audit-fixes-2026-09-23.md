# Client audit fixes — 23 September 2026

Implemented findings 1–9 from [the client audit](client-audit-2026-09-23.md).

| Finding | Change |
| --- | --- |
| 1. Native Save As exit | Pinned local WebView2 patch skips focus on disabled owners and defers `E_INVALIDARG`; other fatal errors retain upstream behavior. COM regression tests cover both outcomes. |
| 2. Misleading whisper preview | Preview uses acknowledged routing for the current connection/channel. Pending, rejected, and unapplied routing are explicit; obsolete replies cannot confirm targets. |
| 3. Quit hides instead of exiting | Tray and application-menu Quit share an explicit backend exit path that bypasses close-to-tray. Tray teardown follows desktop shutdown. |
| 4. Blocked audio remains audible | Effective mute includes the block list for existing and newly registered playback chains. Unblocking preserves independent manual mute and volume preferences. |
| 5. Lost audio settings updates | Audio preference mutations run serially, each cloning the latest settings when its turn begins. Block reconciliation no longer starts competing mute saves. |
| 6. False mute success | Failed persistence rejects the mutation; callers display the error and leave effective playback unchanged. A failed operation does not poison later queued saves. |
| 7. Obsolete hotkey workers | Per-action generations invalidate pending registration and stale callbacks/status. Replacement releases held PTT before publishing the new binding. Shutdown rejects late workers. |
| 8. Obsolete live fixtures | Fixtures create role-aware channels through the native role API, consume current membership snapshots, and assert acknowledged errors. Closed-baseline authorization and privacy assertions remain. |
| 9. Fresh runner cannot start | Runner provisions disposable identities, activates roles-v1 before server startup, protects credentials, pins the generated certificate, and provides a `test` action with environment restoration. Provisioning clears inherited chat master keys. |

Review also caught a Contacts dialog retaining its original settings object after a block. Its rendering and subsequent contact actions now use current settings; a browser regression covers block, edit, unblock, and failed save without reopening.

## Verification

- Full Chromium workflow suite: **632 passed**, zero failed, skipped, or flaky (10.4 minutes). After extending the explicit quit fix to the application menu, the affected nine-test browser file passed again, including the new Quit regression.
- Frontend units: **170 passed** across eight suites; lint passed (150 files).
- Client `go test -race -count=1 ./...` and `go vet ./...`: passed. Focused quit/hotkey/restart race tests passed again after exporting the shared Quit binding.
- `go test github.com/wailsapp/go-webview2/pkg/edge -run '^TestFocus' -count=1`: passed. The original error reproduced before the patch; unrelated fatal HRESULTs still exit with status 1.
- Fresh runner at loopback port 19583: provisioning and readiness passed despite a deliberately invalid inherited `NOXA_CHAT_MASTER_KEY`. All **seven live backend tests passed** using the runner's `test` action.
- Production Wails build and generated bindings passed; executable: `client/build/bin/noxa-audit-fixed.exe`.

## Actual Windows UI

Used the packaged executable, an isolated profile, and the fresh disposable server. Startup, recent-server restoration, TLS guest connection, channel join, native file browser, Save As, refocusing the disabled owner, and Save As cancellation passed. Downloaded bytes exactly matched the 43-byte live fixture (SHA-256 `3D53DC3775D6FC01D82EDC40AC9057C4A97289CE1766C3E618E6C6CF9A466597`).

The native log recorded `[WebView2] focus deferred: The parameter is incorrect.` at **20:06:17 UTC** while the client remained running and subsequently completed the download. This exercised the actual error path that previously terminated the process, in addition to the deterministic COM tests.

The final rebuilt client reconnected through its recent-server entry, then **Connections → Quit terminated the process with `CloseToTray=true`**. The tray invokes this same backend method; the tray icon itself was not clicked in this run. The native client, disposable server, and both database containers are stopped; evidence and disposable data are retained.

The focus dependency replacement and upgrade/removal instructions are documented in `client/third_party/go-webview2/NOXA-PATCH.md`; its upstream license is retained. The audio save queue coordinates audio preference changes, not arbitrary unrelated full-settings writers.

Evidence is retained under `.cache/client-fixes-2026-09-23/` and `temp/native-ui-client-fixes-20260923/evidence/`. Real remote microphone/speaker quality, WAN behavior, and long-running media acceptance are outside this regression run.
