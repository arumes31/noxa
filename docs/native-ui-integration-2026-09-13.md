# Native UI integration — 2026-09-13

Run: `UI-AUDIT-20260913-01`. **Execution and cleanup complete; full acceptance
incomplete because of the blocked and unexecuted scenarios listed below.**
Three independent native guest clients connected. ALPHA and BRAVO completed a
receiver-verified channel conversation, and BRAVO and CHARLIE completed a global
conversation. The repaired clients also completed a private conversation and
reconnected without duplicate tabs after a server restart. Native update testing
found acceptance of a correctly signed Linux executable on Windows; the repaired
native updater now rejects it. A valid update installed and relaunched successfully
with the retained identity, trust, bookmark and recorded settings. Updated ALPHA
completed private, global and channel conversations with unchanged peers, including
a functioning CHARLIE control for private-message exclusion. All three clients
rejected a changed certificate until explicit native trust; conversation recovered
afterward. A saved setting also survived native quit/relaunch. Owned processes and
disposable backing services are stopped; data and evidence are retained.
This report distinguishes native
UI observations, actual remote delivery and audio playback; earlier mocked browser
tests and protocol load tests are not substitutes for these acceptance scenarios.

## Environment and starting state

- Branch `codex/quality-audit-20260913`, commit
  `38e119de87195059afc80d06a39555549bfeafd1`, with the preceding Go 1.27/audit
  changes uncommitted. Starting status and binary diff are preserved in the run's
  ignored evidence directory. Existing changes were retained.
- Windows amd64, Windows build 26200, PowerShell 7.6.6, Go 1.27.1, Node 24.19.0.
  The installed Wails CLI was 2.13.0; this run built and used the client module's
  pinned Wails CLI 2.15.0 in its disposable tool directory.
- Actual Wails/WebView2 executables, not Vite or mocked Wails bindings. Initial
  server/client build identity: `0.4.0-dev+g38e119de8719.dirty.h75ed62282ea2`.
  Later repaired builds are identified below; this is not a single-build final report.
- Initial client executable SHA-256:
  `DE5ED31EEBD9604442C607764C3EA269C78C96C9206A6AF35F931206E89F1055`.
- Server binds loopback TCP/UDP/control/admin/file/health ports 12583–12589.
  TLS remains enabled. PostgreSQL database `noxa_ui_20260913_01` is newly created
  in the labeled disposable audit PostgreSQL container; Redis is the separately
  labeled audit instance. No production/default deployment data is used.
- Three executable copies and profiles: ALPHA, BRAVO, CHARLIE. Each process has
  its own APPDATA, LOCALAPPDATA, WebView2 data, TEMP/TMP, installation and fixture
  directories. Public-key PEM hashes confirm three distinct generated identities.
  DPAPI still belongs to the same Windows account; these are isolated application
  profiles, not three OS security principals.
- Native UI control through `@oai/sky`; one worker assigned per client, with
  coordinator-controlled input turns to avoid desktop focus conflicts.

## Capability boundaries

Native discovery, activation, accessibility inspection and input are available.
An initial combined activation/state capture stalled for 267 seconds and was
interrupted by the coordinator before any connection input occurred. Separate
activation and state calls recovered the actual Wails UI. The first foreground
capture was incomplete; it was not counted as an application-render failure.

Subsequent BRAVO activation attempts failed, including the coordinator attempt
with the exact error `Computer Use app approval timed out`. Resetting the JS
kernel recovered discovery and ALPHA control, but did not establish BRAVO input.
Later ALPHA input and inspection returned `Computer Use helper already has an
active request`. A concurrent task, **Test client update flow**, also uses the
shared helper; it was informed of the contention. No task's approval dialog was
automated or bypassed. These were earlier interruptions: the coordinator later
recovered native input and operated all three clients sequentially while keeping
their sessions active. Workers supplied independent diagnosis and evidence.
Discovery alone was never counted as working input or remote delivery.

Automatic approval review also rejected redeeming the run's one-time bootstrap
key to make ALPHA administrator, stating that the access change lacked specific
authorization. The user was asked to approve that exact isolated-server grant.
The dialog was dismissed without redemption, and no database/API alternative was
used. ALPHA remains a guest pending that approval. All three clients therefore use
distinct guest identities rather than the intended administrator/member/guest
matrix. Echo Test and unassigned channel 0 support the executed conversation slice;
a separate restricted room and its permission matrix remain unestablished.

The host has zero capture and render audio endpoints, although the Windows audio
services are running. Per-process WebView2 fake-capture flags can provide a WAV
fixture to the real `getUserMedia` path. That alone cannot prove receiver playback.
The official process-loopback capture sample needs MSVC/SDK components absent from
this host; a working receiver output has not been established. See
`evidence/audio-capabilities.md` for exact inventory and primary-source references.

UI actions and remote receiver observations are required before scenario PASS.
No packet counter, local mic meter, server response or unit test is promoted to
receiver audio-output evidence.

## Evidence and rerun entry points

Run directory: `temp/ui-20260913-01/`. It contains only disposable data, keys,
profiles, binaries and fixture evidence and is ignored by Git. Never publish its
private keys, tokens or profile contents.

- `start-server.ps1`: explicit disposable DSN, ports, TLS and process logs.
- `start-client.ps1 -Role ALPHA|BRAVO|CHARLIE`: distinct executable/profile copies,
  inherited per-process isolation, PID and binary hash records.
- `evidence/profile-isolation.json`: public-key hashes and profile/build mapping.
- `evidence/client-build.log`, `client-binary-metadata.txt`, `frontend-build.log`:
  actual native build evidence.
- `evidence/alpha-session.md`, `audio-capabilities.md`, `updater-capabilities.md`:
  worker observations and capability assessment.

The first metadata command was mistakenly run from the separate client module
and failed to resolve a root-only tool dependency. It was corrected to run from
the repository root, and the client was rebuilt with verified metadata before
launch. An initial profile check assumed raw base64 public keys; the actual keys
are PEM. The corrected check hashes their PEM representation and confirms distinct
identities. Neither tooling error is classified as an application defect.

## Executed scenarios and acceptance status

All scenarios use the run ID above. Initial build means
`0.4.0-dev+g38e119de8719.dirty.h75ed62282ea2`; the first repaired ALPHA build is
`0.4.0-dev+g38e119de8719.dirty.h9be390a2ca3b`, SHA-256
`6B70F9DE14702E82C26271CAC9DC7DF13A89CD00FC82E868EB99AE82C8C8B873`.

The later DM/reconnect native baseline on both ALPHA and BRAVO was
`0.4.0-ui-audit+g38e119de8719.dirty.h90f7e1c1dd46`, SHA-256
`4C3366AAB8AB2F12064E649690194F65FAAEC4B5272E59BB2847D4C037462F8F`.
This disposable baseline also supplied the unchanged-hash reference for native
updater rejection cases; it is not a published release.

| ID | Actions and observed evidence | Status |
| --- | --- | --- |
| ENV-01 | Built and launched actual server and three Wails executables; readiness returned 200. Separate profile files and public identity hashes verified independently. | PASS: environment only |
| CONN-01 | Initial ALPHA: enter `127.0.0.1:12583` and nickname `ALPHA`, click CONNECT. Native workspace shows connected tab, join line and first TLS pin; server log records guest authentication at 10:44:49.734 +02:00. | PASS: UI state and server acceptance |
| TRUST-01 | Compare first-connect UI fingerprint with the disposable server certificate SHA-256. They match. | PASS: first pin |
| TRUST-02 | Restart owned server using separate `data/tls-rotation`; original certificate/key and all trust files retained. ALPHA automatic retries rejected the changed pin and its original trust hash stayed unchanged. Manual CONNECT on all three clients displayed the independently verified new fingerprint; only explicit Trust new fingerprint allowed connection. | PASS: native mismatch rejection and explicit trust recovery |
| ROLE-01 | Tools → Use a privilege key. Run key entered after scope review; submitting redemption rejected by automatic approval review. Escape closes without granting access. | BLOCKED |
| CONN-02 | After helper recovery, coordinator connected BRAVO and CHARLIE through their actual connection UIs. Three distinct native guest clients were active; subsequent receiver conversations establish functioning peers. First TLS pins matched the disposable certificate. | PASS: native connection and server acceptance |
| UI-DIALOG-01 | Initial Create channel dialog exceeded the default viewport. Native h7650a792ce21 verified bounded panel, heading, manual scrolling and Cancel. Additional h1f8c39678909 keyboard retest: Shift+Tab from Name focused fully visible Cancel; Tab returned to fully visible Name. [Keyboard evidence](../temp/ui-20260913-01/evidence/dialog-keyboard-visible.jpg). | PASS after repair: native layout and keyboard interaction |
| RESTART-01 | Stop only the validated ALPHA PID and relaunch its disposable installation with the repaired executable. Native version notice shows the new build; login identity prefix is unchanged. | PASS: process/build/identity observation only |
| UI-RECENTS-01 | Native restart originally showed no recent servers despite persisted settings. Refresh after settings resolve repaired it. Native h7650a792ce21 and h1f8c39678909 showed ALPHA recent, correct address/nickname prefill and successful connection with the retained pin. | PASS after repair: native UI and server acceptance |
| CHAT-01 | At 15:13 ALPHA sent the uniquely marked CEDAR-48/violet channel prompt. BRAVO read it in Echo Test and replied using that content at 15:14; ALPHA read the exact reply and sender. [BRAVO receipt](../temp/ui-20260913-01/evidence/channel-received-bravo.jpg), [ALPHA reply receipt](../temp/ui-20260913-01/evidence/channel-reply-alpha.jpg). CHARLIE in channel 0 did not receive the exchange. | PASS: remote channel conversation; channel-0 exclusion only |
| CHAT-02 | CHARLIE sent global ORBIT-62 at 15:17. BRAVO read it and sent a content-based global acknowledgement at 15:18; CHARLIE read that acknowledgement. [CHARLIE receipt](../temp/ui-20260913-01/evidence/global-reply-charlie.jpg). | PASS: remote global conversation |
| CHAT-03 | Initial native guest DM failed with `target user not found`; subsequent native PM presentation remained blank. After server/client repairs, BRAVO at 15:59 sent BIRCH-57 with Grüße 🌲. ALPHA read it in the actual PM tab, replied using that content at 16:00, and BRAVO read the exact reply with sender read tick. Evidence: `dm-alpha-received.jpg`, `dm-bravo-reply.jpg`; full exchange below. CHARLIE was disconnected during this exchange. | PASS after repair: native private round trip; third-client isolation not verified |
| CHAT-04 | Initial CEDAR channel history remained visible after isolated server restarts. Content/length boundaries, reactions, search, receipts, broader notifications and different-room/private-history isolation have not been completed. | PASS: observed channel history persistence only; broader matrix unexecuted |
| CHAT-05 | After certificate recovery, CHARLIE at 16:45 sent WILLOW-91 plus literal HTML. ALPHA read it and replied at 16:46, BRAVO read that acknowledgement and confirmed, then CHARLIE read both replies and ALPHA read BRAVO's confirmation. HTML stayed literal with no alert on all three clients. | PASS: native three-way recovery conversation and this HTML rendering probe |
| UI-MEMBERSHIP-01 | ALPHA initially omitted BRAVO when BRAVO existed unassigned before ALPHA connected. At 15:33 on repaired server/client, repeat exact ordering: BRAVO already channel 0, ALPHA connects and joins, then BRAVO joins. ALPHA now shows both clients and count 2. [Receiver tree](../temp/ui-20260913-01/evidence/membership-alpha-green.jpg). | PASS after repair: native membership |
| VOICE-01 | Host inventory has no capture/render endpoint; no isolated receiver playback/capture was established. No source fixture was transmitted. Echo Test local echo was not used as peer evidence. | BLOCKED |
| MEDIA-02 | Peer sessions worked, but isolated capture/playback routes were unavailable. Simultaneous speech, PTT/mute/deafen, whisper and channel/reconnect audio remain blocked; receiver video/share scenarios were not executed. | BLOCKED audio; video/share unexecuted |
| FILE-01 | ALPHA opens Files → Upload, selects generated fixture through native picker and confirms with Return. UI reports insufficient permission `i_ft_file_upload_power`. [Guest denial](../temp/ui-20260913-01/evidence/guest-upload-denied.jpg). | PASS: expected guest upload rejection |
| FILE-02 | Authorized upload/download with receiver hash comparison, cancellation/interruption and boundary cases still require the pending administrator grant/topology. | BLOCKED: authorized transfer unverified |
| RECOVERY-02 | Initial restart duplicated ALPHA tabs ([failure](../temp/ui-20260913-01/evidence/reconnect-duplicate-tabs.jpg)). After repair, server restart at 16:01 reauthenticated all three clients; ALPHA and BRAVO each showed one connected tab. Reopening BRAVO's DM tab showed both persisted messages with correct locks. | PASS after repair: native tab replacement and DM history restoration |
| RECOVERY-04 | After the restart, BRAVO at 16:03 asked CHARLIE globally to reply with WREN-64. CHARLIE read it and replied at 16:04, “CHARLIE received WREN-64 after restart. Global route confirmed.” BRAVO read the exact reply. | PASS: receiver-verified conversation after recovery |
| RECOVERY-03 | Wider three-client reconnect, backing-service/network interruption and bounded repetition matrix was not executed. Observed reconnect resets membership to channel 0; no automatic saved-channel rejoin contract was added. | NOT EXECUTED |
| UPDATE-UI-01 | ALPHA Help → Check for updates against loopback fixture port 12593, mode NONE, reported no update. Dedicated test keys and disposable install used throughout. | PASS: native no-update case |
| UPDATE-UI-02 | Native invalid-signature, untrusted-key, corrupt-download and interrupted-download cases each showed an error; the installed baseline SHA-256 remained unchanged. | PASS: four native rejection cases |
| UPDATE-UI-03 | Initial signed Linux ELF was accepted and failed at restart. On repaired baseline h1ecd7c38075a, the same native flow rejects the ELF for missing DOS header before replacement; installed baseline hash stays unchanged. | PASS after repair: native wrong-platform rejection |
| UPDATE-UI-04 | Native retry with valid candidate reached 100% and applied. Restart now replaced PID 17636 with 26816 at the original executable path. Startup displayed `0.4.1-ui-audit+g38e119de8719.dirty.h197173fb3b93`; candidate hash matched. Subsequent private/global/channel peer conversations pass below. | PASS: native installation/relaunch and post-update chat |
| UPDATE-UI-05 | Temporary current-SID Deny CreateFiles on only the disposable ALPHA install directory caused native `.new` Access denied and “old version still running.” Original SDDL was restored exactly; executable hash unchanged. Native retry then installed successfully. Evidence: `update-unwritable-green.json`. | PASS: native unwritable-path failure and recovery |
| UPDATE-CHAT-01 | Updated ALPHA at 16:33 privately prompted unchanged BRAVO with FERN-83 and Grüße 🌿. BRAVO read it in the actual PM tab and replied at 16:34 using that content; ALPHA read the exact reply and own read ticks. Earlier BIRCH history survived the native update process restart. | PASS: native post-update private conversation and history |
| UPDATE-CHAT-02 | Connected CHARLIE had no FERN content or PM tab. At 16:34 CHARLIE independently sent global CYPRESS-44; updated ALPHA read it and acknowledged at 16:35, and CHARLIE read that acknowledgement. | PASS: DM exclusion with functioning global control; not restricted-room isolation |
| UPDATE-CHAT-03 | ALPHA and BRAVO each double-clicked Echo Test; both showed count 2. ALPHA sent JUNIPER-26 at 16:37; BRAVO read it and replied at 16:38, and ALPHA read the exact reply. | PASS: native post-update channel conversation and membership |
| SETTINGS-01 | At 15:49 ALPHA bookmarked the current server through UI. After update, starred recent remained visible; `pre-update-profile.json` and `post-update-profile.json` verify the original public identity, trust hash, bookmark and recorded settings were retained. | PASS: native bookmark and update preservation |
| SETTINGS-02 | Final ALPHA Tools → Settings: Chat max lines 200 → 250, Apply/OK. Native Connections → Quit exited PID 26816; relaunch of the same candidate/hash as PID 27520, recent-server CONNECT, and reopened Settings showed 250. Rotated trust pin was accepted without another warning. | PASS: native setting persistence, quit/relaunch and retained trust |
| UPDATE-COMP-01 | Initial 11-case signed integration suite passed. Final follow-up expanded to 29 signed executable-replacement integration cases with race detection (183.746s); full client race suite (29.502s) and lint (zero issues) passed. | PASS: component integration; separate native evidence above |
| UPDATE-CANCEL-01 | Current updater disables dismissal while downloading and exposes no cancellation action. | NOT APPLICABLE: unsupported action |

The channel/global rows establish actual remote UI receipt and content-based
replies. CHARLIE's functioning global exchange supports the channel-0 exclusion
observation, but does not establish isolation from a different joined room or a
restricted/private channel. CHARLIE was disconnected during the first BIRCH private
exchange, but the later FERN post-update exchange includes a connected CHARLIE
exclusion observation and independent successful global control. This supports
observed DM isolation for that exchange. Authorized file integrity and receiver
audio playback have not passed. Final multi-client acceptance is incomplete.

## Repairs and verification

The dialog repair adds viewport width/height bounds and scrolling to shared
`.dlg`. The regression checks fully visible heading, focus-driven scrolling to
fully visible Create/Cancel, and mocked submission at a 1024×730 content viewport.
Evidence: `channel-dialog-red.log`, `channel-dialog-green.log`,
`channel-dialog-repair.md`, and `channel-dialog-browser-suite.log`.

Initial repair validation: 82 browser tests, frontend lint, 70 core unit tests,
8 UI unit tests and production frontend build passed. Wails 2.15.0 rebuilt the
native executable with Go 1.27.1; the actual new version was observed after launch.
The subsequent native manual-scroll and keyboard retests passed as recorded in
UI-DIALOG-01; these are separate evidence from mocked browser submission.

The recent-server repair redraws the list after initial native settings loading.
Its regression delays the real startup `GetSettings` mock, resolves persisted
recents, and verifies that the displayed server can populate the connection form
without a manual render hook. Focused checks passed 2/2; that repair's full browser
suite passed **83/83** without retries, including accessibility, and lint passed.
Evidence: `recent-startup-{red,green,browser-suite,lint}.log` and
`recent-startup-repair.md`.

The first combined repaired Wails build was
`0.4.0-dev+g38e119de8719.dirty.h7650a792ce21`, SHA-256
`024BE1D5FEE0984E43C9FF73FB89A315963D9FEF1171071E1F6734B7C3B265E9`.
All three disposable profiles launched that exact executable; process paths and
hashes were verified, and the original distinct public identities survived.
Evidence: `final-build.json`, `final-profile-processes.json`,
`frontend-build-complete.log`, `client-build-complete.log`. Despite those historical
filenames, this build was followed by further native findings and repairs.

The additional keyboard focus repair was native-verified on
`0.4.0-dev+g38e119de8719.dirty.h1f8c39678909`, SHA-256
`350948EB6761A068E3B5DEC91E0E22268E48E0BAFB6C6BA8493A10FE5038C148`.
Its full browser suite passed 83 tests without retries. Evidence:
`keyboard-build.json`, `dialog-keyboard-{red,green,browser-suite,lint,unit}.log`,
and `dialog-keyboard-visible.jpg`. Recents were also native-verified on this build.

The membership repair adds optional `unassigned_clients` to the server snapshot
using the same visibility filter as joined clients. Frontend ingestion retains
those identities so later move events can update them; duplicate self-join events
merge with existing metadata. Older clients ignore the additive JSON field and
older servers omit it; both repaired sides are needed for this scenario. There
is no protobuf message-ID or framing change. RED tests reproduced missing
unassigned identities and count 1 instead of 2; broadcast race tests and the
84-test browser suite passed. Details: `membership-snapshot-repair.md` and the
`membership-*` logs.

Native membership retest passed at 15:33 on ALPHA client
`0.4.0-dev+g38e119de8719.dirty.hdfc8ae9af623`, SHA-256
`686327F2D97D13AB25FA7874CFFCC5AB129ACAE54DBBAC436DD6DC246A480E0C`,
and server `0.4.0-dev+g38e119de8719.dirty.hc3c97fa3abcc`, SHA-256
`6FBECA8931508F4DEAF57D493FB4FEE8D39526E7E3BA316FA853715AB9AD72C3`.
ALPHA's receiver tree now contains both clients after repeating the original
ordering (`membership-alpha-green.jpg`).

The reconnect repair carries the exact dropped tab ID through the retry series
and retires that disconnected tab after a successful replacement. Failed retries,
unrelated same-address tabs and a source that is connected again remain. RED
reproduced old-plus-new accumulation; 10 reconnect component regressions, frontend
lint and build pass. Evidence: `reconnect-tabs-repair.md`,
`reconnect-tabs-{red,final,lint,build}.log`. **Native repeat passed at 16:01:** both
ALPHA and BRAVO showed one connected tab after the isolated server restarted.
All three clients authenticated again, and BRAVO/CHARLIE completed the WREN-64
conversation afterward. Existing channel-0 membership reset is unchanged.

The first private-message repair routes to the live authenticated identity registry
before requiring a registered account row, allowing online guest recipients.
Real TLS protocol regressions reproduced `target user not found` and then passed
guest/registered combinations, unknown/offline cases and encrypted-spool rules;
the full server race suite passed in 36.635 seconds and server lint reported zero
issues. Evidence: `guest-dm-repair.md`, `guest-dm-{red,green,server-suite,lint}.log`.
The first native rerun still showed blank/missing PM presentation. Actual-box and
browser regressions traced this to loss of private scope when native decryption
stripped ciphertext flags, plus use of the sender's own key instead of the
recipient key to open a sender echo. Global rendering was reproduced by the
browser regression; it was not a separately observed native result.

The client/protocol repair preserves explicit private scope and recipient identity,
routes sender echoes to their actual recipient, and shows verified encryption only
after successful native opening. Local identity-sealed DM history preserves that
verification provenance; legacy/plaintext entries do not gain a false verified
lock. The fields are additive JSON metadata; old-server echoes without recipient
metadata remain unverified private placeholders. Details:
`dm-presentation-repair.md`, `dm-{presentation,routing-ui}-{red,green}.log`,
`dm-verification-review-red.log`, `dm-history-review-red.log`.

Native retest on the baseline identified above completed this actual exchange:

- BRAVO, 15:59: `[UI-AUDIT-20260913-01] ALPHA, private reply with BIRCH-57 and Grüße 🌲.`
- ALPHA read it in the actual PM tab and replied at 16:00:
  `[UI-AUDIT-20260913-01] ALPHA received BIRCH-57 and Grüße 🌲 privately. BRAVO, confirmed.`
- BRAVO read the exact reply in its PM tab, with the sender read tick. Evidence:
  [ALPHA PM receipt](../temp/ui-20260913-01/evidence/dm-alpha-received.jpg),
  [BRAVO reply receipt](../temp/ui-20260913-01/evidence/dm-bravo-reply.jpg).

After the 16:01 server restart, BRAVO reopened the PM tab and saw both persisted
messages with their correct locks. CHARLIE was disconnected during this original
private exchange; the later post-update FERN scenario supplies the connected
third-client exclusion/control observation. The repair
does not redesign asynchronous send-acknowledgement behavior.

Final DM-source automated gates passed: 89 browser tests, 70 core plus 8 UI unit
tests with unchanged coverage gates, client-module race suite (33.468 seconds),
netproto/server/broadcast race suites (2.574/35.075/3.310 seconds), frontend lint
and affected root/client Go lint. Evidence: `dm-browser-all.log`,
`dm-frontend-unit.log`, `dm-client-race.log`, `dm-root-race.log`, and `dm-*-lint.log`.

Updater implementation and its integration tests were added concurrently by
**Test client update flow** and were preserved. This audit did not implement or
publish those changes. Its fresh component command was:

```powershell
Set-Location client
$env:GOTOOLCHAIN = 'go1.27.1'
go test -race -tags=integration -count=1 -json -run 'Test(CheckForUpdate|VerifySignedManifest|DownloadTo|DownloadAndApply|ApplyAndRestart|BeforeClose)' ./...
```

It passed in 80.534 seconds. Evidence: `updater-component-tests.jsonl`. The
integration child processes replace disposable executable fixtures; this does
not prove the full Wails update interaction or post-update peer communication.
Restart-error handling and close-to-tray repairs already exist in the concurrent
updater task; this audit does not report its earlier hypotheses as unfixed defects.

Native updater execution subsequently passed no-update, invalid-signature,
untrusted-key, corrupt-download and interrupted-download cases. Each rejection
left the verified baseline executable hash unchanged. The signed wrong-platform
case then established a separate application defect: the installed Windows path
became the test Linux ELF with SHA-256
`D4C2FCBB78DC61AD3C8787A1FCBD8B321709F060558BEED39CF3886F8C1B3CAC`.
The UI said the update was applied and required restart, then displayed the Windows
incompatibility error when Restart now was clicked. Evidence:
`update-wrong-platform-red.json`,
[native applied state](../temp/ui-20260913-01/evidence/update-wrong-platform-applied.jpg),
[native restart failure](../temp/ui-20260913-01/evidence/update-wrong-platform-restart.jpg).
These are native observations and an installed
file hash, not merely an inspection concern.

The original ALPHA process remained running from its `.old` executable. Recovery
restored only the audit-owned ALPHA path from the verified baseline. The subsequent
platform-validation repair retains signature and checksum verification and rejects
incompatible executable headers before self-replacement. On repaired baseline
`0.4.0-ui-audit+g38e119de8719.dirty.h1ecd7c38075a`, SHA-256
`531E9A6F949C5DD4FD8EED5E5DF00A4E7EA89F1E98A2325688979E9C656A6B9A`,
the native wrong-platform retry reported a missing DOS header and preserved that
exact hash. Details: `updater-platform-repair.md`,
`updater-{platform,header}-{red,green}.log`.

For the native unwritable-path scenario, a temporary current-SID Deny CreateFiles
entry applied only to the disposable ALPHA installation directory. Update now
reported `self-update failed (old version still running)` with `.new: Access is
denied`. The original directory SDDL was restored exactly and the installed
baseline hash remained unchanged (`update-unwritable-green.json`). A subsequent
native retry succeeded, establishing recovery after restoring write access.

The installed final candidate SHA-256 was
`3FCE2A141EF79817DF8A411D212534EE8AE71868990FDF980CAF1D89390D638D`.
The updater showed 100% and applied; Restart now changed ALPHA PID 17636 → 26816
at the same original executable path. Native startup displayed
`0.4.1-ui-audit+g38e119de8719.dirty.h197173fb3b93` and retained the starred recent.
`post-update-profile.json` verifies the original public identity PEM hash, trust
hash, bookmark and recorded settings against `pre-update-profile.json`.

The final updater follow-up passed 29 signed integration cases under race detection
in 183.746 seconds, full client race in 29.502 seconds, and lint with zero issues
(`updater-header-green.log`, `updater-header-client-race.log`,
`updater-header-lint.log`). These component checks supplement the actual native
rejection, installation and relaunch observations.

Post-update native communication used ALPHA PID 26816 on h197173fb3b93, unchanged
BRAVO PID 16332 on h90f7e1c1dd46, and connected CHARLIE PID 25340 on
h7650a792ce21. The coordinator observed these actual conversations:

- ALPHA, 16:33: `[UI-AUDIT-20260913-01] BRAVO, after update reply privately with FERN-83 and Grüße 🌿.`
  BRAVO read it in the actual PM tab and replied at 16:34:
  `[UI-AUDIT-20260913-01] BRAVO received FERN-83 and Grüße 🌿 privately after your update.`
  ALPHA read the reply and own read ticks. Its prior BIRCH history was also retained
  across the native update process restart. Evidence:
  [ALPHA reply receipt](../temp/ui-20260913-01/evidence/update-dm-reply-alpha.jpg),
  [BRAVO PM receipt](../temp/ui-20260913-01/evidence/update-dm-received-bravo.jpg).
- CHARLIE stayed connected, with no FERN content in its global view and no PM tab.
  Its independent 16:34 positive control was:
  `[UI-AUDIT-20260913-01] ALPHA, global check after update: please acknowledge CYPRESS-44.`
  ALPHA read it and replied at 16:35:
  `[UI-AUDIT-20260913-01] ALPHA acknowledges CYPRESS-44 globally from the updated client.`
  CHARLIE read the exact acknowledgement
  ([positive control](../temp/ui-20260913-01/evidence/update-charlie-control.jpg)). This
  establishes a functioning observer for DM exclusion, not a restricted-room test.
- Both ALPHA and BRAVO double-clicked Echo Test and saw count 2. ALPHA, 16:37:
  `[UI-AUDIT-20260913-01] BRAVO, channel check after update: reply JUNIPER-26.`
  BRAVO read it and replied at 16:38:
  `[UI-AUDIT-20260913-01] BRAVO received JUNIPER-26 in Echo Test after ALPHA updated.`
  ALPHA read the reply. Evidence:
  [ALPHA channel reply](../temp/ui-20260913-01/evidence/update-channel-reply-alpha.jpg),
  [BRAVO channel receipt](../temp/ui-20260913-01/evidence/update-channel-received-bravo.jpg).

These are visible native receiver observations, separate from server acceptance
and automated component results. No receiver audio playback is inferred from
the successful chats or Echo Test membership. The local update fixture used port
12593 and was stopped during final cleanup.

## Final trust and settings checks

The owned server restarted as PID 4680 with a separate `data/tls-rotation`
certificate directory. Original certificate/key files and all client trust stores
were retained. Independent X509 inspection produced the new certificate SHA-256
`25A149000892547AD7B224D6C16161D11810A8B7764BA7EDD0E5A6BF8CCA83BA`.
ALPHA's automatic retries rejected the mismatch. Its original known-servers file
hash stayed
`6AEA0062EAA286FB5CCC91D4CAEEA970DAF8E8EBB32B71428528D0D51B0A958E`
before explicit consent. All three manual CONNECT flows displayed “Server
certificate changed” with the exact new fingerprint. Clicking Trust new fingerprint
allowed each to connect; no trust file was removed as a workaround. These manual
new connections retained old disconnected tabs, unlike the repaired automatic
retry replacement scenario.
[Native certificate warning](../temp/ui-20260913-01/evidence/trust-changed-dialog.jpg).

Native three-way conversation after trust recovery:

- CHARLIE, 16:45: `[UI-AUDIT-20260913-01] ALPHA, certificate recovery check: reply WILLOW-91. Literal HTML: <img src=x onerror="alert(123)">`
- ALPHA read it and replied at 16:46:
  `[UI-AUDIT-20260913-01] ALPHA received WILLOW-91 after certificate recovery. The HTML is literal text. BRAVO, confirm receipt.`
- BRAVO read the acknowledgement and replied at 16:46:
  `[UI-AUDIT-20260913-01] BRAVO confirms WILLOW-91 and ALPHA’s acknowledgement after certificate recovery.`
- CHARLIE read both replies and ALPHA read BRAVO's confirmation. The HTML appeared
  as literal text and caused no alert on all three clients. This verifies this
  particular rendering probe, not every markdown/link/filename boundary.
  [CHARLIE recovery receipt](../temp/ui-20260913-01/evidence/trust-recovery-charlie.jpg).

Final ALPHA h197173fb3b93 then changed Chat max lines from 200 to 250 through
Tools → Settings → Apply/OK. Native Connections → Quit exited PID 26816.
Relaunch via the isolated script created PID 27520 from the same candidate SHA-256;
recent-server CONNECT accepted the retained rotated pin without a warning, and
reopened Settings showed 250.
[Persisted native setting](../temp/ui-20260913-01/evidence/settings-persisted.jpg).

## Remaining scope

The following blocked work is distinct from scenarios that simply were not
executed during this run:

- **BLOCKED — audio:** no isolated receiver output/capture route. Two-way receiver
  playback, simultaneous speakers, PTT/global hotkeys, VAD, mute/deafen, device
  recovery, whisper and channel/reconnect media acceptance remain unverified.
- **BLOCKED — administrator-dependent topology:** the pending bootstrap-role
  approval prevented administrator/member/guest roles, separate restricted rooms,
  moderation/revocation tests and authorized file transfer with receiver hash
  comparison. Channel-0 and DM exclusion observations do not fill these gaps.
- **NOT EXECUTED — broader messaging boundaries:** simultaneous sends, complete
  length/Unicode/multiline/link/filename matrix, rapid channel-switch races, search,
  reactions/pins, broader unread/notification/scroll behavior, and offline delivery
  beyond the recorded history observations.
- **NOT EXECUTED — broader recovery/load:** 20-cycle reconnect/channel-switch run,
  sustained resource-growth measurement, active native UI PostgreSQL/Redis outage,
  and isolated network-interruption matrices. Earlier protocol/chaos tests remain
  separate evidence.
- **NOT EXECUTED — remaining native surfaces:** video/screen-share receiver motion,
  avatars, all device/hotkey/notification settings, tray/minimize combinations and
  the full small-window/accessibility matrix. Recorded dialog focus, recents,
  bookmark, quit/relaunch and one settings-persistence case passed.
- **NOT APPLICABLE:** updater cancellation has no implemented control. Native
  no-update, invalid signature/key, corrupt/interrupted download, wrong platform,
  unwritable path, valid install/relaunch and profile retention were exercised.

No blanket full-acceptance PASS is claimed.

## Reusable runner and cleanup

Tracked entry point: `scripts/native-ui-session.ps1`, requiring Windows,
PowerShell 7.4+, Docker, Go and Node/npm. A fresh run builds the actual server and
Wails client, creates labeled loopback-only PostgreSQL/Redis containers, waits for
readiness, and launches three independent profiles. It records exact process
paths/start times, container IDs/labels and build hashes. It does not automate
native interaction. As of 23 September, it provisions disposable Alice/Bob/owner
accounts and activates roles-v1 before starting the server. Credentials and the
live-test environment are retained in the run's access-restricted `secrets`
directory; the `test` action loads and restores that environment automatically.

```powershell
pwsh -File ./scripts/native-ui-session.ps1 start -RunId ui-rerun-20260913-02 -BasePort 13583
pwsh -File ./scripts/native-ui-session.ps1 status -RunId ui-rerun-20260913-02
pwsh -File ./scripts/native-ui-session.ps1 test -RunId ui-rerun-20260913-02
pwsh -File ./scripts/native-ui-session.ps1 stop -RunId ui-rerun-20260913-02
```

Runner validation used a separate `harness-check-20260913` run with supplied real
binaries and `-SkipBuild -NoClients`, on ports 14683–14691. Actual server readiness,
status, duplicate-run refusal, refusal to terminate a mismatched executable and
successful cleanup passed. An ISO timestamp comparison problem found in that
validation was corrected before the successful rerun. The normal build/client
launch branches were not exercised by this setup validation. Evidence:
`temp/native-ui-harness-check-20260913/evidence/harness-validation.md`.

**Final cleanup completed at 2026-09-13 16:51:35 +02:00.** Executable-path
verification preceded stopping ALPHA 27520, BRAVO 16332, CHARLIE 25340 and server
4680. The owned update-fixture Node process 27928 was stopped after verifying both
its executable and fixture-script command line. Both exact audit container names
and ownership labels were verified before stopping PostgreSQL and Redis. The
separate runner-validation server and its two containers are also stopped.

All databases, volumes, identity/trust keys and evidence remain. The original
ALPHA installation SDDL was restored exactly. No unrelated updater process was
stopped. [Cleanup record](../temp/ui-20260913-01/evidence/cleanup.json).

The retained session can resume with its explicitly trusted rotated certificate.
This example launches the final candidate into all three retained profiles:

```powershell
docker start noxa-audit-20260913-postgres noxa-audit-20260913-redis
pwsh -File ./temp/ui-20260913-01/start-server-rotated-cert.ps1
foreach ($role in 'ALPHA','BRAVO','CHARLIE') {
    pwsh -File ./temp/ui-20260913-01/start-client.ps1 -Role $role
}
```

`start-client.ps1 -Role ...` copies common `bin/client.exe`, now final candidate
h197173fb3b93, into the selected profile before launch. It does not preserve the
older peer executables used for the mixed-version comparison. Verify each intended
binary hash and profile environment when repeating that comparison. The tracked
runner above creates a fresh three-client setup for a new run.

Native platform rejection, valid update/relaunch, profile comparison, DM/reconnect
retests, post-update conversations, changed-trust recovery and settings persistence
pass. Remaining scope is listed above.
Redeem ALPHA's run-only key only after the pending approval, then establish the
intended roles/topology and authorized file-transfer scenarios.
Receiver playback still requires an isolated working output/capture route.
Changed certificates must use the real warning/trust flow; retained trust storage
must not be deleted. Test-only update trust must remain in disposable builds.

Raw logs and generated private fixtures remain local and ignored. Use
`evidence/server-redacted.log` for sharing; do not share the raw bootstrap log,
signing seed or identity files. This audit performed no commit, push, production
release or replacement of the user's installed client.
