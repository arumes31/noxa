# Remaining deployment work — 2026-09-22

- [x] Deploy 0.5.0 fresh roles-v1 on orderotto-dev with persistence under /container/noxa.
- [x] Native three-client auth/chat/files and role revocation checks.
- [x] Consistent full backup and isolated restore, including decryption and file hash.
- [x] RTP/VP8 continuity across simulcast source switches; camera motion encoding.
- [x] Restore the Windows taskbar icon on all three native QA clients. Plain Go test builds omitted Windows resources; rebuilding through Wails packaging restores the existing noXa icon. All three running windows expose matching nonzero icon handles. Keep Wails packaging for subsequent QA rebuilds; the local compiler wrapper applies the test modfile only during compilation.
- [x] Synchronize Windows taskbar artwork with the tray's six voice states. Real two-client testing verifies distinct icon pixels, talking/mute/deafen combinations, hidden-window changes, independent clients, cached handle reuse and disconnect while transmitting. Native race suite, frontend tray units/browser workflow, client lint and packaged production build pass. Evidence: `.cache/tray-live-test.log`, `.cache/tray-disconnect-test.log`, `.cache/tray-client-race.log`.
- [x] Complete sustained screen delivery. The deployed clean image is
  `noxa:video-stability-final`. It uses a capped trendline instead of the removed
  Kalman filter, one ordered rate decision per feedback report, separate
  audio/video feedback, bounded congestion backoff, independent delay/loss
  recovery, and a two-second stale-timing reset. TWCC parsing now respects
  the declared status count and consumes deltas across evicted history,
  preventing padding/unknown packets from becoming invented losses.
  Regression tests reproduce these defects and independent review passes.
  The timeout candidate failed controlled five-second interruptions with
  30–60 seconds of collapsed video despite recovered voice. Bounded recovery
  passed a single interruption. The parser candidate recovered from two full
  server pauses, but the third disconnected the viewer during the freeze;
  that whole-server run is not a pass. Full-server freezes can also expire
  five-second control requests, so a separate test interrupts only media UDP
  inside noXa's isolated network namespace (automatic rule cleanup).
  All three five-second media outages passed the 30/60-second recovery gates:
  screens >=6 fps, camera >=12 fps, all renderers advancing, all four audio
  tracks present, no 30-second video collapse. Strict ten-minute playback
  passed all 40 intervals (600.66 seconds): screen minima 6.13/7.06 fps,
  camera minimum 14.32 fps, audio minimum 48.91 packets/second, every renderer
  advancing. Averages were 9.21/9.50 fps for screens and 18.36 fps for camera.
  Brief dips remain; this result does not claim stutter-free playback.
  Evidence:
  `.cache/video-bounded-recovery-pause.log` (one pause despite its old
  hardcoded "three" success message), `.cache/video-feedback-parser-pause.log`,
  `.cache/video-final-media-recovery.log`, `.cache/video-final-soak.log` and
  `.cache/video-final-path.log`; aggregate `.cache/video-final-soak-summary.json`.
  Final activation took a successful backup and verified image identity,
  media source checksums, readiness, and persistence under `/container/noxa`.
  The backup container's inherited empty PostgreSQL anonymous volume was
  replaced with `/container/noxa/backup-runtime`; all active persistent mounts
  now use `/container/noxa`.
  The backup service succeeded again after this compose change. Native QA
  clients were disconnected and closed after final smoke checks.
  Rollback image: `noxa:before-video-stability-20260923T015354Z`; prior source
  retained in `/container/noxa/source-before-video-stability-20260923T015354Z`.
  Post-restart native opt-in watching, three decoded streams, independent
  shared-audio energy, packet-level stop preserving voice/camera and fresh
  keyframe resume all pass (`.cache/video-final-smoke-*.log`).
  Full root tests, controller/WebRTC/server
  race checks and lint pass (`.cache/video-feedback-parser-*-tests.log`,
  `.cache/video-feedback-parser-race.log`, `.cache/video-feedback-parser-lint.log`).
  Independent shared-audio mute and
  packet-level stop/resume while preserving voice pass in
  `.cache/video-timeout-audio.log` and `.cache/video-timeout-stop.log`.
- [ ] Complete follow-up recovery validation (2026-09-23). Candidate
  `noxa:video-recovery-final` is active after successful backup, with the
  original accepted image retained as `noxa:before-recovery-improved`.
  Background asset/client/server information reads now retain their typed
  reply slot for a bounded ten-second drain after caller timeout. Late replies
  cannot complete later requests; mutations retain strict timeout behavior.
  Production Windows client rebuilt at `client/build/bin/noxa.exe`, SHA256
  `F72AE703E0827D329351711489D34E7E27F4396BFD293C5916910818CE904CFE`.
  Media fixes cover current-layer PLI during a pending switch, valid padding
  forwarding without false layer liveness or dimension resets, packet/media
  sequence separation for reordered padding, complete padding byte accounting,
  stale retransmission rejection without invalidating a recovered keyframe,
  and ten NACK attempts per missing packet using Pion's existing option.
  Focused regressions failed before each fix and passed afterward; independent
  review, root/client suites, media/client race checks and both linters pass.
  Earlier candidates still failed repeated seven-second freezes (retained in
  `.cache/video-query-recovery*.log` and `.cache/video-padding-query-recovery.log`).
  The final image passes three seven-second whole-server freezes: all nine
  forced information reads exceed five seconds without disconnecting, later
  reads succeed, and the same voice memberships remain. The 20–35-second
  recovery windows show screens 9.60–10.12 fps, camera 18.86–19.91 fps and all
  four audio streams >=49.80 packets/second. Independent shared-audio mute
  and packet-level stop/resume also pass. Evidence:
  `.cache/video-recovery-final-freezes.log`,
  `.cache/video-recovery-final-freeze-path.log`,
  `.cache/video-recovery-final-audio.log`, `.cache/video-recovery-final-stop.log`,
  `.cache/video-recovery-final-{tests,race,lint,build}.log` and
  `.cache/video-drain-client-lint.log`. Strict ten-minute playback failed its
  third 15-second interval: camera 10.85 fps fell below the 12 fps gate while
  all media experienced a shared delivery pause. The independent 615-second
  trace averaged camera 17.84 fps and screens 8.97/9.42 fps, with 61.23/45.23/
  46.98 seconds of reported freezes. These averages do not override the failed
  sustained-delivery gate. Evidence: `.cache/video-recovery-final-soak.log`
  and `.cache/video-recovery-final-soak-path.log`. A 150-second bidirectional
  kernel UDP capture recorded 125,923 packets with zero capture-socket drops.
  At 07:32:32 and 07:34:02–03 UTC, arrival rates at the server dropped across
  all peers and outgoing rates followed them. At 07:33:10 UTC, all clients'
  incoming audio slowed while server incoming/outgoing rates stayed steady.
  This establishes disruption outside user-space forwarding for those events;
  it does not distinguish the client host, network, or host kernel scheduling,
  nor rule out additional video recovery delays. Only packet counts were
  retained; no payloads were stored. Evidence: `.cache/video-udp-timing.log`
  and `.cache/video-udp-client-path.log`. The deployed source is synchronized
  at `/container/noxa/source`, with the prior source retained at
  `/container/noxa/source-before-video-recovery-final`. The three QA clients
  were disconnected and closed (`.cache/video-recovery-final-cleanup.log`).
  This follow-up is not a stutter-free production sign-off; the sustained
  delivery gate remains open pending diagnosis on an independent network path.
  Implementation notes: `docs/native-background-reads.md` and
  `docs/video-loss-recovery.md`.
- [x] Separate participant recordings with manifest (user explicitly chose this).
  Live manifest channel-3-ad4d20654e77263cd119cf43aa0ed927.json contains eight
  valid tracks; ffprobe decoded every microphone, shared-audio, camera and
  screen file. Graceful FFmpeg input end and empty-child disposal pass real
  process tests and the recorder race suite. Evidence: .cache/opt-in-recording-final-files.log.
- [x] Opt-in camera/screen viewing, periodic preview cards, independent stop.
  Design: docs/plans/2026-09-22-opt-in-channel-video.md. Default receive off at
  both router admission and final egress; no hidden continuous preview video.
- [x] Independent audio mute per watched screen share; native analyser measured
  one stream fall from RMS 0.106 to zero while the other stayed at 0.107.
- [x] Watch overlays the preview; active streams hide their preview and expose
  a compact Stop control inside the video. Browser keyboard/layout tests pass.
- [x] Publisher viewer-start sound: three native clients verified actual decoded
  cue playback only at publishers, silent polling/stopping, master mute and
  resume. All 51 WAVs verified inside production noxa.exe. Evidence:
  .cache/viewer-sound-final-native-cue.log (repeated on the final deployment).
  Browser tests cover DND and stale events. Final server/WebRTC/congestion
  race checks and root lint pass (.cache/viewer-sound-clean-race.log,
  .cache/viewer-sound-clean-lint.log).
- [ ] Real native tests for all new controls, multiple selected streams,
  sustained delivery, permission revocation, real synthetic audio energy.
- [ ] Final root/client/integration/race/lint/frontend/production builds.
- [ ] Final deployment, QA account/role cleanup, usable default Member role,
  operator handoff and screenshots.

Do not call the deployment production-ready while these checks remain open.
