# Communication parity: selected features 1, 2, 5, 8, 9

User authorized implementation of all five selected recommendations. Existing plan.md/todo.md and earlier uncommitted work remain intact; this checklist tracks the new independent initiative.

## Contracts and build order

Camera capture provides a shared, disposable stream pipeline for preview, channel camera and private calls. Background segmentation runs locally with bundled assets. Private calls retain authenticated signaling and explicitly acquired camera/display tracks. Persistent threads/forums inherit channel access and use server-enforced mutations. Voice recordings use the existing encrypted attachment transport. The gaming overlay is enabled by default, optional, visible only for voice activity, and uses a separate non-activating Windows window for borderless/windowed games.

1. Camera device and background pipeline -> channel/settings integration -> private call integration.
2. Thread/forum schema and wire contract -> authorized server operations -> client UI.
3. Voice recording lifecycle -> encrypted attachment send -> inline playback.
4. Overlay snapshot and lifecycle -> native window -> settings switch.
5. Combined regression, production build, and isolated native UI verification.

## Acceptance and verification

- [x] Private calls: separate camera/screen slots, optional screen audio, participant tiles, late-capture cleanup, no unintended microphone/camera activation. Test with paired peers and call teardown.
- [x] Camera: selected device persists; genuine foreground/background separation for blur/replacement; local preview matches live capture; errors release resources and remain actionable. Unit and browser tests.
- [x] Threads/forums persistence: independent threads with membership/subscriptions/archive; tagged forum posts; migration and server permission checks. Go tests for lifecycle and denied access.
- [x] Threads/forums UI: create, discover, read/reply, join/subscribe, archive/reopen and filter posts; stale requests cannot cross servers. Browser regression.
- [x] Voice messages: record/stop/preview/discard/send, bounded duration/size, encrypted attachments, inline controlled playback, scope changes release media and cannot misroute sends. Browser and unit regression.
- [x] Gaming overlay: default enabled with saved off switch; separate topmost, nonactivating window, speaker/mute and notification display, bounded data, no input interception, closes with client. Go tests and native verification.
- [x] Integration: lint, unit/browser suites, Go tests/race checks for changed packages, frontend and Windows builds, isolated native client smoke test.

## Results

- All frontend unit suites and lint passed. Full browser run: 686/691 passed;
  four obsolete group-message selectors were updated after adding audio containers,
  and the Web Audio render timeout passed on rerun. The 40-case follow-up and
  separate read-acknowledgment regression passed, covering all five failures.
- Review fixed recording cancellation during group refresh, retry deduplication
  after rerecording, and live Deafen/volume propagation to voice-message players.
  Group-message whitespace styling follows the new container.
- Root internal tests and client tests passed with 72.6% and 70.7% coverage.
  Changed backend packages and client race checks passed; root/client vet passed.
  The broader run exposed a pre-existing recorder test synchronization race:
  its assertion now awaits bounded asynchronous cleanup while the router remains
  blocked. One hundred repetitions and the full recorder suite passed.
- Real PostgreSQL persistence and TLS/TCP encrypted discussion roundtrips passed,
  including permission denials and delivery to followers outside the voice channel.
- Frontend and Windows production builds passed. Real WebView2 smoke checks covered
  camera device/effect persistence, offline segmentation, capture cancellation,
  voice recording/preview/discard, and default-enabled overlay settings. Media input
  was synthetic; physical cameras/headsets were not assessed. A native Win32 test
  verified the overlay's visible/nonactivating/pass-through styles and close lifecycle.
- Owned native clients and disposable database containers were cleaned up.

## Compatibility

The native gaming overlay targets Windows borderless/windowed games; exclusive fullscreen is not supported. Unsupported platforms must report availability honestly. New capture effects never send images to external services. No existing work is committed or discarded as part of this request.

## Next approved batch: 1, 2, 3, 4, 6, 7, 8, 9, 10, 11, 15

The user approved the numbered recommendations in the subsequent conversation.
Item 7 is explicitly optional undocking: calls remain docked by default.

| Module | Contract and acceptance | Verification |
|---|---|---|
| channel-media | Camera and screen coexist in channels; distinct tiles and independent per-person share-audio volume persist without changing microphone volume or deafen semantics. | Real paired media, channel transitions, sender teardown and saved-volume tests. |
| overlay-controls | Monitor, position, scale, opacity, speakers-only, local preview; saved off remains effective; ordinary overlay passes input through. | Go bounds/lifecycle tests and native Windows window checks. |
| camera-customization | Local custom image, adjustable blur, local-only mirroring; bounded validated image storage and no preview mirroring on outgoing streams. | Pure/unit, real processing and native settings smoke. |
| voice-playback | Waveform, duration, seek, speed and local resume; lazy encrypted attachment loading and scope cleanup retained. | Browser MediaRecorder/decode/playback tests and local storage bounds tests. |
| call-undocking | Explicit undock/dock of call UI, participant video and call controls, always-on-top support; close returns to dock without ending call; main close releases resources. | Capability check early, real native/browser window lifecycle and media continuity. |
| conversation-tools | Unified unread inbox and jump; richer history search with sender/channel/date/attachment/thread filters; personal organized saved-message references with jump. Existing client-side history decryption stays private and bounded. | Browser workflows plus Go tab/isolation and persistence tests. |
| discussion-controls | Edit title/tags, pin, resolve, durable automatic archiving under current channel permissions. | Database lifecycle/concurrency and browser author/moderator workflows. |
| incoming-webhooks | Create/list/revoke channel-scoped tokens, authenticated HTTP POST, current authorization, payload/rate limits, clear integration attribution. Hash tokens at rest; raw token shown once. | Real HTTP/database integration, denial/revocation and UI tests. |

Build order: provider contracts → independent vertical slices → shared UI integration → focused tests → combined regression/native acceptance. Root owns settings/main/i18n integration; media, conversation and server slices use disjoint ownership. Migrations are allocated 035 for discussion metadata and 036 for webhooks, sequentially. Preserve existing dirty work and previous plans; no deployment or commit requested.

- [x] Channel media and per-person share volume
- [x] Overlay controls
- [x] Camera customization
- [x] Voice playback controls
- [x] Optional call undocking
- [x] Inbox, history search, saved messages
- [x] Discussion controls
- [x] Incoming webhooks
- [x] Integrated tests, review and real native UI acceptance

### Next batch implementation and verification

- Channel media retains independent camera/screen slots. Personal share-audio
  gain/mute is separate from microphone gain, including private calls. Real peer
  tests verify audible shared audio while the same member's microphone is muted.
- The optional detached call view keeps video and controls working, including
  push-to-talk release on docking, blur and connection changes. Calls start docked.
- Review fixed resume persistence before detached audio cleanup and replaced full
  PCM decoding of received recordings with bounded streaming waveform sampling.
  Local recording previews retain a complete waveform; remote waveforms fill as
  playback advances. Only hashed references and playback positions are retained.
- Camera background validation covers format, byte and dimension limits. Preview
  mirroring updates immediately without replacing the outgoing track.
- Inbox and saved collections include direct messages. DM references carry the
  captured identity context; saved files never contain message bodies or keys.
  Channel/DM unread counts are session-local; group/thread unread state is durable.
- Real PostgreSQL/TCP/HTTP checks cover discussion edits, pin paging, subscription
  filtering, inactivity archiving, webhook encryption, current permissions, bans,
  rate limits and concurrent post/revoke serialization. Inactivity archiving is
  evaluated on reads/writes. Remote webhooks require the documented HTTPS proxy.
- Root internal race suite with PostgreSQL/Redis passed at 78.2% coverage (70% gate).
  Client race suite passed at 70.4% total coverage (50% gate). Root/client vet and
  repository Go formatting passed.
- Production frontend and Windows builds passed. Isolated real WebView2 checks
  verified new native bindings, monitor/position/size/opacity/speakers-only settings,
  native overlay preview, custom camera image/blur/mirror and capture cleanup,
  voice recording waveform/speed/seek/discard, and detached live video/control
  survival. No uploads occurred. The owned client and database containers were
  removed; media inputs were synthetic, not physical cameras/headsets.
- All 186 frontend unit tests and lint passed. The complete browser run finished
  with 713/714 passing; one test failed before application startup because the
  local browser reported `ERR_NO_BUFFER_SPACE`. Its entire 11-case emoji group
  and the three overlay cases passed in the final 14-case follow-up. No application
  assertion failures remain. The final follow-up also verified the matching native
  preview button styling. Windows integration-tag tests passed.
- Validation logs and screenshots are retained under `.cache/next-*` and
  `.cache/mic-ui-native/`. The production executable is
  `client/build/bin/noxa-communication-parity.exe`. Changes remain uncommitted.
