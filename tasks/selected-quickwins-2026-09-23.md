# Selected quick wins — 2026-09-23

Requested items: 3, 4, 5, 6, 7, 11, 20, 31, 32, 33, 34, 40, 43 (including bandwidth in kbit/s and Mbit/s), 46, 81–83, 91.

Implemented:

- Localized pane-resize help/names and removed numeric permission wording from banner help.
- Updated README authorization documentation, refreshed the feature-gap report against recorded implementation evidence, and repaired obsolete project-path links.
- Added a composer character/UTF-8-byte counter. ServerInfo now exposes optional `chat_max_bytes`; the counter highlights 90% of that limit for channel/global messages. Direct messages show counts without applying the channel limit. The display is advisory; authoritative send validation remains on the server.
- Added message copy with clipboard error handling and local accessible confirmation.
- Added per-member volume reset and explicit amplification wording above 100%.
- Added live PTT binding/unavailable guidance and a whisper-target preview, including the armed reply target.
- Added receiver-scoped stream codec, payload bandwidth, and copyable diagnostics. Samples exclude candidate addresses/credentials and other streams. Connection traffic rates also use decimal kbit/s or Mbit/s.
- Added 30/60-minute notification snooze and cancellation in the notification center. `SetNotificationSnooze(minutes)` accepts only 0, 30 or 60, writes one field transactionally, returns the persisted Unix-millisecond expiry, and does not apply unrelated settings effects. Ordinary settings saves preserve this backend-owned value. DND and quiet hours remain independent.
- Added About → Copy version information with client/server build identities. Identity and address are excluded, and old server responses cannot be attributed to a new connection.

Verification:

- Root and client Go suites passed; persistence tests cover snooze reload, stale full-settings saves, invalid durations, cancellation and failed writes.
- Full frontend unit suite passed. Final affected UI unit tests and lint passed after follow-up fixes.
- Seventeen final Chromium workflows passed: selected features, eight existing accessibility workflows, and stream controls/layout checks. Additional stream-control regressions passed in the preceding thirteen-test run.
- Wide English and compact German stream views were visually inspected.
- Production frontend and separate Wails Windows executable built successfully: `client/build/bin/noxa-selected-wins.exe`.
- Independent review found and resolved snooze ownership/effect-error issues and missing-track statistics isolation.

The accessibility gate also exposed a pre-existing private-call action in the channel context menu referencing an undefined member. It was moved to the member menu with tab/generation scoping; the previously failing channel-editing workflow now passes.

No deployment or live native-device acceptance is claimed. The protocol counter limit requires the updated server; older servers still receive a count without an invented limit. Evidence is retained locally under `.cache/selected-quickwins/` and `.cache/selected-native-build.log`.
