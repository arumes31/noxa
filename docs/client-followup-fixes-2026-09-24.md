# Client follow-up fixes: items 1–5

Implemented and verified locally after the first nine audit fixes. No production
deployment was performed. Existing working-tree changes were preserved.

## Changes

1. **Settings edits preserve unrelated changes.** `GetSettings` includes the
   original snapshot as bridge-only baseline metadata. Within the serialized
   backend transaction, `SaveSettings` applies only changed fields and rejects
   conflicting same-field edits without partially writing anything. Maps and
   arrays are treated as whole fields. Native callers that deliberately omit a
   baseline retain full-replacement semantics; Go-owned fields remain protected.
   The dialog rebases after persistence and rebinds controls after successful
   application or an audio-application failure. Native effects and whisper
   application use committed values rather than an old dialog snapshot.
2. **Contacts and notes report persistence failures.** Immediate contact, note
   and per-user audio edits share a serialized queue. They edit a clone and
   publish only the persisted settings. Failed contact add/remove/watch,
   context-menu additions and note saves show errors; additions retain input for
   retry and failures no longer announce success.
3. **Browser runtime errors fail tests.** All browser specs use a shared automatic
   fixture that records uncaught exceptions/rejections across the context and
   secondary pages. Independent WebRTC peer contexts are observed too. Incomplete
   sound and stream-control fixtures were repaired after the guard exposed them.
4. **Administration documentation matches current code.** README examples use
   integration-enabled accounts, mandatory `roles-v1` negotiation and current
   role/member commands. Retired numeric permissions and tokens are removed.
   Native API and media inventories distinguish current behavior from historical
   checkpoint gaps. Query syntax was checked against handlers and TCP tests.
5. **Failed terminal media output is retired.** Existing production socket
   deadlines and terminal policy checks already cover pacing and NACK/RTX.
   Terminal writer errors now deactivate the stream, invalidate tickets and log
   the failure after authorization, policy, watch and stream read locks unwind.
   Guard denials do not retire healthy output. Recovery requires a normal track
   or peer rebuild/reconnect. Arbitrary custom synchronous writers must bound
   their own calls; already transmitted packets cannot be revoked.

## Verification

Evidence directory: `.cache/client-followup-2026-09-23/`.

- Full root and client Go suites passed. Full client race suite and complete
  WebRTC/Query race suites passed; client and changed-server-package vet passed.
- Deterministic media tests cover failed-output retirement, guard recovery, a
  real blocked socket releasing authorization/policy leases, and old paced/NACK
  packets being rejected after a policy change.
- All 170 frontend unit tests and frontend lint passed.
- The full guarded browser run passed 632/634 checks. Its two failures coincided
  with Vite navigation during native binding generation; both passed three times
  each with the build idle. Two additional follow-up checks passed, and review
  identified one more notification retry defect. Its regression failed before
  the fix and passed afterward with all 28 adjacent audio/settings checks.
  This covers 637 distinct browser tests across the full and targeted runs;
  it is not a claim of one uninterrupted 637-test run.
- Independent review found the nested-notification-draft retry defect described
  above; follow-up review found no remaining concrete issue. Backend merge and
  media lock ordering were also reviewed independently.
- Wails production frontend/audio verification and native executable build passed.
  Final artifact: `client/build/bin/noxa-followup-fixed.exe`.
- The actual Windows Wails client connected as a synthetic guest to a disposable
  loopback server. Settings Apply persisted 200 → 777 → 888 without reopening.
  Contact creation and its watch preference persisted while retaining 888;
  `settings_base` was absent from the disk file. Native evidence is in
  `native-settings-evidence.json`. These checks preceded the final
  notification-retry-only UI fix, which is covered by the browser regression.
- Seven live backend tests passed against the disposable roles-v1 server:
  authentication, isolated certificate trust, channels, client info, presence,
  upload/download and file management. The native client exited normally and
  the recorded server/containers were stopped; evidence was retained.

Physical camera/display drivers, sustained WAN streaming and FFmpeg recording
acceptance remain separate from this follow-up. See `docs/media-resource-limits.md`
and the current checkpoint in `tasks/permission-implementation-status.md`.
