# Selected UI quick wins — 2026-09-22

User selection: 1, 2, 3, 6, 7, 11, 15, 22, 27, 28, 31, 32, 33,
34, 36, 37, 45, 46, 64, 93, 99. Existing behavior must be verified rather
than replaced. Preserve the ongoing deployment/media work.

- [x] Voice: persistent server/channel context (1), explicit microphone state
  with icon/text (2/3), capture meter next to mute (6), local muted-speech warning
  (7), distinct camera/share controls (11), explicit leave/disconnect (64).
- [x] Video: actual presented-frame age for paused screen previews (15),
  accessible share volume percent (22), reliable Escape/fullscreen
  controls and persistent participant name (27/28).
- [x] Chat: localized unread separator/pill (31/32), stable scrollback (33),
  consecutive-author grouping (34), localized edit marker (36), explicit scoped
  retry (37), result count and all occurrence highlighting (45/46).
- [x] Files: open containing folder for completed downloads (93), using the
  native recorded transfer destination rather than a server-supplied path.
- [x] Language consistency (99): audit affected voice/chat/video/file controls,
  update both catalogs and verify live language switching.
- [x] Regression tests, browser/native verification, final builds and review.

Implementation order: voice, chat, files, video/previews, integration.

Verification:

- Frontend quality pipeline passed: lint, complete unit suites, eight accessibility
  workflows, production assets and all 50 bundled audio recordings verified.
- After final history reconciliation: UI unit suite and 19 scoped browser
  workflows passed. After visual refinement: 20 selected/accessibility workflows
  passed, including grouping geometry and the visible video name bounds.
- Full client race suite passed; Go linter reported zero issues.
- Two packaged Windows QA clients connected to orderotto-dev. Real checks passed:
  muted capture meter and warning with a continuous fake audio device, disabled
  sender track, peer chat delivery/search, completed file upload/download with
  byte comparison, native folder opening, unknown transfer refusal, German
  controls, remote camera fullscreen/name/Escape with voice still connected.
- Independent review found retry/history races; fixed and reviewed again with
  no remaining actionable findings. History content and concurrent reactions
  reconcile separately, and older-page cursors retain a contiguous range.
- Final packaged production executable: `client/build/bin/noxa.exe`.

Scope boundary: item 15 currently reports the age of the actual paused video
frame, not a continuously refreshed server preview. The separately approved
opt-in stream catalog, periodic preview publication, and watch/stop protocol in
`docs/plans/2026-09-22-opt-in-channel-video.md` remain unfinished. This checklist
does not assert completion of that broader media/deployment task or a complete
translation audit of every unrelated dialog in the application.

Evidence logs: `.cache/ui-selected-quality.log`,
`.cache/ui-selected-final-browser.log`, `.cache/ui-selected-visual-final.log`,
`.cache/ui-selected-client-race.log`, `.cache/ui-selected-client-lint.log`,
`.cache/ui-native-test.log`, `.cache/ui-native-video.log`, and
`.cache/ui-selected-production-build.log`.
