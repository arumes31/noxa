# Remaining deployment work — 2026-09-22

- [x] Deploy 0.5.0 fresh roles-v1 on orderotto-dev with persistence under /container/noxa.
- [x] Native three-client auth/chat/files and role revocation checks.
- [x] Consistent full backup and isolated restore, including decryption and file hash.
- [x] RTP/VP8 continuity across simulcast source switches; camera motion encoding.
- [x] Restore the Windows taskbar icon on all three native QA clients. Plain Go test builds omitted Windows resources; rebuilding through Wails packaging restores the existing noXa icon. All three running windows expose matching nonzero icon handles. Keep Wails packaging for subsequent QA rebuilds; the local compiler wrapper applies the test modfile only during compilation.
- [x] Synchronize Windows taskbar artwork with the tray's six voice states. Real two-client testing verifies distinct icon pixels, talking/mute/deafen combinations, hidden-window changes, independent clients, cached handle reuse and disconnect while transmitting. Native race suite, frontend tray units/browser workflow, client lint and packaged production build pass. Evidence: `.cache/tray-live-test.log`, `.cache/tray-disconnect-test.log`, `.cache/tray-client-race.log`.
- [ ] Complete sustained screen delivery: GCC zero-span, Kalman period and normal-state additive decrease fixes tested/deployed. Current-layer continuity while waiting for a replacement keyframe passes the local WebRTC race suite, but needs deployment/live verification. Temporary MEDIA_DIAGNOSTIC logging is removed locally; deploy that removal.
- [ ] Separate participant recordings with manifest (user explicitly chose this).
  Coordinator and scoped taps are wired locally through NewChannelRecorder.
  Recorder race tests cover cancellation, pending processes, root identity and
  socket bounds. Deploy with max_concurrent 16, then verify real FFmpeg files
  and manifests; this remains unverified on the live server.
- [ ] Opt-in camera/screen viewing, periodic preview cards, independent stop.
  Design: docs/plans/2026-09-22-opt-in-channel-video.md. Default receive off at
  both router admission and final egress; no hidden continuous preview video.
- [ ] Independent audio mute per watched screen share; voice and other streams unaffected.
- [ ] Real native tests for all new controls, multiple selected streams,
  sustained delivery, permission revocation, real synthetic audio energy.
- [ ] Final root/client/integration/race/lint/frontend/production builds.
- [ ] Final deployment, QA account/role cleanup, usable default Member role,
  operator handoff and screenshots.

Do not call the deployment production-ready while these checks remain open.
